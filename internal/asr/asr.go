// Package asr 定义语音识别引擎接口，并提供 None（关闭）与
// Mock（演示/测试）两种内置实现；FunASR WebSocket 实现见 funasr_ws.go。
package asr

import (
	"fmt"
	"time"

	"github.com/henyemiren/hymrec/internal/config"
)

// Engine 流式语音识别引擎接口。
type Engine interface {
	// ProcessChunk 处理一块 16kHz 单声道 PCM 音频，返回增量识别文本。
	ProcessChunk(pcm []int16) (string, error)
	// Flush 结束本轮识别，返回最终（补全）文本。
	Flush() (string, error)
	// Close 释放资源。
	Close() error
}

// New 根据配置创建识别引擎。
func New(cfg config.ASRConfig) (Engine, error) {
	switch cfg.Type {
	case "none":
		return &noneEngine{}, nil
	case "mock":
		return NewMock(MockOptions{
			Texts:    cfg.MockTexts,
			Interval: cfg.MockInterval,
		}), nil
	case "funasr-ws":
		return NewFunASRWS(FunASROptions{
			Endpoint:  cfg.Endpoint,
			Mode:      cfg.Mode,
			ChunkSize: cfg.ChunkSize,
			WavName:   cfg.WavName,
			Hotwords:  cfg.Hotwords,
		})
	}
	return nil, fmt.Errorf("未知 ASR 类型 %q", cfg.Type)
}

// noneEngine 关闭语音识别（仅依赖弹幕触发）。
type noneEngine struct{}

func (e *noneEngine) ProcessChunk(pcm []int16) (string, error) { return "", nil }
func (e *noneEngine) Flush() (string, error)                   { return "", nil }
func (e *noneEngine) Close() error                             { return nil }

// MockOptions Mock 引擎参数。
type MockOptions struct {
	// Texts 轮询输出的文本；为空时使用默认演示文本。
	Texts []string
	// Interval 输出间隔，默认 8 秒。
	Interval time.Duration
}

// defaultMockTexts 模拟"主播提及关键词"的演示文本。
func defaultMockTexts() []string {
	return []string{
		"欢迎新来的朋友们",
		"今天我们来聊聊最近的热点新闻",
		"说到这个话题，其实我最近也在关注",
		"大家知道星瞳吗，她最近的新歌很好听",
		"啊，刚才说到哪了",
	}
}

// NewMock 创建 Mock 引擎：按固定间隔轮询输出文本。
// 用途：无 FunASR 服务器时演示完整触发-录制流水线、集成测试。
func NewMock(opts MockOptions) Engine {
	if len(opts.Texts) == 0 {
		opts.Texts = defaultMockTexts()
	}
	if opts.Interval <= 0 {
		opts.Interval = 8 * time.Second
	}
	return &mockEngine{texts: opts.Texts, interval: opts.Interval}
}

type mockEngine struct {
	texts    []string
	interval time.Duration
	idx      int
	last     time.Time
}

func (e *mockEngine) ProcessChunk(pcm []int16) (string, error) {
	now := time.Now()
	if e.last.IsZero() {
		e.last = now
		return "", nil
	}
	if now.Sub(e.last) < e.interval {
		return "", nil
	}
	e.last = now
	text := e.texts[e.idx%len(e.texts)]
	e.idx++
	return text, nil
}

func (e *mockEngine) Flush() (string, error) { return "", nil }
func (e *mockEngine) Close() error           { return nil }
