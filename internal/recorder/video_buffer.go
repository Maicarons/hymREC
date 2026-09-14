// Package recorder 实现视频素材录制：
//   - VideoBuffer：ffmpeg 持续分段录制直播流（TS 分段，自动清理），支持按时间回溯
//   - Controller：关键词触发 -> 停止 的状态机，触发时从缓冲提取前 N 秒片段合并输出
package recorder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// VideoBufferConfig 视频环形缓冲配置。
type VideoBufferConfig struct {
	// Dir 分段文件目录。
	Dir string
	// SegmentSeconds 每个分段时长（秒），默认 10。
	SegmentSeconds int
	// KeepDuration 分段保留时长，超出后被 Cleanup 删除。
	KeepDuration time.Duration
	// FFmpegPath ffmpeg 可执行文件路径。
	FFmpegPath string
}

// VideoBuffer 以 TS 分段持续录制直播流，保留最近 N 分钟的素材，
// 供关键词触发后回溯提取"断章取义"所需的前 90 秒上下文。
type VideoBuffer struct {
	cfg VideoBufferConfig

	mu      sync.Mutex
	cmd     *exec.Cmd
	running bool
}

// NewVideoBuffer 创建视频缓冲。
func NewVideoBuffer(cfg VideoBufferConfig) (*VideoBuffer, error) {
	if cfg.SegmentSeconds <= 0 {
		cfg.SegmentSeconds = 10
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.Dir == "" {
		cfg.Dir = "./video_buffer"
	}
	if cfg.KeepDuration <= 0 {
		cfg.KeepDuration = 5 * time.Minute
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建视频缓冲目录失败: %w", err)
	}
	return &VideoBuffer{cfg: cfg}, nil
}

// segmentArgs 构造 ffmpeg 分段录制命令。
// 文件名形如 seg_20260914_123000.ts（本地时间，秒级），
// 用于按时间回溯。注意避免使用 %s（Windows 上不可用）。
func (v *VideoBuffer) segmentArgs(streamURL string) []string {
	pattern := filepath.Join(v.cfg.Dir, "seg_%Y%m%d_%H%M%S.ts")
	return []string{
		"-hide_banner",
		"-loglevel", "error",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_delay_max", "10",
		"-i", streamURL,
		"-c", "copy", // 不转码
		"-f", "segment",
		"-segment_time", fmt.Sprint(v.cfg.SegmentSeconds),
		"-segment_format", "mpegts",
		"-reset_timestamps", "1",
		"-strftime", "1",
		pattern,
	}
}

// Start 启动持续分段录制。
func (v *VideoBuffer) Start(streamURL string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.running {
		return fmt.Errorf("视频缓冲已在运行")
	}
	cmd := exec.Command(v.cfg.FFmpegPath, v.segmentArgs(streamURL)...)
	// 分段录制的日志保留在 stderr，出错时便于排查
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 分段录制失败: %w", err)
	}
	v.cmd = cmd
	v.running = true
	return nil
}

// Running 返回缓冲是否在录制。
func (v *VideoBuffer) Running() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.running
}

// segmentTime 从文件名解析分段起始时间，解析失败返回 zero Time。
// 文件名格式：seg_20060102_150405.ts（本地时区）。
func segmentTime(name string) time.Time {
	base := filepath.Base(name)
	trimmed := strings.TrimPrefix(strings.TrimSuffix(base, ".ts"), "seg_")
	// 兼容旧格式 seg_20060102_150405_1790000000.ts：取前 15 位
	if len(trimmed) > 15 {
		trimmed = trimmed[:15]
	}
	t, err := time.ParseInLocation("20060102_150405", trimmed, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// SegmentsInRange 返回起始时间落在 [start, end] 区间（含 start 前一整段）
// 的分段文件绝对路径，按时间升序。
func (v *VideoBuffer) SegmentsInRange(start, end time.Time) []string {
	startSec := start.Unix()
	endSec := end.Unix()
	entries, err := os.ReadDir(v.cfg.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "seg_") || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		sec := segmentTime(e.Name()).Unix()
		if sec == 0 {
			continue
		}
		// 覆盖 start 之前最近的一个分段，保证回溯不丢开头
		if sec+int64(v.cfg.SegmentSeconds) < startSec {
			continue
		}
		if sec > endSec {
			continue
		}
		out = append(out, filepath.Join(v.cfg.Dir, e.Name()))
	}
	sort.Slice(out, func(i, j int) bool {
		return segmentTime(out[i]).Before(segmentTime(out[j]))
	})
	return out
}

// Cleanup 删除超出保留时长的旧分段。
func (v *VideoBuffer) Cleanup() {
	entries, err := os.ReadDir(v.cfg.Dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-v.cfg.KeepDuration)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "seg_") {
			continue
		}
		sec := segmentTime(e.Name())
		if !sec.IsZero() && sec.Before(cutoff) {
			_ = os.Remove(filepath.Join(v.cfg.Dir, e.Name()))
		}
	}
}

// Stop 停止分段录制并回收进程。
func (v *VideoBuffer) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.running {
		return
	}
	v.running = false
	if v.cmd != nil && v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
		_, _ = v.cmd.Process.Wait()
	}
	v.cmd = nil
}
