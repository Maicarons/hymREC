package pipeline

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/recorder"
)

func testPipeline(t *testing.T) *Pipeline {
	t.Helper()
	cfg := config.Default()
	cfg.ASR.Type = "none"
	cfg.Danmaku.Enabled = false
	cfg.ASR.Hotwords = nil
	cfg.Recorder.OutputDir = filepath.Join(t.TempDir(), "recordings")
	cfg.Recorder.VideoBufferDir = filepath.Join(t.TempDir(), "video_buffer")
	p, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("创建流水线失败: %v", err)
	}
	return p
}

func TestStatusDefaults(t *testing.T) {
	p := testPipeline(t)
	st := p.Status()

	if st.Recording || st.Live {
		t.Fatalf("初始状态不应在录制或直播中: %+v", st)
	}
	if st.ASRType != "none" || st.DanmakuEnabled {
		t.Fatalf("引擎/弹幕状态异常: %+v", st)
	}
	if st.Buffer.Segments != 0 || st.Buffer.Stalled != 0 {
		t.Fatalf("空缓冲快照异常: %+v", st.Buffer)
	}
	if st.StopDelay != 120*time.Second {
		t.Fatalf("停止延迟 = %v，期望默认 120s", st.StopDelay)
	}
	if st.DanmakuThreshold != 5 || st.DanmakuWindow != 30*time.Second {
		t.Fatalf("弹幕参数快照异常: %+v", st)
	}
}

func TestUpdateSettingsAppliesToDetector(t *testing.T) {
	p := testPipeline(t)

	if triggered, _ := p.detector.Feed("今天天气不错", time.Now()); triggered {
		t.Fatal("默认关键词不应命中")
	}

	p.UpdateSettings(Settings{
		Keyword: config.KeywordConfig{
			StopDelaySeconds: 10, CooldownSeconds: 1, T2Threshold: 0.8, T3CooccurWindow: 20,
			T1: []string{"天气"},
		},
		Danmaku: config.DanmakuConfig{
			Enabled: false, Keywords: []string{"天气"}, DensityThreshold: 3, WindowSeconds: 10,
			ConfirmDelay: time.Second,
		},
	})

	if triggered, _ := p.detector.Feed("天气真好", time.Now()); !triggered {
		t.Fatal("更新后的关键词应立即生效")
	}
	st := p.Status()
	if st.StopDelay != 10*time.Second || st.DanmakuThreshold != 3 || st.DanmakuWindow != 10*time.Second {
		t.Fatalf("状态未反映新设置: %+v", st)
	}
}

func TestUpdateSettingsKeepsDanmakuWindowCount(t *testing.T) {
	p := testPipeline(t)
	now := time.Now()
	p.dmDensity.countHit("星瞳来了", now)

	p.UpdateSettings(Settings{
		Keyword: config.KeywordConfig{
			StopDelaySeconds: 120, CooldownSeconds: 5, T2Threshold: 0.75, T3CooccurWindow: 30,
			T1: []string{"测试"},
		},
		Danmaku: config.DanmakuConfig{
			Enabled: false, Keywords: []string{"测试"}, DensityThreshold: 5, WindowSeconds: 30,
			ConfirmDelay: time.Second,
		},
	})
	if hits := p.dmDensity.count(); hits != 1 {
		t.Fatalf("窗口内的命中应保留，实际 %d", hits)
	}
}

func TestTriggerRequiresSession(t *testing.T) {
	p := testPipeline(t)

	if p.Trigger() {
		t.Fatal("没有直播会话时不应触发")
	}

	p.sessionActive.Store(true)
	if !p.Trigger() {
		t.Fatal("会话进行中且空闲时应可手动触发")
	}
	if p.recorder.State() != recorder.StateRecording {
		t.Fatal("触发后应进入录制态")
	}
	st := p.Status()
	if !st.Recording || st.TriggerSource != "manual" || st.TriggerAt.IsZero() {
		t.Fatalf("状态未反映录制: %+v", st)
	}

	if !p.StopRecording() {
		t.Fatal("录制中应可结束录制")
	}
	if p.recorder.State() != recorder.StateIdle {
		t.Fatal("结束后应回到空闲")
	}
	if p.StopRecording() {
		t.Fatal("空闲时结束录制应返回 false")
	}
}

func TestStatusTracksRoomAndASR(t *testing.T) {
	p := testPipeline(t)
	p.setRoomStatus(12345, "测试直播间", 1)
	p.setSession(time.Now(), true)
	p.setLastASR("今天聊点什么", time.Now())

	st := p.Status()
	if st.RoomID != 12345 || st.RoomTitle != "测试直播间" || st.LiveStatus != 1 {
		t.Fatalf("房间状态异常: %+v", st)
	}
	if !st.Live || st.SessionSince.IsZero() {
		t.Fatalf("会话状态异常: %+v", st)
	}
	if st.LastASRText != "今天聊点什么" || st.LastASRAt.IsZero() {
		t.Fatalf("ASR 状态异常: %+v", st)
	}

	p.setSession(time.Time{}, false)
	if p.Status().Live || !p.Status().SessionSince.IsZero() {
		t.Fatal("会话结束后应清空时长")
	}
}

func TestEventsReachSubscriber(t *testing.T) {
	p := testPipeline(t)
	events := make(chan Event, 8)
	p.SetOnEvent(func(ev Event) { events <- ev })

	p.sessionActive.Store(true)
	if !p.Trigger() {
		t.Fatal("手动触发失败")
	}
	select {
	case ev := <-events:
		if ev.Type != EventTrigger || ev.Source != "manual" {
			t.Fatalf("触发事件异常: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到触发事件")
	}
}

func TestPipelineStopsOnCancel(t *testing.T) {
	p := testPipeline(t)
	p.asrEngine = &scriptedEngine{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("取消后应正常返回，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消后流水线未退出")
	}
}
