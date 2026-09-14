//go:build reallive

// 真实直播端到端测试：连接真实 B 站直播间、真实弹幕服务器与真实 FunASR 服务，
// 验证"真实内容 -> 识别/弹幕触发 -> 回溯 -> 切片输出"全链路。
//
// 运行要求（缺一不可，缺失时测试明确报错）：
//   - 目标直播间正在直播（可用 HYMREC_TEST_ROOM 指定，默认 545068）
//   - 本地 FunASR 流式服务可用（HYMREC_ASR_ENDPOINT，默认 ws://127.0.0.1:10095）
//   - ffmpeg 可用
//
// 运行：go test -tags reallive -run TestRealLiveEndToEnd ./internal/pipeline/
package pipeline

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/henyemiren/hymrec/internal/bili"
	"github.com/henyemiren/hymrec/internal/config"
)

func TestRealLiveEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatalf("需要 ffmpeg 才能录制，请先安装并加入 PATH")
	}

	roomID := envInt("HYMREC_TEST_ROOM", 545068)
	asrEndpoint := envStr("HYMREC_ASR_ENDPOINT", "ws://127.0.0.1:10095")

	// 1. 校验直播间开播（真实 API）
	bc := bili.NewClient(nil)
	st, err := bc.GetRoomStatus(roomID)
	if err != nil {
		t.Fatalf("查询直播间状态失败: %v", err)
	}
	if st.LiveStatus != 1 {
		t.Fatalf("房间 %d 当前未开播（live_status=%d），无法进行真实直播测试", roomID, st.LiveStatus)
	}
	t.Logf("直播间: %d title=%q", roomID, st.Title)

	// 2. 快速节奏配置：真实直播间高频词触发
	cfg := config.Default()
	cfg.RoomID = roomID
	cfg.LiveCheckInterval = 10 * time.Second
	cfg.ASR.Type = "funasr-ws"
	cfg.ASR.Endpoint = asrEndpoint
	cfg.VAD.Enabled = false // 直播音量大，关闭静音跳过，让 ASR 全程识别
	// ASR 关键词：直播中高频口语（识别命中即触发）
	cfg.Keyword.StopDelaySeconds = 20
	cfg.Keyword.CooldownSeconds = 10
	cfg.Keyword.T1 = []string{"谢谢", "哈哈", "兄弟们", "好的", "欢迎", "卧槽", "我操", "真的", "什么", "大家"}
	cfg.Keyword.T2 = nil
	cfg.Keyword.T3 = nil
	cfg.Keyword.T4 = nil
	// 弹幕：真实弹幕流密度触发（游戏直播间弹幕量大）
	cfg.Danmaku.Enabled = true
	cfg.Danmaku.Keywords = []string{"哈哈", "谢谢", "666", "好", "牛", "笑死", "主播", "真的", "卧槽", "下把"}
	cfg.Danmaku.DensityThreshold = 6
	cfg.Danmaku.WindowSeconds = 30
	cfg.Danmaku.ConfirmDelay = 3 * time.Second
	cfg.Recorder.OutputDir = filepath.Join(t.TempDir(), "recordings")
	cfg.Recorder.VideoBufferDir = filepath.Join(t.TempDir(), "video_buffer")
	cfg.Recorder.VideoKeepMinutes = 3
	cfg.Recorder.VideoSegmentSeconds = 10
	cfg.Recorder.MergeFormat = "ts"

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	p, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("初始化流水线失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// 3. 等待切片产出（最长 3.5 分钟，给 ASR/弹幕触发留足时间）
	deadline := time.Now().Add(210 * time.Second)
	var clips []string
	for time.Now().Before(deadline) {
		clips = globClips(t, cfg.Recorder.OutputDir)
		if len(clips) > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	cancel()
	if err := <-done; err != nil && ctx.Err() == nil {
		t.Fatalf("流水线退出异常: %v", err)
	}
	if len(clips) == 0 {
		clips = globClips(t, cfg.Recorder.OutputDir)
	}

	if len(clips) == 0 {
		t.Fatal("未产出任何切片：真实直播 3.5 分钟内既无 ASR 关键词命中，弹幕密度也未触发")
	}
	info, err := os.Stat(clips[0])
	if err != nil || info.Size() == 0 {
		t.Fatalf("切片文件无效: %v (size=%d)", err, info.Size())
	}

	// 4. 用 ffprobe 校验切片媒体有效性（真实视频+音频流）
	if err := ffprobeCheck(t, clips[0]); err != nil {
		t.Fatalf("切片媒体校验失败: %v", err)
	}
	t.Logf("真实直播端到端测试通过：切片 %s（%d 字节），媒体流有效", clips[0], info.Size())
}

// ffprobeCheck 校验媒体文件包含 video 与 audio 流。
func ffprobeCheck(t *testing.T, path string) error {
	t.Helper()
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		path,
	).CombinedOutput()
	if err != nil {
		return err
	}
	s := string(out)
	if !strings.Contains(s, "video") || !strings.Contains(s, "audio") {
		return os.ErrInvalid
	}
	return nil
}

// globClips 返回输出目录中的切片文件。
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

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
