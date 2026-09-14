package recorder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// State 录制状态。
type State int

const (
	StateIdle State = iota
	StateRecording
)

func (s State) String() string {
	if s == StateRecording {
		return "recording"
	}
	return "idle"
}

// ControllerConfig 录制控制器配置。
type ControllerConfig struct {
	// OutputDir 最终切片输出目录。
	OutputDir string
	// FilenamePrefix 输出文件名前缀。
	FilenamePrefix string
	// PreRollSeconds 触发前回溯时长（秒），恨也迷人风格为 90。
	PreRollSeconds int
	// MergeFormat 合并输出格式：ts 或 mp4。
	MergeFormat string
	// FFmpegPath ffmpeg 可执行文件路径。
	FFmpegPath string
}

// Session 一次触发录制的会话信息。
type Session struct {
	TriggerAt time.Time
	Source    string // 触发来源：asr / danmaku
	StopAt    time.Time
	Output    string // 最终输出文件路径
}

// Controller 录制控制器（状态机）：
//   - OnTrigger：进入录制态，记录触发时刻
//   - OnStop：退出录制态，从视频缓冲提取 [触发前N秒, 当前] 的分段并合并输出
type Controller struct {
	cfg    ControllerConfig
	buffer *VideoBuffer

	mu      sync.Mutex
	state   State
	session *Session
}

// NewController 创建录制控制器。
func NewController(cfg ControllerConfig, buffer *VideoBuffer) (*Controller, error) {
	if cfg.PreRollSeconds <= 0 {
		cfg.PreRollSeconds = 90
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "./recordings"
	}
	if cfg.FilenamePrefix == "" {
		cfg.FilenamePrefix = "hym_clip"
	}
	if cfg.MergeFormat != "mp4" {
		cfg.MergeFormat = "ts"
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建输出目录失败: %w", err)
	}
	return &Controller{cfg: cfg, buffer: buffer, state: StateIdle}, nil
}

// State 返回当前状态。
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// OnTrigger 关键词命中（或弹幕密度达标）时调用。
// source 用于日志标识触发来源。
func (c *Controller) OnTrigger(at time.Time, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateRecording {
		return // 已在录制中
	}
	c.state = StateRecording
	c.session = &Session{TriggerAt: at, Source: source}
}

// OnStop 停止录制并产出切片。返回本次会话（未在录制时返回 nil）。
func (c *Controller) OnStop() *Session {
	c.mu.Lock()
	sess := c.session
	if c.state != StateRecording || sess == nil {
		c.state = StateIdle
		c.session = nil
		c.mu.Unlock()
		return nil
	}
	c.state = StateIdle
	c.session = nil
	c.mu.Unlock()

	if c.buffer == nil {
		return sess
	}
	sess.StopAt = time.Now()
	start := sess.TriggerAt.Add(-time.Duration(c.cfg.PreRollSeconds) * time.Second)
	segments := c.buffer.SegmentsInRange(start, sess.StopAt)
	if len(segments) == 0 {
		return sess
	}
	output, err := mergeSegments(c.cfg, segments, sess)
	if err != nil {
		return sess
	}
	sess.Output = output
	return sess
}

// Close 停止录制并回收缓冲。
func (c *Controller) Close() {
	c.mu.Lock()
	wasRecording := c.state == StateRecording
	c.state = StateIdle
	c.session = nil
	c.mu.Unlock()
	if wasRecording {
		c.OnStop()
	}
}

// mergeSegments 将分段列表合并为最终切片文件（ts 或 mp4）。
func mergeSegments(cfg ControllerConfig, segments []string, sess *Session) (string, error) {
	stamp := sess.TriggerAt.Format("20060102_150405")
	base := filepath.Join(cfg.OutputDir,
		fmt.Sprintf("%s_%s_%s", cfg.FilenamePrefix, sess.Source, stamp))
	listFile := base + ".list"

	var b strings.Builder
	for _, seg := range segments {
		abs, err := filepath.Abs(seg)
		if err != nil {
			abs = seg
		}
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
	}
	if err := os.WriteFile(listFile, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("写 concat 列表失败: %w", err)
	}
	defer os.Remove(listFile)

	tsOut := base + ".ts"
	cmd := exec.Command(cfg.FFmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0",
		"-i", listFile,
		"-c", "copy",
		"-y", tsOut,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg concat 失败: %v（%s）", err, strings.TrimSpace(string(out)))
	}

	if cfg.MergeFormat == "mp4" {
		mp4Out := base + ".mp4"
		cmd := exec.Command(cfg.FFmpegPath,
			"-hide_banner", "-loglevel", "error",
			"-i", tsOut,
			"-c", "copy",
			"-movflags", "+faststart",
			"-y", mp4Out,
		)
		if _, err := cmd.CombinedOutput(); err != nil {
			// 转封装失败时保留 ts 输出
			return tsOut, nil
		}
		_ = os.Remove(tsOut)
		return mp4Out, nil
	}
	return tsOut, nil
}
