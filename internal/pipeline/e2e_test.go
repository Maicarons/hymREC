package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maicarons/hymREC/internal/bili"
	"github.com/Maicarons/hymREC/internal/config"
)

// TestPipelineEndToEnd 验证"触发 → 回溯 → 合并 → 切片输出"全链路：
// ffmpeg 用 lavfi 合成直播流（HLS）并起本地 HTTP 服务，B 站 API 用测试服务器
// 模拟，ASR 用脚本化引擎注入关键词文本。全程不需要外部网络与真实直播间。
func TestPipelineEndToEnd(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("未安装 ffmpeg，跳过端到端测试")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("未安装 ffprobe，跳过端到端测试")
	}

	live := &atomic.Bool{}
	live.Store(true)
	streamURL := startSyntheticLive(t, ffmpeg)
	api := startMockBiliAPI(t, streamURL, live)

	cfg := config.Default()
	cfg.RoomID = 123
	cfg.Platform = "h5" // HLS
	cfg.LiveCheckInterval = time.Second
	cfg.StreamRetryDelay = time.Second
	cfg.ChunkMS = 300
	cfg.RingBufferSeconds = 6
	cfg.VAD.Enabled = false
	cfg.ASR.Type = "none"
	cfg.Keyword.StopDelaySeconds = 2
	cfg.Keyword.CooldownSeconds = 1
	cfg.Danmaku.Enabled = false
	cfg.Recorder.OutputDir = filepath.Join(t.TempDir(), "recordings")
	cfg.Recorder.VideoBufferDir = filepath.Join(t.TempDir(), "video_buffer")
	cfg.Recorder.VideoSegmentSeconds = 2
	cfg.Recorder.VideoKeepMinutes = 1
	cfg.Recorder.MergeFormat = "ts"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("测试配置非法: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	bc := bili.NewClient(nil)
	bc.SetBaseURL(api)
	p, err := NewWithClient(cfg, logger, bc)
	if err != nil {
		t.Fatalf("初始化流水线失败: %v", err)
	}
	// 用脚本化引擎替代 FunASR：第一块音频返回命中的关键词文本，之后返回静默
	p.asrEngine = &scriptedEngine{text: "星瞳开播啦", remaining: 1}

	var (
		evMu   sync.Mutex
		events []Event
	)
	p.SetOnEvent(func(ev Event) {
		evMu.Lock()
		events = append(events, ev)
		evMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	var clips []string
	waitFor(t, 90*time.Second, func() bool {
		clips = listClips(cfg.Recorder.OutputDir)
		return len(clips) > 0
	}, "流水线未在预期时间内产出切片")

	info, err := os.Stat(clips[0])
	if err != nil || info.Size() == 0 {
		t.Fatalf("切片文件无效: %v", err)
	}
	if err := ffprobeStreams(clips[0], "video", "audio"); err != nil {
		t.Fatalf("切片媒体校验失败: %v", err)
	}
	t.Logf("端到端切片: %s（%d 字节）", clips[0], info.Size())

	// 触发与切片产出都应通过事件流推送（监控前端依赖）
	evMu.Lock()
	var sawTrigger, sawClip bool
	for _, ev := range events {
		if ev.Type == EventTrigger && ev.Source == "asr" {
			sawTrigger = true
		}
		if ev.Type == EventClip && ev.File == clips[0] && ev.Size > 0 {
			sawClip = true
		}
	}
	evMu.Unlock()
	if !sawTrigger {
		t.Fatal("未收到 ASR 触发事件")
	}
	if !sawClip {
		t.Fatal("未收到切片产出事件")
	}

	// 下播：会话应结束（视频缓冲停止），主循环回到等待开播而不是空转
	live.Store(false)
	waitFor(t, 40*time.Second, func() bool {
		return !p.videoBuf.Running()
	}, "主播下播后视频缓冲应停止")
	select {
	case err := <-done:
		t.Fatalf("下播后 Run 应继续等待下次开播，实际退出: %v", err)
	default:
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("取消后 Run 应正常返回: %v", err)
	}
}

// scriptedEngine 按脚本返回识别文本，用于在没有 ASR 服务时驱动关键词触发。
type scriptedEngine struct {
	mu        sync.Mutex
	text      string
	remaining int
}

func (e *scriptedEngine) ProcessChunk([]int16) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.remaining <= 0 {
		return "", nil
	}
	e.remaining--
	return e.text, nil
}

func (e *scriptedEngine) Flush() (string, error) { return "", nil }
func (e *scriptedEngine) Close() error           { return nil }

// startSyntheticLive 用 ffmpeg 实时生成 HLS 直播流并通过本地 HTTP 服务提供，
// 返回 m3u8 地址。播放列表省略 ENDLIST，读取方会当作持续直播的流。
//
// 优先用 libx264，缺失时退回内置的 mpeg4 编码器，避免精简版 ffmpeg 直接失败。
func startSyntheticLive(t *testing.T, ffmpeg string) string {
	t.Helper()
	var lastErr string
	for _, encoder := range []string{"libx264", "mpeg4"} {
		dir := t.TempDir()
		// 先输入后编码参数：-preset 只能放在输出参数里（libx264 才有）
		args := []string{
			"-hide_banner", "-loglevel", "error",
			"-re", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10",
			"-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000",
			"-c:v", encoder, "-pix_fmt", "yuv420p", "-g", "10",
		}
		if encoder == "libx264" {
			args = append(args, "-preset", "ultrafast")
		}
		args = append(args,
			"-c:a", "aac", "-b:a", "64k",
			"-f", "hls",
			"-hls_time", "1",
			"-hls_list_size", "10",
			"-hls_flags", "delete_segments+omit_endlist",
			"-hls_segment_filename", filepath.Join(dir, "seg%03d.ts"),
			filepath.Join(dir, "live.m3u8"),
		)

		cmd := exec.Command(ffmpeg, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			lastErr = err.Error()
			continue
		}
		ready := waitForDuration(12*time.Second, func() bool { return countSegments(dir) >= 2 })
		if !ready {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			lastErr = "编码器 " + encoder + " 未在预期时间内产出分段: " + strings.TrimSpace(stderr.String())
			continue
		}

		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			if t.Failed() {
				t.Logf("合成流 ffmpeg 输出: %s", strings.TrimSpace(stderr.String()))
			}
		})
		srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
		t.Cleanup(srv.Close)
		return srv.URL + "/live.m3u8"
	}
	t.Fatalf("合成直播流未就绪: %s", lastErr)
	return ""
}

// countSegments 统计目录中已生成的 TS 分段数。
func countSegments(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ts") {
			n++
		}
	}
	return n
}

// startMockBiliAPI 模拟 B 站直播间接口：开播状态由 live 控制，流地址指向合成流。
func startMockBiliAPI(t *testing.T, streamURL string, live *atomic.Bool) string {
	t.Helper()
	status := func() int {
		if live.Load() {
			return 1
		}
		return 0
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"room_id":123,"live_status":%d,"title":"端到端测试"}}`, status())
	})
	mux.HandleFunc("/room/v1/Room/getRoomInfoOld", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"title":"端到端测试","liveStatus":%d}}`, status())
	})
	mux.HandleFunc("/room/v1/Room/playUrl", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"durl":[{"url":%q,"format":"m3u8"}]}}`, streamURL)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitFor 轮询等待条件成立，超时则判定测试失败。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	if !waitForDuration(timeout, cond) {
		t.Fatal(msg)
	}
}

// waitForDuration 轮询等待条件成立，返回是否在超时前成立。
func waitForDuration(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// listClips 返回输出目录中的切片文件。
func listClips(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".ts") || strings.HasSuffix(e.Name(), ".mp4") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// ffprobeStreams 校验媒体文件包含指定的流类型。
func ffprobeStreams(path string, kinds ...string) error {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		path,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffprobe 执行失败: %w（%s）", err, strings.TrimSpace(string(out)))
	}
	for _, kind := range kinds {
		if !strings.Contains(string(out), kind) {
			return fmt.Errorf("缺少 %s 流，实际: %s", kind, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
