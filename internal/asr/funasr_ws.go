package asr

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// FunASROptions FunASR WebSocket 客户端参数。
type FunASROptions struct {
	// Endpoint FunASR 服务地址，如 ws://127.0.0.1:10095。
	Endpoint string
	// Mode 识别模式：2pass（推荐）、online、offline。
	Mode string
	// ChunkSize chunk 大小 [encoder, decoder, offset]。
	ChunkSize []int
	// WavName 传给服务端的音频名。
	WavName string
	// Hotwords 热词表（权重 1-5）。
	Hotwords map[string]int
	// ITN 是否启用标点/数字逆文本正则化。
	ITN bool
}

// FunASRWS 对接 FunASR runtime WebSocket 服务的流式客户端。
//
// 协议（funasr/runtime/python/websocket/funasr_wss_client.py）：
//  1. 文本帧发送 JSON header：{"mode":"2pass","chunk_size":[...],"wav_name":...,"is_speaking":true,"itn":true,"hotwords":"{...}"}
//  2. 二进制帧发送 PCM s16le 音频字节
//  3. 服务端文本帧返回：{"mode":"2pass-online","text":...} 或 {"mode":"2pass-offline","text":...}
//  4. 结束：发送 {"mode":"2pass","wav_name":...,"is_speaking":false}，等待 offline 最终结果
type FunASRWS struct {
	opts FunASROptions

	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	textCh  chan string // 在线增量文本
	final   chan struct{}
	pending string // drain 累积缓冲
	mu      sync.Mutex
	closed  bool
}

// NewFunASRWS 创建 FunASR 客户端（惰性连接，首次调用时建连）。
func NewFunASRWS(opts FunASROptions) (*FunASRWS, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("funasr-ws 需要配置 endpoint")
	}
	if opts.Mode == "" {
		opts.Mode = "2pass"
	}
	if len(opts.ChunkSize) != 3 {
		opts.ChunkSize = []int{0, 10, 5}
	}
	if opts.WavName == "" {
		opts.WavName = "hymREC_live"
	}
	opts.ITN = true
	return &FunASRWS{
		opts:   opts,
		textCh: make(chan string, 64),
		final:  make(chan struct{}, 1),
	}, nil
}

// header 构造认证/开始帧。
func (f *FunASRWS) header(isSpeaking bool) ([]byte, error) {
	hotword, err := json.Marshal(f.opts.Hotwords)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"mode":        f.opts.Mode,
		"chunk_size":  f.opts.ChunkSize,
		"wav_name":    f.opts.WavName,
		"is_speaking": isSpeaking,
		"itn":         f.opts.ITN,
		"hotwords":    string(hotword), // 官方服务端字段为复数形式
	}
	return json.Marshal(payload)
}

// ensureConnected 建立连接并发送开始帧（幂等）。
func (f *FunASRWS) ensureConnected() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	dialer := websocket.Dialer{Subprotocols: []string{"binary"}} // 服务端声明 subprotocol=binary
	conn, _, err := dialer.DialContext(ctx, f.opts.Endpoint, nil)
	if err != nil {
		cancel()
		return fmt.Errorf("连接 FunASR 服务失败: %w", err)
	}
	header, err := f.header(true)
	if err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("构造 FunASR header 失败: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, header); err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("发送 FunASR header 失败: %w", err)
	}
	f.conn = conn
	f.ctx = ctx
	f.cancel = cancel
	go f.readLoop(conn)
	return nil
}

// readLoop 读取服务端响应。
func (f *FunASRWS) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			// 触发 Flush 后服务端关闭连接属正常结束
			return
		}
		var resp struct {
			Mode    string `json:"mode"`
			Text    string `json:"text"`
			IsFinal bool   `json:"is_final"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.Text != "" {
			select {
			case f.textCh <- resp.Text:
			default:
			}
		}
		if resp.IsFinal {
			select {
			case f.final <- struct{}{}:
			default:
			}
		}
	}
}

// ProcessChunk 发送一块 PCM 并返回服务端返回的增量文本。
func (f *FunASRWS) ProcessChunk(pcm []int16) (string, error) {
	if len(pcm) == 0 {
		return "", nil
	}
	if err := f.ensureConnected(); err != nil {
		return "", err
	}
	// int16 -> 小端字节
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	f.mu.Lock()
	err := f.conn.WriteMessage(websocket.BinaryMessage, buf)
	f.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("发送音频到 FunASR 失败: %w", err)
	}
	// 短暂收集响应，避免阻塞流水线
	return f.drain(100 * time.Millisecond), nil
}

// Flush 通知服务端本轮结束，返回最终文本并重开连接。
func (f *FunASRWS) Flush() (string, error) {
	f.mu.Lock()
	if f.conn == nil {
		f.mu.Unlock()
		return "", nil
	}
	header, err := f.header(false)
	if err != nil {
		f.mu.Unlock()
		return "", err
	}
	err = f.conn.WriteMessage(websocket.TextMessage, header)
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("发送 FunASR 结束帧失败: %w", err)
	}

	// 等 offline 最终结果（最多 5 秒）
	tail := f.drain(0)
	select {
	case <-f.final:
		tail += f.drain(500 * time.Millisecond)
	case <-time.After(5 * time.Second):
	}
	_ = conn.Close()
	return tail, nil
}

// drain 从文本通道收集并返回拼接文本；d 为 0 时立即返回。
func (f *FunASRWS) drain(d time.Duration) string {
	if d > 0 {
		deadline := time.After(d)
		for {
			select {
			case t := <-f.textCh:
				if f.pending == "" {
					f.pending = t
				} else {
					f.pending += t
				}
			case <-deadline:
				out := f.pending
				f.pending = ""
				return out
			}
		}
	}
	out := f.pending
	f.pending = ""
	for {
		select {
		case t := <-f.textCh:
			out += t
		default:
			return out
		}
	}
}

// Close 关闭连接。
func (f *FunASRWS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.conn != nil {
		_ = f.conn.Close()
		f.conn = nil
	}
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	return nil
}
