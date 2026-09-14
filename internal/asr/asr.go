// Package asr 定义语音识别引擎接口，并提供 None（关闭）实现；
// FunASR WebSocket 实现见 funasr_ws.go。
package asr

import (
	"fmt"

	"github.com/henyemiren/hymREC/internal/config"
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
	case "funasr-ws":
		return NewFunASRWS(FunASROptions{
			Endpoint:  cfg.Endpoint,
			Mode:      cfg.Mode,
			ChunkSize: cfg.ChunkSize,
			WavName:   cfg.WavName,
			Hotwords:  cfg.Hotwords,
		})
	}
	return nil, fmt.Errorf("未知 ASR 类型 %q（仅支持 none|funasr-ws）", cfg.Type)
}

// noneEngine 关闭语音识别（仅依赖弹幕触发）。
type noneEngine struct{}

func (e *noneEngine) ProcessChunk(pcm []int16) (string, error) { return "", nil }
func (e *noneEngine) Flush() (string, error)                   { return "", nil }
func (e *noneEngine) Close() error                             { return nil }
