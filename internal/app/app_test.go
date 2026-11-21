package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/pipeline"
)

// fakeRunner 用于测试服务生命周期，替代真实流水线。
type fakeRunner struct {
	mu      sync.Mutex
	status  pipeline.Status
	events  []pipeline.Event
	onEvent func(pipeline.Event)

	started chan struct{}
	stopped chan struct{}

	triggerOK bool
	stopRecOK bool
	updates   []pipeline.Settings
	runErr    error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		started:   make(chan struct{}, 4),
		stopped:   make(chan struct{}, 4),
		triggerOK: true,
		stopRecOK: true,
	}
}

func (f *fakeRunner) Run(ctx context.Context) error {
	select {
	case f.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case f.stopped <- struct{}{}:
	default:
	}
	return f.runErr
}

func (f *fakeRunner) Status() pipeline.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeRunner) SetOnEvent(fn func(pipeline.Event)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onEvent = fn
}

func (f *fakeRunner) Trigger() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.triggerOK {
		f.status.Recording = true
	}
	return f.triggerOK
}

func (f *fakeRunner) StopRecording() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopRecOK {
		f.status.Recording = false
	}
	return f.stopRecOK
}

func (f *fakeRunner) UpdateSettings(s pipeline.Settings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, s)
}

func (f *fakeRunner) CurrentSettings() pipeline.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.updates) > 0 {
		return f.updates[len(f.updates)-1]
	}
	return pipeline.Settings{}
}

func (f *fakeRunner) emit(ev pipeline.Event) {
	f.mu.Lock()
	fn := f.onEvent
	f.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.ASR.Type = "none"
	cfg.Danmaku.Enabled = false
	cfg.Recorder.OutputDir = filepath.Join(t.TempDir(), "recordings")
	cfg.Recorder.VideoBufferDir = filepath.Join(t.TempDir(), "video_buffer")
	return cfg
}

func newTestApp(t *testing.T) (*App, *fakeRunner) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(testConfig(t), logger)
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	runner := newFakeRunner()
	a.newRunner = func(*config.Config, *slog.Logger) (Runner, error) { return runner, nil }
	return a, runner
}

func TestStartStopLifecycle(t *testing.T) {
	a, runner := newTestApp(t)

	if a.Snapshot().Service.Running {
		t.Fatal("初始状态应为未运行")
	}
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("流水线未启动")
	}
	if snap := a.Snapshot(); !snap.Service.Running || snap.Service.State == StateStopped {
		t.Fatalf("运行态快照异常: %+v", snap.Service)
	}

	if err := a.Stop(); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	select {
	case <-runner.stopped:
	case <-time.After(time.Second):
		t.Fatal("流水线未退出")
	}
	if snap := a.Snapshot(); snap.Service.Running || snap.Service.State != StateStopped {
		t.Fatalf("停止后快照异常: %+v", snap.Service)
	}
	if err := a.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("重复停止应返回 ErrNotRunning，实际: %v", err)
	}
}

func TestStartTwiceRejected(t *testing.T) {
	a, _ := newTestApp(t)
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()
	if err := a.Start(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("重复启动应返回 ErrAlreadyRunning，实际: %v", err)
	}
}

func TestTriggerAndStopRecording(t *testing.T) {
	a, runner := newTestApp(t)

	if err := a.Trigger(); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("未运行时触发应报错，实际: %v", err)
	}
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()

	if err := a.Trigger(); err != nil {
		t.Fatalf("触发失败: %v", err)
	}
	if !a.Snapshot().Recorder.Recording {
		t.Fatal("触发后应为录制中")
	}
	if err := a.StopRecording(); err != nil {
		t.Fatalf("结束录制失败: %v", err)
	}
	if a.Snapshot().Recorder.Recording {
		t.Fatal("结束后应回到空闲")
	}

	runner.mu.Lock()
	runner.triggerOK = false
	runner.mu.Unlock()
	if err := a.Trigger(); err == nil {
		t.Fatal("流水线拒绝触发时应返回错误")
	}
}

func TestSnapshotStateMapping(t *testing.T) {
	a, runner := newTestApp(t)
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()

	cases := []struct {
		status pipeline.Status
		state  string
		text   string
	}{
		{pipeline.Status{Live: true}, StateLive, "直播中"},
		{pipeline.Status{Live: true, Recording: true, TriggerSource: "asr", TriggerAt: time.Now()}, StateRecording, "录制中"},
		{pipeline.Status{}, StateWaiting, "等待开播"},
	}
	for _, c := range cases {
		runner.mu.Lock()
		runner.status = c.status
		runner.mu.Unlock()
		snap := a.Snapshot()
		if snap.Service.State != c.state || snap.Service.StateText != c.text {
			t.Fatalf("状态映射错误: got %s/%s want %s/%s", snap.Service.State, snap.Service.StateText, c.state, c.text)
		}
	}
}

func TestApplySettings(t *testing.T) {
	a, runner := newTestApp(t)
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()

	kw := config.KeywordConfig{
		StopDelaySeconds: 30, CooldownSeconds: 5, T2Threshold: 0.8, T3CooccurWindow: 20,
		T1: []string{"新关键词"},
	}
	dm := config.DanmakuConfig{Enabled: false, Keywords: []string{"新关键词"}, DensityThreshold: 4, WindowSeconds: 20, ConfirmDelay: time.Second}
	if err := a.ApplySettings(kw, dm); err != nil {
		t.Fatalf("应用设置失败: %v", err)
	}
	runner.mu.Lock()
	updates := len(runner.updates)
	runner.mu.Unlock()
	if updates != 1 {
		t.Fatalf("应下发一次设置，实际 %d", updates)
	}
	if got := a.Settings().Keyword.StopDelaySeconds; got != 30 {
		t.Fatalf("设置未生效: stop_delay=%d", got)
	}

	// 非法阈值被配置校验拦下
	bad := kw
	bad.T2Threshold = 2
	if err := a.ApplySettings(bad, dm); err == nil {
		t.Fatal("非法阈值应被拒绝")
	}

	// 弹幕开关需要重启
	flip := dm
	flip.Enabled = true
	flip.Keywords = []string{"词"}
	if err := a.ApplySettings(kw, flip); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("弹幕开关应返回 ErrRestartRequired，实际: %v", err)
	}
}

func TestRestartSwitchesRoom(t *testing.T) {
	a, _ := newTestApp(t)
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()

	if err := a.Restart(456); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if got := a.Snapshot().Room.ID; got != 456 {
		t.Fatalf("房间号 = %d，期望 456", got)
	}
	if got := a.Settings().RoomID; got != 456 {
		t.Fatalf("设置中的房间号 = %d，期望 456", got)
	}
}

func TestClipScanAndPath(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"hym_clip_asr_20260102_120000.ts",
		"hym_clip_danmaku_20260101_120000.mp4",
		"hym_clip_manual_20260103_120000.ts",
		"not_a_clip.ts",
		"notes.txt",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}

	cfg := testConfig(t)
	cfg.Recorder.OutputDir = dir
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}

	clips := a.Clips()
	if len(clips) != 3 {
		t.Fatalf("应识别 3 个切片，实际 %d: %+v", len(clips), clips)
	}
	if clips[0].Name != "hym_clip_manual_20260103_120000.ts" {
		t.Fatalf("应按触发时间倒序，首条 = %s", clips[0].Name)
	}
	if clips[0].Source != "manual" || clips[0].TriggerAt.IsZero() {
		t.Fatalf("文件名解析异常: %+v", clips[0])
	}

	if _, ok := a.ClipPath(clips[0].Name); !ok {
		t.Fatal("存在的切片应可下载")
	}
	for _, name := range []string{"", "../config.yaml", "notes.txt", "missing.ts"} {
		if _, ok := a.ClipPath(name); ok {
			t.Fatalf("%q 不应可下载", name)
		}
	}
}

func TestClipEventAddedToList(t *testing.T) {
	a, runner := newTestApp(t)
	if err := a.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer a.Stop()

	file := filepath.Join(a.cfg.Recorder.OutputDir, "hym_clip_asr_20260105_101010.ts")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(file, []byte("clip"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	recent, ch, unsubscribe := a.SubscribeEvents()
	defer unsubscribe()
	if len(recent) == 0 {
		// 启动事件已入队即满足；主要验证事件能被转发
		t.Log("启动事件尚未入队")
	}
	runner.emit(pipeline.Event{Type: pipeline.EventClip, File: file, Source: "asr", Size: 4})

	select {
	case ev := <-ch:
		if ev.Type != pipeline.EventClip {
			t.Fatalf("事件类型 = %s", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到切片事件")
	}

	clips := a.Clips()
	if len(clips) != 1 || clips[0].Size != 4 {
		t.Fatalf("切片未加入列表: %+v", clips)
	}
}
