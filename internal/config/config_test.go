package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
	if cfg.SampleRate != 16000 || cfg.ChunkMS != 600 {
		t.Fatalf("采样参数异常: %d Hz / %d ms", cfg.SampleRate, cfg.ChunkMS)
	}
	if got := cfg.ChunkSamples(); got != 9600 {
		t.Fatalf("ChunkSamples() = %d，期望 9600（600ms @16kHz）", got)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "not-exist.yaml"))
	if err != nil {
		t.Fatalf("配置文件不存在时应返回默认配置，实际报错: %v", err)
	}
	if cfg.RoomID != Default().RoomID {
		t.Fatalf("RoomID = %d，期望默认值 %d", cfg.RoomID, Default().RoomID)
	}
}

func TestLoadOverridesAndFillsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
room_id: 123
ring_buffer_seconds: 120
danmaku:
  keywords: ["测试词", "TEST"]
  density_threshold: 8
keyword:
  stop_delay_seconds: 60
  t1: ["自定义"]
recorder:
  video_keep_minutes: 10
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.RoomID != 123 {
		t.Errorf("RoomID = %d，期望 123", cfg.RoomID)
	}
	if got := cfg.Danmaku.Keywords; len(got) != 2 || got[0] != "测试词" {
		t.Errorf("danmaku.keywords 未生效: %v", got)
	}
	if cfg.Danmaku.DensityThreshold != 8 {
		t.Errorf("danmaku.density_threshold = %d，期望 8", cfg.Danmaku.DensityThreshold)
	}
	if got := cfg.Keyword.T1; len(got) != 1 || got[0] != "自定义" {
		t.Errorf("keyword.t1 未生效: %v", got)
	}
	// 未在 YAML 中出现的字段应回落到默认值
	if cfg.Platform != "web" {
		t.Errorf("platform = %q，期望默认 web", cfg.Platform)
	}
	if cfg.SampleRate != 16000 || cfg.ChunkMS != 600 {
		t.Errorf("采样参数未填充默认值: %d / %d", cfg.SampleRate, cfg.ChunkMS)
	}
	if cfg.LiveCheckInterval != 60*time.Second {
		t.Errorf("live_check_interval = %v，期望 60s", cfg.LiveCheckInterval)
	}
	if cfg.Danmaku.WindowSeconds != 30 {
		t.Errorf("danmaku.window_seconds = %d，期望默认 30", cfg.Danmaku.WindowSeconds)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("覆盖后的配置应通过校验: %v", err)
	}
}

func TestLoadFillsVADThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// 只开启 VAD，不写阈值：阈值为 0 会把所有音频判为静音并跳过 ASR
	if err := os.WriteFile(path, []byte("vad:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.VAD.EnergyThreshold != Default().VAD.EnergyThreshold {
		t.Fatalf("energy_threshold = %v，期望默认 %v", cfg.VAD.EnergyThreshold, Default().VAD.EnergyThreshold)
	}
}

func TestWebDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.Web.Enabled {
		t.Fatal("默认应启用监控面板")
	}
	if cfg.Web.Listen != "127.0.0.1:8080" {
		t.Fatalf("默认监听地址 = %q，期望只监听回环", cfg.Web.Listen)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("web:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Web.Enabled {
		t.Fatal("显式关闭监控面板应生效")
	}
	if cfg.Web.Listen != Default().Web.Listen {
		t.Fatalf("未配置监听地址时应回落默认值，实际 %q", cfg.Web.Listen)
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("room_id: [1, 2\n"), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("非法 YAML 应返回错误")
	}
}

func TestValidate(t *testing.T) {
	// 每项在默认配置上制造一处非法值，错误信息需指明对应字段
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"房间号为空", func(c *Config) { c.RoomID = 0 }, "room_id"},
		{"拉流平台非法", func(c *Config) { c.Platform = "android" }, "platform"},
		{"采样率为零", func(c *Config) { c.SampleRate = 0 }, "sample_rate"},
		{"音频块为零", func(c *Config) { c.ChunkMS = 0 }, "chunk_ms"},
		{"回溯窗口为零", func(c *Config) { c.RingBufferSeconds = 0 }, "ring_buffer_seconds"},
		{"ASR 类型非法", func(c *Config) { c.ASR.Type = "whisper" }, "asr.type"},
		{"T2 阈值越界", func(c *Config) { c.Keyword.T2Threshold = 1.5 }, "t2_threshold"},
		{"合并格式非法", func(c *Config) { c.Recorder.MergeFormat = "avi" }, "merge_format"},
		{"分段时长为零", func(c *Config) { c.Recorder.VideoSegmentSeconds = 0 }, "video_segment_seconds"},
		{"缓冲保留不足", func(c *Config) {
			c.RingBufferSeconds = 90
			c.Recorder.VideoSegmentSeconds = 10
			c.Recorder.VideoKeepMinutes = 1
		}, "video_keep_minutes"},
		{"弹幕关键词为空", func(c *Config) { c.Danmaku.Keywords = []string{} }, "danmaku.keywords"},
		{"弹幕窗口为零", func(c *Config) { c.Danmaku.WindowSeconds = 0 }, "danmaku.window_seconds"},
		{"弹幕阈值零值", func(c *Config) { c.Danmaku.DensityThreshold = 0 }, "danmaku.density_threshold"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Default()
			c.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("期望校验失败，实际通过")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应包含 %q，实际: %v", c.want, err)
			}
		})
	}
}

func TestValidateKeepCoversPreRollAndSegment(t *testing.T) {
	cfg := Default()
	cfg.RingBufferSeconds = 600
	cfg.Recorder.VideoSegmentSeconds = 10
	cfg.Recorder.VideoKeepMinutes = 5
	err := cfg.Validate()
	if err == nil {
		t.Fatal("保留时长不足以覆盖回溯窗口时应校验失败")
	}
	if !strings.Contains(err.Error(), "11 分钟") {
		t.Fatalf("错误信息应给出建议的保留时长，实际: %v", err)
	}
}

// 关闭弹幕触发时，关键词等弹幕字段不参与校验。
func TestValidateSkipsDanmakuWhenDisabled(t *testing.T) {
	cfg := Default()
	cfg.Danmaku.Enabled = false
	cfg.Danmaku.Keywords = nil
	cfg.Danmaku.WindowSeconds = 0
	cfg.Danmaku.DensityThreshold = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("关闭弹幕时不应校验弹幕字段: %v", err)
	}
}
