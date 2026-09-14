package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henyemiren/hymrec/internal/bili"
	"github.com/henyemiren/hymrec/internal/config"
)

// TestPipelineEndToEnd 端到端集成测试：
// mock B 站 API + ffmpeg 实时生成的测试直播流 + mock ASR 关键词注入，
// 验证"关键词触发 -> 分段回溯 -> 合并切片输出"全链路。
func TestPipelineEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("未检测到 ffmpeg，跳过端到端集成测试")
	}

	// 1. 起一个实时测试流（ffmpeg -re 生成 FLV）
	streamURL, stopStream := startTestStream(t)
	defer stopStream()

	// 2. mock B 站 API：开播 + 返回测试流地址
	apiSrv := newPipelineMockAPI(t, streamURL)
	defer apiSrv.Close()

	// 3. 快速节奏配置
	cfg := config.Default()
	cfg.RoomID = 21452505
	cfg.LiveCheckInterval = 100 * time.Millisecond
	cfg.StreamRetryDelay = 2 * time.Second
	cfg.RingBufferSeconds = 6        // pre_roll 6 秒
	cfg.Keyword.StopDelaySeconds = 5 // 5 秒无关键词停止
	cfg.Keyword.CooldownSeconds = 2
	cfg.Danmaku.Enabled = false
	cfg.VAD.Enabled = false
	cfg.ASR.Type = "mock"
	cfg.ASR.MockTexts = []string{"星瞳今天状态很好", "随便聊点别的"}
	cfg.ASR.MockInterval = 3 * time.Second
	cfg.Recorder.OutputDir = filepath.Join(t.TempDir(), "recordings")
	cfg.Recorder.VideoBufferDir = filepath.Join(t.TempDir(), "video_buffer")
	cfg.Recorder.VideoKeepMinutes = 1
	cfg.Recorder.VideoSegmentSeconds = 2
	cfg.Recorder.MergeFormat = "ts"

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	bc := bili.NewClient(nil)
	bc.SetBaseURL(apiSrv.URL)

	p, err := NewWithClient(cfg, logger, bc)
	if err != nil {
		t.Fatalf("NewWithClient 失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// 4. 等待切片产出（轮询输出目录）
	deadline := time.Now().Add(25 * time.Second)
	var clips []string
	for time.Now().Before(deadline) {
		clips = globClips(t, cfg.Recorder.OutputDir)
		if len(clips) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && ctx.Err() == nil {
		t.Fatalf("流水线退出异常: %v", err)
	}
	// 停止收尾时（ctx 取消分支）也会产出切片，收尾完成后再次检查
	if len(clips) == 0 {
		clips = globClips(t, cfg.Recorder.OutputDir)
	}

	// 诊断输出
	if vbEntries, err := os.ReadDir(cfg.Recorder.VideoBufferDir); err == nil {
		t.Logf("video_buffer 分段数: %d", len(vbEntries))
		for _, e := range vbEntries {
			t.Logf("  seg: %s", e.Name())
		}
	}
	t.Logf("recordings 切片数: %d", len(clips))

	if len(clips) == 0 {
		t.Fatal("未产出任何切片文件（触发-录制链路未生效）")
	}
	info, err := os.Stat(clips[0])
	if err != nil {
		t.Fatalf("无法访问切片文件: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("切片文件为空: %s", clips[0])
	}
	t.Logf("端到端测试通过，切片输出: %s（%d 字节）", clips[0], info.Size())
}

func globClips(t *testing.T, dir string) []string {
	t.Helper()
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

// newPipelineMockAPI 返回 mock B 站 API（开播 + 流地址）。
func newPipelineMockAPI(t *testing.T, streamURL string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{"room_id": 21452505, "short_id": 21452505},
		})
	})
	mux.HandleFunc("/room/v1/Room/getRoomInfoOld", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{"roomid": 21452505, "liveStatus": 1, "title": "集成测试"},
		})
	})
	mux.HandleFunc("/room/v1/Room/playUrl", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{
			"code": 0, "message": "ok",
			"data": map[string]any{"durl": []map[string]any{{
				"url": streamURL, "format": "flv", "qn": 10000,
			}}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// startTestStream 用 ffmpeg -re 实时生成一段 FLV 测试流，通过 HTTP 对外提供。
// 返回流 URL 与停止函数。ffmpeg 不存在时调用 t.Skip。
func startTestStream(t *testing.T) (url string, stop func()) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("未检测到 ffmpeg，无法生成测试流")
	}

	// ffmpeg -re 按实时速率输出：320x240 测试画面 + 440Hz 正弦音 -> FLV
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-re",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-c:a", "aac", "-b:a", "64k",
		"-f", "flv", "pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("创建 ffmpeg 输出管道失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 ffmpeg 测试流失败: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	var mu sync.Mutex
	subs := make(map[chan []byte]struct{})
	closed := make(chan struct{})

	// 环形缓存：保留最近 1MB 数据，新连接建立时重放开头（FLV 头），
	// 保证每个 ffmpeg 拉流端都能从头解析
	var ringMu sync.Mutex
	ring := make([]byte, 0, 1<<20)

	// 转发 goroutine：stdout -> 环形缓存 + 所有订阅者 channel（阻塞写，不丢包）
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				ringMu.Lock()
				ring = append(ring, data...)
				if len(ring) > 1<<20 {
					ring = append([]byte(nil), ring[len(ring)-(1<<20):]...)
				}
				ringMu.Unlock()
				mu.Lock()
				targets := make([]chan []byte, 0, len(subs))
				for ch := range subs {
					targets = append(targets, ch)
				}
				mu.Unlock()
				for _, ch := range targets {
					select {
					case ch <- data:
					case <-closed:
						return
					}
				}
			}
			if err != nil {
				close(closed)
				return
			}
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-flv")
		w.WriteHeader(http.StatusOK)
		// 先重放环形缓存（含 FLV 头），保证新连接可解析
		ringMu.Lock()
		replay := append([]byte(nil), ring...)
		ringMu.Unlock()
		if len(replay) > 0 {
			if _, err := w.Write(replay); err != nil {
				return
			}
			if flusher, _ := w.(http.Flusher); flusher != nil {
				flusher.Flush()
			}
		}
		ch := make(chan []byte, 512)
		mu.Lock()
		subs[ch] = struct{}{}
		mu.Unlock()
		defer func() {
			mu.Lock()
			delete(subs, ch)
			mu.Unlock()
		}()
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case data, ok := <-ch:
				if !ok {
					return
				}
				if _, err := w.Write(data); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			case <-closed:
				return
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() {}
}
