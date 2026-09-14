// Package config 定义恨也迷人风格录制系统的配置结构，
// 支持 YAML 文件加载与命令行覆盖（见 cmd/hymrec/main.go）。
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Default 返回一份带默认值的配置，默认值对齐 plan.md 第八节参数。
func Default() *Config {
	return &Config{
		RoomID:            21452505,
		LiveCheckInterval: 60 * time.Second,
		Quality:           10000,
		Platform:          "web", // web -> flv 直写；h5 -> m3u8（HLS）
		StreamRetryDelay:  5 * time.Second,

		SampleRate:        16000,
		ChunkMS:           600,
		RingBufferSeconds: 90,

		ASR: ASRConfig{
			Type:      "funasr-ws", // none | funasr-ws
			Endpoint:  "ws://127.0.0.1:10095",
			Mode:      "2pass",
			ChunkSize: []int{0, 10, 5},
			WavName:   "hymrec_live",
			Hotwords:  defaultHotwords(),
		},

		VAD: VADConfig{
			Enabled:         true,
			EnergyThreshold: 500.0,
		},

		Keyword: KeywordConfig{
			StopDelaySeconds: 120,
			CooldownSeconds:  5,
			T2Threshold:      0.75,
			T3CooccurWindow:  30.0,
			T1:               []string{"星瞳", "XingTong", "XT"},
			T2:               []string{"小星星", "瞳子", "瞳宝", "星瞳酱"},
			T3:               []string{"腾讯大小姐", "CDD", "虚无", "云之彼端", "深月的风吟鸟唱", "邻家女孩", "瞳工"},
			T4:               []string{"不是星瞳", "星瞳的黑称"},
		},

		Danmaku: DanmakuConfig{
			Enabled:          true,
			Keywords:         []string{"星瞳", "瞳", "XT", "小星星"},
			DensityThreshold: 5,
			WindowSeconds:    30,
			ConfirmDelay:     2 * time.Second,
		},

		Recorder: RecorderConfig{
			OutputDir:           "./recordings",
			VideoBufferDir:      "./video_buffer",
			VideoKeepMinutes:    5,
			VideoSegmentSeconds: 10,
			MergeFormat:         "ts", // ts 直接拼接；mp4 需要 ffmpeg 转封装
			FilenamePrefix:      "hym_clip",
			FFmpegPath:          "ffmpeg",
		},

		LogLevel: "info",
	}
}

// Config 顶层配置。
type Config struct {
	// RoomID 目标直播间号（支持短号，内部会自动解析为真实房间号）。
	RoomID int `yaml:"room_id"`
	// LiveCheckInterval 开播检测轮询间隔。
	LiveCheckInterval time.Duration `yaml:"live_check_interval"`
	// Quality 画质，10000 为原画。
	Quality int `yaml:"quality"`
	// Platform 拉流平台：web 返回 FLV（可直写），h5 返回 m3u8（HLS）。
	Platform string `yaml:"platform"`
	// StreamRetryDelay 流中断后的重连等待时间。
	StreamRetryDelay time.Duration `yaml:"stream_retry_delay"`

	// SampleRate ASR 采样率（Hz），默认 16000。
	SampleRate int `yaml:"sample_rate"`
	// ChunkMS 每个音频块时长（毫秒），默认 600。
	ChunkMS int `yaml:"chunk_ms"`
	// RingBufferSeconds 音频环形缓冲回溯时长（秒），恨也迷人风格为 90 秒。
	RingBufferSeconds int `yaml:"ring_buffer_seconds"`

	ASR      ASRConfig      `yaml:"asr"`
	VAD      VADConfig      `yaml:"vad"`
	Keyword  KeywordConfig  `yaml:"keyword"`
	Danmaku  DanmakuConfig  `yaml:"danmaku"`
	Recorder RecorderConfig `yaml:"recorder"`

	// LogLevel 日志级别：debug | info | warn | error。
	LogLevel string `yaml:"log_level"`
}

// ASRConfig 语音识别引擎配置。
type ASRConfig struct {
	// Type 识别引擎类型：none（关闭，仅弹幕触发）或 funasr-ws（FunASR 流式，默认）。
	Type string `yaml:"type"`
	// Endpoint FunASR WebSocket 服务地址，如 ws://127.0.0.1:10095。
	Endpoint string `yaml:"endpoint"`
	// Mode FunASR 模式：2pass（推荐）。
	Mode string `yaml:"mode"`
	// ChunkSize FunASR chunk 大小 [encoder, decoder, offset]。
	ChunkSize []int `yaml:"chunk_size"`
	// WavName 传给 FunASR 的音频名，用于日志区分。
	WavName string `yaml:"wav_name"`
	// Hotwords 热词表，键为热词，值为权重（T1/T2 建议 5，T3 建议 3-4）。
	Hotwords map[string]int `yaml:"hotwords"`
}

// VADConfig 静音检测配置（基于能量阈值）。
type VADConfig struct {
	Enabled bool `yaml:"enabled"`
	// EnergyThreshold RMS 能量阈值，低于该值视为静音并跳过 ASR。
	EnergyThreshold float64 `yaml:"energy_threshold"`
}

// KeywordConfig 层级关键词配置（T1 精确 / T2 模糊 / T3 联合 / T4 否定）。
type KeywordConfig struct {
	// StopDelaySeconds 停止延迟：关键词停止出现 N 秒后结束录制。
	StopDelaySeconds int `yaml:"stop_delay_seconds"`
	// CooldownSeconds 冷却时间：同一会话内两次触发的最小间隔。
	CooldownSeconds int `yaml:"cooldown_seconds"`
	// T2Threshold T2 模糊匹配阈值（0-1），越高越严格。
	T2Threshold float64 `yaml:"t2_threshold"`
	// T3CooccurWindow T3 梗词与 T1/T2 的联合触发时间窗口（秒）。
	T3CooccurWindow float64 `yaml:"t3_cooccur_window"`

	T1 []string `yaml:"t1"`
	T2 []string `yaml:"t2"`
	T3 []string `yaml:"t3"`
	T4 []string `yaml:"t4"`
}

// DanmakuConfig 弹幕辅助触发配置。
type DanmakuConfig struct {
	Enabled bool `yaml:"enabled"`
	// Keywords 参与密度统计的弹幕关键词。
	Keywords []string `yaml:"keywords"`
	// DensityThreshold 窗口内含关键词弹幕条数达到该值即触发。
	DensityThreshold int `yaml:"density_threshold"`
	// WindowSeconds 密度统计滑动窗口（秒）。
	WindowSeconds int `yaml:"window_seconds"`
	// ConfirmDelay 弹幕触发后的确认延迟，用于降低误触发。
	ConfirmDelay time.Duration `yaml:"confirm_delay"`
}

// RecorderConfig 录制输出配置。
type RecorderConfig struct {
	// OutputDir 最终切片输出目录。
	OutputDir string `yaml:"output_dir"`
	// VideoBufferDir 视频环形缓冲（TS 分段）目录。
	VideoBufferDir string `yaml:"video_buffer_dir"`
	// VideoKeepMinutes 视频缓冲保留时长（分钟）。
	VideoKeepMinutes int `yaml:"video_keep_minutes"`
	// VideoSegmentSeconds 每个 TS 分段时长（秒）。
	VideoSegmentSeconds int `yaml:"video_segment_seconds"`
	// MergeFormat 片段合并输出格式：ts（直接 concat）或 mp4（ffmpeg 转封装）。
	MergeFormat string `yaml:"merge_format"`
	// FilenamePrefix 输出文件名前缀。
	FilenamePrefix string `yaml:"filename_prefix"`
	// FFmpegPath ffmpeg 可执行文件路径。
	FFmpegPath string `yaml:"ffmpeg_path"`
}

// Load 从文件读取配置；文件不存在时返回默认配置。
// 相对路径会先按工作目录解析，再按可执行文件所在目录解析。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	cfg.fillDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// fillDefaults 用默认值补齐未设置的字段。
func (c *Config) fillDefaults() {
	d := Default()
	if c.SampleRate == 0 {
		c.SampleRate = d.SampleRate
	}
	if c.ChunkMS == 0 {
		c.ChunkMS = d.ChunkMS
	}
	if c.RingBufferSeconds == 0 {
		c.RingBufferSeconds = d.RingBufferSeconds
	}
	if c.LiveCheckInterval == 0 {
		c.LiveCheckInterval = d.LiveCheckInterval
	}
	if c.StreamRetryDelay == 0 {
		c.StreamRetryDelay = d.StreamRetryDelay
	}
	if c.Quality == 0 {
		c.Quality = d.Quality
	}
	if c.Platform == "" {
		c.Platform = d.Platform
	}
	if c.ASR.Type == "" {
		c.ASR.Type = d.ASR.Type
	}
	if c.ASR.Endpoint == "" {
		c.ASR.Endpoint = d.ASR.Endpoint
	}
	if c.ASR.Mode == "" {
		c.ASR.Mode = d.ASR.Mode
	}
	if c.ASR.WavName == "" {
		c.ASR.WavName = d.ASR.WavName
	}
	if len(c.ASR.ChunkSize) != 3 {
		c.ASR.ChunkSize = d.ASR.ChunkSize
	}
	if c.Keyword.StopDelaySeconds == 0 {
		c.Keyword.StopDelaySeconds = d.Keyword.StopDelaySeconds
	}
	if c.Keyword.CooldownSeconds == 0 {
		c.Keyword.CooldownSeconds = d.Keyword.CooldownSeconds
	}
	if c.Keyword.T2Threshold == 0 {
		c.Keyword.T2Threshold = d.Keyword.T2Threshold
	}
	if c.Keyword.T3CooccurWindow == 0 {
		c.Keyword.T3CooccurWindow = d.Keyword.T3CooccurWindow
	}
	if c.Keyword.T1 == nil {
		c.Keyword.T1 = d.Keyword.T1
	}
	if c.Keyword.T2 == nil {
		c.Keyword.T2 = d.Keyword.T2
	}
	if c.Keyword.T3 == nil {
		c.Keyword.T3 = d.Keyword.T3
	}
	if c.Keyword.T4 == nil {
		c.Keyword.T4 = d.Keyword.T4
	}
	if c.Danmaku.DensityThreshold == 0 {
		c.Danmaku.DensityThreshold = d.Danmaku.DensityThreshold
	}
	if c.Danmaku.WindowSeconds == 0 {
		c.Danmaku.WindowSeconds = d.Danmaku.WindowSeconds
	}
	if c.Danmaku.ConfirmDelay == 0 {
		c.Danmaku.ConfirmDelay = d.Danmaku.ConfirmDelay
	}
	if c.Danmaku.Keywords == nil {
		c.Danmaku.Keywords = d.Danmaku.Keywords
	}
	if c.Recorder.OutputDir == "" {
		c.Recorder.OutputDir = d.Recorder.OutputDir
	}
	if c.Recorder.VideoBufferDir == "" {
		c.Recorder.VideoBufferDir = d.Recorder.VideoBufferDir
	}
	if c.Recorder.VideoKeepMinutes == 0 {
		c.Recorder.VideoKeepMinutes = d.Recorder.VideoKeepMinutes
	}
	if c.Recorder.VideoSegmentSeconds == 0 {
		c.Recorder.VideoSegmentSeconds = d.Recorder.VideoSegmentSeconds
	}
	if c.Recorder.MergeFormat == "" {
		c.Recorder.MergeFormat = d.Recorder.MergeFormat
	}
	if c.Recorder.FilenamePrefix == "" {
		c.Recorder.FilenamePrefix = d.Recorder.FilenamePrefix
	}
	if c.Recorder.FFmpegPath == "" {
		c.Recorder.FFmpegPath = d.Recorder.FFmpegPath
	}
	if c.LogLevel == "" {
		c.LogLevel = d.LogLevel
	}
	if c.ASR.Hotwords == nil {
		c.ASR.Hotwords = d.ASR.Hotwords
	}
}

// Validate 校验配置合法性。
func (c *Config) Validate() error {
	if c.RoomID <= 0 {
		return fmt.Errorf("room_id 必须为正整数")
	}
	if c.Platform != "web" && c.Platform != "h5" {
		return fmt.Errorf("platform 仅支持 web(flv) 或 h5(m3u8)，当前: %q", c.Platform)
	}
	switch c.ASR.Type {
	case "none", "funasr-ws":
	default:
		return fmt.Errorf("asr.type 仅支持 none|funasr-ws，当前: %q", c.ASR.Type)
	}
	if c.Keyword.T2Threshold <= 0 || c.Keyword.T2Threshold > 1 {
		return fmt.Errorf("keyword.t2_threshold 必须在 (0,1] 区间")
	}
	if c.Recorder.MergeFormat != "ts" && c.Recorder.MergeFormat != "mp4" {
		return fmt.Errorf("recorder.merge_format 仅支持 ts|mp4，当前: %q", c.Recorder.MergeFormat)
	}
	return nil
}

// ChunkSamples 返回每个音频块的采样点数。
func (c *Config) ChunkSamples() int {
	return c.SampleRate * c.ChunkMS / 1000
}

func defaultHotwords() map[string]int {
	return map[string]int{
		"星瞳": 5, "小星星": 5,
		"瞳子": 4, "瞳宝": 4, "星瞳酱": 4,
		"腾讯大小姐": 3, "CDD": 3, "虚无": 3,
		"云之彼端": 3, "深月的风吟鸟唱": 3,
		"邻家女孩": 3, "瞳工": 3,
	}
}
