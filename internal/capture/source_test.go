package capture

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestNewDefaults(t *testing.T) {
	s, err := New(Options{StreamURL: "http://example.com/live.flv"})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	if s.opts.FFmpegPath != "ffmpeg" {
		t.Errorf("FFmpegPath = %q，期望 ffmpeg", s.opts.FFmpegPath)
	}
	if s.opts.SampleRate != 16000 || s.opts.ChunkSamples != 9600 {
		t.Errorf("采样参数 = %d Hz / %d 点，期望 16000 / 9600（600ms）", s.opts.SampleRate, s.opts.ChunkSamples)
	}
}

func TestFFmpegArgs(t *testing.T) {
	const streamURL = "http://example.com/live.flv"
	s, _ := New(Options{StreamURL: streamURL, SampleRate: 16000, ChunkSamples: 9600})
	args := strings.Join(s.ffmpegArgs(), " ")

	for _, want := range []string{
		"-i " + streamURL,   // 输入流地址
		"-vn",               // 丢弃视频
		"-acodec pcm_s16le", // PCM 输出
		"-ar 16000",
		"-ac 1", // 单声道
		"-f s16le",
		"pipe:1",
		"Referer: https://live.bilibili.com/", // CDN 校验头
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ffmpeg 参数缺少 %q，实际: %s", want, args)
		}
	}
	if !strings.Contains(args, "-reconnect 1") {
		t.Errorf("应开启断流重连，实际: %s", args)
	}
}

func TestNextDecodesLittleEndianPCM(t *testing.T) {
	buf := &bytes.Buffer{}
	for _, v := range []int16{1, -2, 300, -400} {
		if err := binary.Write(buf, binary.LittleEndian, v); err != nil {
			t.Fatalf("构造 PCM 失败: %v", err)
		}
	}
	s := &Source{opts: Options{ChunkSamples: 4}, out: io.NopCloser(buf)}

	pcm, err := s.Next()
	if err != nil {
		t.Fatalf("Next 失败: %v", err)
	}
	want := []int16{1, -2, 300, -400}
	if len(pcm) != len(want) {
		t.Fatalf("采样点数 = %d，期望 %d", len(pcm), len(want))
	}
	for i := range want {
		if pcm[i] != want[i] {
			t.Fatalf("第 %d 个采样 = %d，期望 %d", i, pcm[i], want[i])
		}
	}
}

func TestNextEOF(t *testing.T) {
	s := &Source{opts: Options{ChunkSamples: 4}, out: io.NopCloser(bytes.NewReader([]byte{1, 2}))}
	if _, err := s.Next(); err == nil {
		t.Fatal("数据不足一个块时应返回错误（供上层重连）")
	}
}
