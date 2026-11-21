// Package app 管理录制服务的生命周期与对外可观测状态，
// 供命令行入口与监控前端共用：启动/停止/重启流水线、收集切片与事件、
// 在线调整关键词规则。
package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/pipeline"
)

// 服务状态。
const (
	StateStopped   = "stopped"
	StateWaiting   = "waiting"
	StateLive      = "live"
	StateRecording = "recording"
)

var (
	// ErrNotRunning 服务未在运行。
	ErrNotRunning = errors.New("服务未在运行")
	// ErrAlreadyRunning 服务已在运行。
	ErrAlreadyRunning = errors.New("服务已在运行")
	// ErrRestartRequired 该设置需要重启才能生效。
	ErrRestartRequired = errors.New("该设置需要重启服务后生效")
)

// Runner 是流水线对外接口，抽出以便测试替换。
type Runner interface {
	Run(ctx context.Context) error
	Status() pipeline.Status
	SetOnEvent(func(pipeline.Event))
	Trigger() bool
	StopRecording() bool
	UpdateSettings(pipeline.Settings)
	CurrentSettings() pipeline.Settings
}

// App 录制服务的生命周期与状态。
type App struct {
	mu      sync.RWMutex
	cfg     *config.Config
	logger  *slog.Logger
	version string

	runner    Runner
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   bool
	startedAt time.Time
	lastErr   string

	// ctlMu 串行化启动/停止/重启，避免并发操作把状态搞乱。
	ctlMu sync.Mutex

	clipsMu sync.RWMutex
	clips   []Clip

	hub *eventHub

	newRunner func(*config.Config, *slog.Logger) (Runner, error)
}

// New 创建服务（不自动启动）。
func New(cfg *config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{
		cfg:    cfg,
		logger: logger,
		hub:    newEventHub(),
		newRunner: func(c *config.Config, l *slog.Logger) (Runner, error) {
			return pipeline.New(c, l)
		},
	}
	a.clips = scanClips(cfg.Recorder.OutputDir)
	return a, nil
}

// SetVersion 设置对外展示的版本号。
func (a *App) SetVersion(v string) { a.version = v }

// Start 启动录制服务（异步运行）。
func (a *App) Start() error {
	a.ctlMu.Lock()
	defer a.ctlMu.Unlock()
	return a.startLocked()
}

// Stop 停止录制服务，等待流水线收尾（包括未完成的切片合并）。
func (a *App) Stop() error {
	a.ctlMu.Lock()
	defer a.ctlMu.Unlock()
	return a.stop()
}

func (a *App) stop() error {
	a.mu.RLock()
	cancel, running := a.cancel, a.running
	a.mu.RUnlock()
	if !running || cancel == nil {
		return ErrNotRunning
	}
	cancel()
	a.wg.Wait()
	return nil
}

// Restart 重启服务；roomID > 0 时切换目标直播间。
func (a *App) Restart(roomID int) error {
	a.ctlMu.Lock()
	defer a.ctlMu.Unlock()

	_ = a.stop() // 未运行时忽略；先停再改配置，避免与正在运行的流水线竞争
	if roomID > 0 {
		a.mu.Lock()
		a.cfg.RoomID = roomID
		a.mu.Unlock()
	}
	return a.startLocked()
}

// startLocked 与 Start 相同的逻辑，调用方需持有 ctlMu。
func (a *App) startLocked() error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return ErrAlreadyRunning
	}
	cfg, logger := a.cfg, a.logger
	a.mu.Unlock()

	runner, err := a.newRunner(cfg, logger)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())

	a.mu.Lock()
	a.runner = runner
	a.cancel = cancel
	a.running = true
	a.startedAt = time.Now()
	a.lastErr = ""
	a.mu.Unlock()

	runner.SetOnEvent(a.handleEvent)
	a.hub.publish(pipeline.Event{Type: "service_start", At: time.Now(), Message: "服务已启动"})

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := runner.Run(ctx)

		a.mu.Lock()
		a.running = false
		a.runner = nil
		a.cancel = nil
		if err != nil && ctx.Err() == nil {
			a.lastErr = err.Error()
		}
		a.mu.Unlock()

		if err != nil && ctx.Err() == nil {
			logger.Error("录制服务异常退出", "err", err)
			a.hub.publish(pipeline.Event{Type: "error", At: time.Now(), Message: err.Error()})
			return
		}
		a.hub.publish(pipeline.Event{Type: "service_stop", At: time.Now(), Message: "服务已停止"})
	}()
	return nil
}

// Trigger 手动触发一次录制。
func (a *App) Trigger() error {
	a.mu.RLock()
	runner := a.runner
	a.mu.RUnlock()
	if runner == nil {
		return ErrNotRunning
	}
	if !runner.Trigger() {
		return errors.New("当前无法触发：需要直播进行中且未在录制")
	}
	return nil
}

// StopRecording 结束当前录制并产出切片。
func (a *App) StopRecording() error {
	a.mu.RLock()
	runner := a.runner
	a.mu.RUnlock()
	if runner == nil {
		return ErrNotRunning
	}
	if !runner.StopRecording() {
		return errors.New("当前没有进行中的录制")
	}
	return nil
}

// Settings 返回当前生效的规则（运行中取流水线内的实时值）。
func (a *App) Settings() SettingsView {
	a.mu.RLock()
	runner := a.runner
	kw := a.cfg.Keyword
	dm := a.cfg.Danmaku
	roomID := a.cfg.RoomID
	a.mu.RUnlock()

	if runner != nil {
		s := runner.CurrentSettings()
		kw, dm = s.Keyword, s.Danmaku
	}
	return SettingsView{
		RoomID:          roomID,
		Keyword:         kw,
		Danmaku:         danmakuView(dm),
		RestartRequired: []string{"room_id", "danmaku.enabled"},
	}
}

// ApplySettings 应用在线可调的规则并立即生效；需要重启的字段返回 ErrRestartRequired。
func (a *App) ApplySettings(kw config.KeywordConfig, dm config.DanmakuConfig) error {
	a.mu.Lock()
	check := *a.cfg
	check.Keyword = kw
	check.Danmaku = dm
	if err := check.Validate(); err != nil {
		a.mu.Unlock()
		return err
	}
	if dm.Enabled != a.cfg.Danmaku.Enabled {
		a.mu.Unlock()
		return ErrRestartRequired
	}
	a.cfg.Keyword = kw
	a.cfg.Danmaku = dm
	runner := a.runner
	a.mu.Unlock()

	if runner != nil {
		runner.UpdateSettings(pipeline.Settings{Keyword: kw, Danmaku: dm})
	}
	return nil
}

// Snapshot 返回监控面板所需的完整状态。
func (a *App) Snapshot() Snapshot {
	a.mu.RLock()
	runner := a.runner
	running := a.running
	startedAt := a.startedAt
	lastErr := a.lastErr
	roomID := a.cfg.RoomID
	version := a.version
	a.mu.RUnlock()

	snap := Snapshot{
		Service: ServiceStatus{
			Running:   running,
			State:     StateStopped,
			StateText: "已停止",
			Version:   version,
			LastError: lastErr,
		},
		Room: RoomStatus{ID: roomID, LiveText: "未开播"},
	}
	if runner == nil {
		snap.Clips = a.ClipSummary()
		return snap
	}

	st := runner.Status()
	snap.Room = RoomStatus{
		ID:         firstNonZero(st.RoomID, roomID),
		Title:      st.RoomTitle,
		LiveStatus: st.LiveStatus,
		LiveText:   liveText(st.LiveStatus),
	}
	snap.Service.State, snap.Service.StateText = serviceState(st)
	if running {
		snap.Service.Since = optTime(startedAt)
	}

	snap.Session = SessionStatus{
		Active:      st.Live,
		Since:       optTime(st.SessionSince),
		DurationMS:  millis(time.Since(st.SessionSince), st.Live),
		StreamURL:   st.StreamURL,
		StreamSince: optTime(st.StreamSince),
	}
	snap.Buffer = BufferStatus{
		Segments:  st.Buffer.Segments,
		Newest:    optTime(st.Buffer.Newest),
		SpanMS:    st.Buffer.Span.Milliseconds(),
		StalledMS: st.Buffer.Stalled.Milliseconds(),
	}
	snap.Recorder = RecorderStatus{
		Recording:   st.Recording,
		TriggerAt:   optTime(st.TriggerAt),
		Source:      st.TriggerSource,
		ElapsedMS:   millis(time.Since(st.TriggerAt), st.Recording),
		StopDelayMS: st.StopDelay.Milliseconds(),
	}
	snap.ASR = ASRStatus{
		Type:     st.ASRType,
		LastText: st.LastASRText,
		LastAt:   optTime(st.LastASRAt),
	}
	snap.Danmaku = DanmakuStatus{
		Enabled:   st.DanmakuEnabled,
		Connected: st.DanmakuConnected,
		Hits:      st.DanmakuHits,
		Threshold: st.DanmakuThreshold,
		WindowMS:  st.DanmakuWindow.Milliseconds(),
	}
	snap.Clips = a.ClipSummary()
	return snap
}

// Clips 返回切片列表（按触发时间倒序）。
func (a *App) Clips() []Clip {
	a.clipsMu.RLock()
	defer a.clipsMu.RUnlock()
	out := make([]Clip, len(a.clips))
	copy(out, a.clips)
	return out
}

// ClipSummary 返回切片汇总信息。
func (a *App) ClipSummary() ClipsStatus {
	a.clipsMu.RLock()
	defer a.clipsMu.RUnlock()
	st := ClipsStatus{Count: len(a.clips)}
	for _, c := range a.clips {
		st.TotalBytes += c.Size
	}
	if len(a.clips) > 0 {
		st.Last = a.clips[0].Name
	}
	return st
}

// ClipPath 返回切片文件的绝对路径；文件名不合法或文件不存在时返回 false。
func (a *App) ClipPath(name string) (string, bool) {
	if name == "" || name != filepath.Base(name) {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ts", ".mp4":
	default:
		return "", false
	}
	a.mu.RLock()
	dir := a.cfg.Recorder.OutputDir
	a.mu.RUnlock()
	path := filepath.Join(dir, name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", false
	}
	return path, true
}

// AddClip 记录新产出的切片（供事件回调使用）。
func (a *App) AddClip(c Clip) {
	a.clipsMu.Lock()
	defer a.clipsMu.Unlock()
	for i, old := range a.clips {
		if old.Name == c.Name {
			a.clips[i] = c
			return
		}
	}
	a.clips = append([]Clip{c}, a.clips...)
	sort.SliceStable(a.clips, func(i, j int) bool {
		return a.clips[i].TriggerAt.After(a.clips[j].TriggerAt)
	})
	if len(a.clips) > maxClips {
		a.clips = a.clips[:maxClips]
	}
}

// SubscribeEvents 订阅流水线事件，返回最近事件与取消函数。
func (a *App) SubscribeEvents() ([]pipeline.Event, <-chan pipeline.Event, func()) {
	return a.hub.subscribe()
}

// handleEvent 接收流水线事件：刷新切片并广播给前端。
func (a *App) handleEvent(ev pipeline.Event) {
	if ev.Type == pipeline.EventClip && ev.File != "" {
		if info, err := os.Stat(ev.File); err == nil {
			a.AddClip(ClipFromFile(filepath.Base(ev.File), info))
		}
	}
	a.hub.publish(ev)
}

func serviceState(st pipeline.Status) (string, string) {
	switch {
	case st.Recording:
		return StateRecording, "录制中"
	case st.Live:
		return StateLive, "直播中"
	default:
		return StateWaiting, "等待开播"
	}
}

func liveText(status int) string {
	switch status {
	case 1:
		return "直播中"
	case 2:
		return "轮播中"
	default:
		return "未开播"
	}
}

func millis(d time.Duration, valid bool) int64 {
	if !valid || d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// optTime 把零值时间转换为 JSON null，避免前端收到 0001-01-01。
func optTime(t time.Time) *time.Time {
	if t.IsZero() || t.Year() < 2000 {
		return nil
	}
	return &t
}

func firstNonZero(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

func danmakuView(dm config.DanmakuConfig) DanmakuSettings {
	return DanmakuSettings{
		Enabled:             dm.Enabled,
		Keywords:            dm.Keywords,
		DensityThreshold:    dm.DensityThreshold,
		WindowSeconds:       dm.WindowSeconds,
		ConfirmDelaySeconds: dm.ConfirmDelay.Seconds(),
	}
}

// Snapshot 监控面板的完整状态。
type Snapshot struct {
	Service  ServiceStatus  `json:"service"`
	Room     RoomStatus     `json:"room"`
	Session  SessionStatus  `json:"session"`
	Buffer   BufferStatus   `json:"buffer"`
	Recorder RecorderStatus `json:"recorder"`
	ASR      ASRStatus      `json:"asr"`
	Danmaku  DanmakuStatus  `json:"danmaku"`
	Clips    ClipsStatus    `json:"clips"`
}

// ServiceStatus 服务运行状态。
type ServiceStatus struct {
	Running   bool       `json:"running"`
	State     string     `json:"state"`
	StateText string     `json:"state_text"`
	Since     *time.Time `json:"since,omitempty"`
	Version   string     `json:"version,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// RoomStatus 直播间状态。
type RoomStatus struct {
	ID         int    `json:"id"`
	Title      string `json:"title,omitempty"`
	LiveStatus int    `json:"live_status"`
	LiveText   string `json:"live_text"`
}

// SessionStatus 当前直播会话。
type SessionStatus struct {
	Active      bool       `json:"active"`
	Since       *time.Time `json:"since,omitempty"`
	DurationMS  int64      `json:"duration_ms"`
	StreamURL   string     `json:"stream_url,omitempty"`
	StreamSince *time.Time `json:"stream_since,omitempty"`
}

// BufferStatus 视频缓冲状态。
type BufferStatus struct {
	Segments  int        `json:"segments"`
	Newest    *time.Time `json:"newest,omitempty"`
	SpanMS    int64      `json:"span_ms"`
	StalledMS int64      `json:"stalled_ms"`
}

// RecorderStatus 录制状态机。
type RecorderStatus struct {
	Recording   bool       `json:"recording"`
	TriggerAt   *time.Time `json:"trigger_at,omitempty"`
	Source      string     `json:"source,omitempty"`
	ElapsedMS   int64      `json:"elapsed_ms"`
	StopDelayMS int64      `json:"stop_delay_ms"`
}

// ASRStatus 语音识别状态。
type ASRStatus struct {
	Type     string     `json:"type"`
	LastText string     `json:"last_text,omitempty"`
	LastAt   *time.Time `json:"last_at,omitempty"`
}

// DanmakuStatus 弹幕辅助触发状态。
type DanmakuStatus struct {
	Enabled   bool  `json:"enabled"`
	Connected bool  `json:"connected"`
	Hits      int   `json:"hits"`
	Threshold int   `json:"threshold"`
	WindowMS  int64 `json:"window_ms"`
}

// ClipsStatus 切片汇总。
type ClipsStatus struct {
	Count      int    `json:"count"`
	TotalBytes int64  `json:"total_bytes"`
	Last       string `json:"last,omitempty"`
}

// SettingsView 前端可编辑的规则。
type SettingsView struct {
	RoomID          int                  `json:"room_id"`
	Keyword         config.KeywordConfig `json:"keyword"`
	Danmaku         DanmakuSettings      `json:"danmaku"`
	RestartRequired []string             `json:"restart_required"`
}

// DanmakuSettings 弹幕密度设置（时长以秒暴露给前端）。
type DanmakuSettings struct {
	Enabled             bool     `json:"enabled"`
	Keywords            []string `json:"keywords"`
	DensityThreshold    int      `json:"density_threshold"`
	WindowSeconds       int      `json:"window_seconds"`
	ConfirmDelaySeconds float64  `json:"confirm_delay_seconds"`
}

// SettingsPatch POST /api/settings 的请求体。
type SettingsPatch struct {
	Keyword config.KeywordConfig `json:"keyword"`
	Danmaku DanmakuSettings      `json:"danmaku"`
}

// ToConfig 把前端提交的弹幕设置转换为配置结构。
func (p SettingsPatch) ToDanmakuConfig() config.DanmakuConfig {
	return config.DanmakuConfig{
		Enabled:          p.Danmaku.Enabled,
		Keywords:         p.Danmaku.Keywords,
		DensityThreshold: p.Danmaku.DensityThreshold,
		WindowSeconds:    p.Danmaku.WindowSeconds,
		ConfirmDelay:     time.Duration(p.Danmaku.ConfirmDelaySeconds * float64(time.Second)),
	}
}

// Sanitize 去掉重复/空白的关键词，避免前端提交脏数据。
func SanitizeKeywords(words []string) []string {
	out := make([]string, 0, len(words))
	seen := make(map[string]struct{}, len(words))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if _, ok := seen[w]; ok {
			continue
		}
		seen[w] = struct{}{}
		out = append(out, w)
	}
	return out
}

// SanitizePatch 规范化前端提交的规则。
func SanitizePatch(p SettingsPatch) SettingsPatch {
	p.Keyword.T1 = SanitizeKeywords(p.Keyword.T1)
	p.Keyword.T2 = SanitizeKeywords(p.Keyword.T2)
	p.Keyword.T3 = SanitizeKeywords(p.Keyword.T3)
	p.Keyword.T4 = SanitizeKeywords(p.Keyword.T4)
	p.Danmaku.Keywords = SanitizeKeywords(p.Danmaku.Keywords)
	return p
}
