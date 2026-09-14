// Package capture 通过 ffmpeg 管道从直播流中提取 16kHz 单声道 PCM 音频，
// 供 ASR 引擎使用。流中断时返回 io.EOF，由调用方按需重连。
package capture

import (
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
)

// Options 音频采集参数。
type Options struct {
	// StreamURL 直播流地址（FLV 或 HLS）。
	StreamURL string
	// FFmpegPath ffmpeg 可执行文件路径，默认 "ffmpeg"。
	FFmpegPath string
	// SampleRate 输出采样率（Hz），默认 16000。
	SampleRate int
	// ChunkSamples 每次 Next() 返回的采样点数。
	ChunkSamples int
}

// Source 从 ffmpeg 标准输出读取 PCM 的采集器。
type Source struct {
	opts Options
	cmd  *exec.Cmd
	out  io.ReadCloser
}

// New 创建采集器（不启动）。
func New(opts Options) (*Source, error) {
	if opts.FFmpegPath == "" {
		opts.FFmpegPath = "ffmpeg"
	}
	if opts.SampleRate == 0 {
		opts.SampleRate = 16000
	}
	if opts.ChunkSamples == 0 {
		opts.ChunkSamples = 9600 // 600ms @16kHz
	}
	return &Source{opts: opts}, nil
}

// biliHeaders ffmpeg -headers 参数：B 站 CDN 部分节点校验 Referer/UA。
const biliHeaders = "Referer: https://live.bilibili.com/\r\n" +
	"User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36\r\n"

// ffmpegArgs 构造 ffmpeg 命令。
// -reconnect 系列参数让 ffmpeg 在拉流中断时自动重连（对 HTTP-FLV/HLS 生效）。
func (s *Source) ffmpegArgs() []string {
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "10",
		"-headers", biliHeaders,
		"-i", s.opts.StreamURL,
		"-vn", // 丢弃视频
		"-acodec", "pcm_s16le",
		"-ar", fmt.Sprint(s.opts.SampleRate),
		"-ac", "1",
		"-f", "s16le",
		"pipe:1",
	}
}

// Start 启动 ffmpeg 进程。
func (s *Source) Start() error {
	cmd := exec.Command(s.opts.FFmpegPath, s.ffmpegArgs()...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 ffmpeg 输出管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败（请确认已安装并加入 PATH）: %w", err)
	}
	s.cmd = cmd
	s.out = stdout
	return nil
}

// Next 读取下一块 PCM（int16，小端序），返回采样点切片。
// 流结束或进程退出时返回 io.EOF。
func (s *Source) Next() ([]int16, error) {
	buf := make([]byte, s.opts.ChunkSamples*2)
	if _, err := io.ReadFull(s.out, buf); err != nil {
		return nil, err
	}
	samples := make([]int16, s.opts.ChunkSamples)
	for i := 0; i < s.opts.ChunkSamples; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
	return samples, nil
}

// Close 终止 ffmpeg 进程。
func (s *Source) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	if s.out != nil {
		_ = s.out.Close()
	}
	s.cmd = nil
	s.out = nil
}
