package danmaku

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// DefaultURL B 站弹幕服务器地址。
const DefaultURL = "wss://broadcastlv.chat.bilibili.com/sub"

// OnMessage 消息回调。
type OnMessage func(Message)

// Options 弹幕客户端配置。
type Options struct {
	// URL 弹幕服务器地址，空时使用 DefaultURL（公共服务器通常需要 token）。
	URL string
	// RoomID 真实房间号。
	RoomID int
	// Token getDanmuInfo 获取的认证凭证；为空时使用公共服务器（可能被拒）。
	Token string
	// HeartbeatInterval 心跳间隔，默认 30 秒。
	HeartbeatInterval time.Duration
	// Logger 日志器，可为 nil。
	Logger *slog.Logger
}

// Client B 站弹幕 WebSocket 客户端。
// 断线自动重连，Run 返回后由上层决定是否继续（常驻线程）。
type Client struct {
	opts      Options
	onMsg     OnMessage
	closeOnce sync.Once
	closeCh   chan struct{}
	mu        sync.Mutex
	conn      *websocket.Conn
}

// New 创建弹幕客户端。onMsg 在每条弹幕/消息到达时被调用（运行于读循环 goroutine）。
func New(opts Options, onMsg OnMessage) *Client {
	if opts.URL == "" {
		opts.URL = DefaultURL
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Client{opts: opts, onMsg: onMsg, closeCh: make(chan struct{})}
}

// SetEndpoint 在启动前设置弹幕服务器地址与认证 token
// （由调用方通过 getDanmuInfo 获取，见 internal/bili）。
func (c *Client) SetEndpoint(url, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if url != "" {
		c.opts.URL = url
	}
	c.opts.Token = token
}

// Run 连接弹幕服务器并持续监听，直到 ctx 取消或 Close 被调用。
// 内部自动处理断线重连（退避等待）。
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		err := c.runOnce(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closeCh:
			return nil
		default:
		}
		if !errors.Is(err, context.Canceled) {
			c.opts.Logger.Warn("弹幕连接中断，等待重连", "err", err, "backoff", backoff)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closeCh:
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// runOnce 建立一次连接并处理完整个会话。
func (c *Client) runOnce(ctx context.Context) error {
	// 浏览器 UA 与 Origin：B 站弹幕服务器会拒绝非浏览器来源。
	// 注意：不要声明子协议（binary），B 站服务器会断开不认识的子协议握手。
	headers := http.Header{}
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	headers.Set("Origin", "https://live.bilibili.com")

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.opts.URL, headers)
	if err != nil {
		return fmt.Errorf("连接弹幕服务器失败: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		_ = conn.Close()
	}()

	// 认证
	if err := conn.WriteMessage(websocket.BinaryMessage, PackAuth(c.opts.RoomID, c.opts.Token)); err != nil {
		return fmt.Errorf("发送认证包失败: %w", err)
	}
	c.opts.Logger.Info("弹幕服务器已连接，等待认证...", "room_id", c.opts.RoomID)

	// 心跳循环
	heartCtx, cancelHeart := context.WithCancel(ctx)
	defer cancelHeart()
	go func() {
		ticker := time.NewTicker(c.opts.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.BinaryMessage, PackHeartbeat()); err != nil {
					c.opts.Logger.Debug("心跳发送失败", "err", err)
				}
			case <-heartCtx.Done():
				return
			}
		}
	}()

	// 读循环
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("读取弹幕消息失败: %w", err)
		}
		if err := c.handle(data); err != nil {
			c.opts.Logger.Debug("处理弹幕消息失败", "err", err)
		}
	}
}

// handle 处理一帧 WebSocket 消息。
func (c *Client) handle(data []byte) error {
	packets, err := Decode(data)
	if err != nil {
		return err
	}
	for _, p := range packets {
		switch p.Operation {
		case opAuthReply:
			var authResp struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(p.Body, &authResp); err == nil && authResp.Code == 0 {
				c.opts.Logger.Info("弹幕认证成功", "room_id", c.opts.RoomID)
			} else {
				c.opts.Logger.Warn("弹幕认证异常", "code", authResp.Code)
			}
		case opHeartbeatReply:
			if len(p.Body) >= 4 {
				popularity := binary.BigEndian.Uint32(p.Body)
				c.opts.Logger.Debug("心跳回复（人气）", "popularity", popularity)
			}
		case opMessage:
			if c.onMsg == nil {
				continue
			}
			msg, err := ParseMessage(p)
			if err != nil {
				continue
			}
			c.onMsg(msg)
		}
	}
	return nil
}

// Close 关闭客户端并释放连接。
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		c.mu.Lock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.mu.Unlock()
	})
}
