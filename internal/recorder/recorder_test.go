package recorder

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSegmentTime(t *testing.T) {
	loc := time.Local
	cases := []struct {
		name string
		want time.Time
	}{
		{"seg_20260914_120000.ts", time.Date(2026, 9, 14, 12, 0, 0, 0, loc)},
		{"seg_20260914_120010.ts", time.Date(2026, 9, 14, 12, 0, 10, 0, loc)},
		{"seg_20260914_120000_1750000000.ts", time.Date(2026, 9, 14, 12, 0, 0, 0, loc)}, // 旧格式兼容
		{"not_a_segment.ts", time.Time{}},
		{"seg_badname.ts", time.Time{}},
		{"", time.Time{}},
	}
	for _, c := range cases {
		if got := segmentTime(c.name); !got.Equal(c.want) {
			t.Errorf("segmentTime(%q) = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestSegmentsInRange(t *testing.T) {
	dir := t.TempDir()
	vb, err := NewVideoBuffer(VideoBufferConfig{
		Dir:            dir,
		SegmentSeconds: 10,
		KeepDuration:   5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewVideoBuffer 失败: %v", err)
	}

	// 造 5 个假分段：本地时间 12:00:00 起，每 10 秒一个
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 14, 12, 0, i*10, 0, time.Local)
		name := filepath.Join(dir, "seg_"+ts.Format("20060102_150405")+".ts")
		if err := os.WriteFile(name, []byte("fake"), 0o644); err != nil {
			t.Fatalf("写假分段失败: %v", err)
		}
	}
	// 干扰文件
	_ = os.WriteFile(filepath.Join(dir, "other.ts"), []byte("x"), 0o644)

	start := time.Date(2026, 9, 14, 12, 0, 15, 0, time.Local)
	end := time.Date(2026, 9, 14, 12, 0, 35, 0, time.Local)
	got := vb.SegmentsInRange(start, end)
	if len(got) != 3 {
		t.Fatalf("期望 3 个分段，实际 %d: %v", len(got), got)
	}
	// 覆盖 start 前一整段：首段应为 12:00:10
	wantFirst := filepath.Join(dir, "seg_20260914_120010.ts")
	if got[0] != wantFirst {
		t.Fatalf("首段应为 %v，实际 %v", wantFirst, got[0])
	}
	// 升序
	for i := 1; i < len(got); i++ {
		if segmentTime(got[i]).Before(segmentTime(got[i-1])) {
			t.Fatalf("分段未升序: %v", got)
		}
	}
}

func TestControllerStateMachine(t *testing.T) {
	dir := t.TempDir()
	vb, err := NewVideoBuffer(VideoBufferConfig{Dir: filepath.Join(dir, "vbuf"), SegmentSeconds: 10})
	if err != nil {
		t.Fatalf("NewVideoBuffer 失败: %v", err)
	}
	ctrl, err := NewController(ControllerConfig{
		OutputDir:      filepath.Join(dir, "out"),
		FilenamePrefix: "hym_clip",
		PreRollSeconds: 90,
		MergeFormat:    "ts",
		FFmpegPath:     "ffmpeg",
	}, vb)
	if err != nil {
		t.Fatalf("NewController 失败: %v", err)
	}

	if ctrl.State() != StateIdle {
		t.Fatal("初始应为 idle")
	}
	// 未录制时 OnStop 返回 nil
	if sess := ctrl.OnStop(); sess != nil {
		t.Fatal("idle 状态下 OnStop 应返回 nil")
	}

	now := time.Now()
	ctrl.OnTrigger(now, "asr")
	if ctrl.State() != StateRecording {
		t.Fatal("触发后应为 recording")
	}
	// 录制中重复触发被忽略
	ctrl.OnTrigger(now.Add(2*time.Second), "danmaku")
	if ctrl.State() != StateRecording {
		t.Fatal("录制中重复触发应保持 recording")
	}

	sess := ctrl.OnStop()
	if sess == nil {
		t.Fatal("recording 状态下 OnStop 应返回 session")
	}
	if sess.Source != "asr" {
		t.Fatalf("触发来源 = %s", sess.Source)
	}
	if !sess.TriggerAt.Equal(now) {
		t.Fatal("TriggerAt 应为首个触发时刻")
	}
	if ctrl.State() != StateIdle {
		t.Fatal("停止后应为 idle")
	}
}
