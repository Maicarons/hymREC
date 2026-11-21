package pipeline

import (
	"time"

	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/keyword"
	"github.com/Maicarons/hymREC/internal/recorder"
)

// 事件类型（Event.Type）。
const (
	EventSessionStart  = "session_start"
	EventSessionEnd    = "session_end"
	EventTrigger       = "trigger"
	EventClip          = "clip"
	EventStreamRefresh = "stream_refresh"
	EventBufferRestart = "buffer_restart"
	EventError         = "error"
)

// Event 流水线事件：触发、切片产出、会话开始/结束与自愈动作。
// 监控前端通过事件流实时展示，日志另有专门的采集通道。
type Event struct {
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Message string    `json:"message,omitempty"`
	Source  string    `json:"source,omitempty"`
	File    string    `json:"file,omitempty"`
	Size    int64     `json:"size,omitempty"`
}

// Status 流水线的只读运行时快照，供监控前端展示。
type Status struct {
	RoomID       int
	RoomTitle    string
	LiveStatus   int
	Live         bool
	SessionSince time.Time

	StreamURL   string
	StreamSince time.Time

	Recording     bool
	TriggerAt     time.Time
	TriggerSource string
	StopDelay     time.Duration

	LastASRText string
	LastASRAt   time.Time

	Buffer recorder.Stats

	ASRType          string
	DanmakuEnabled   bool
	DanmakuConnected bool
	DanmakuHits      int
	DanmakuThreshold int
	DanmakuWindow    time.Duration
}

// Settings 可在线调整的规则：关键词层级与弹幕密度。
type Settings struct {
	Keyword config.KeywordConfig
	Danmaku config.DanmakuConfig
}

// SetOnEvent 注册事件回调。回调应快速返回，不得阻塞流水线。
func (p *Pipeline) SetOnEvent(fn func(Event)) {
	p.emitMu.Lock()
	p.onEvent = fn
	p.emitMu.Unlock()
}

// Status 返回当前运行时快照。
func (p *Pipeline) Status() Status {
	p.statusMu.RLock()
	st := Status{
		RoomID:       p.roomID,
		RoomTitle:    p.roomTitle,
		LiveStatus:   p.liveStatus,
		Live:         p.sessionActive.Load(),
		SessionSince: p.sessionSince,
		StreamSince:  p.streamSince,
		LastASRText:  p.lastASRText,
		LastASRAt:    p.lastASRAt,
	}
	p.statusMu.RUnlock()

	ctrl := p.recorder.Snapshot()
	st.Recording = ctrl.State == recorder.StateRecording
	st.TriggerAt = ctrl.TriggerAt
	st.TriggerSource = ctrl.Source

	st.StreamURL = p.currentStreamURL()
	st.Buffer = p.videoBuf.Stats()

	settings := p.CurrentSettings()
	st.StopDelay = time.Duration(settings.Keyword.StopDelaySeconds) * time.Second
	st.ASRType = p.cfg.ASR.Type
	st.DanmakuEnabled = p.cfg.Danmaku.Enabled
	st.DanmakuThreshold = settings.Danmaku.DensityThreshold
	st.DanmakuWindow = time.Duration(settings.Danmaku.WindowSeconds) * time.Second
	st.DanmakuHits = p.dmDensity.count()
	if p.dmClient != nil {
		st.DanmakuConnected = p.dmClient.Connected()
	}
	return st
}

// CurrentSettings 返回当前生效的关键词与弹幕规则。
func (p *Pipeline) CurrentSettings() Settings {
	p.settingsMu.RLock()
	defer p.settingsMu.RUnlock()
	return Settings{Keyword: p.settings.Keyword, Danmaku: p.settings.Danmaku}
}

// UpdateSettings 在线替换关键词规则与弹幕密度参数，立即生效。
// 已开始的任务（录制中的切片、已建立的 ASR 连接）不受影响。
func (p *Pipeline) UpdateSettings(s Settings) {
	p.settingsMu.Lock()
	p.settings = s
	p.settingsMu.Unlock()

	p.detector.Update(keyword.Options{
		T1:              s.Keyword.T1,
		T2:              s.Keyword.T2,
		T3:              s.Keyword.T3,
		T4:              s.Keyword.T4,
		StopDelay:       time.Duration(s.Keyword.StopDelaySeconds) * time.Second,
		Cooldown:        time.Duration(s.Keyword.CooldownSeconds) * time.Second,
		T2Threshold:     s.Keyword.T2Threshold,
		T3CooccurWindow: time.Duration(s.Keyword.T3CooccurWindow * float64(time.Second)),
	})
	p.dmDensity.update(s.Danmaku.WindowSeconds, s.Danmaku.DensityThreshold, s.Danmaku.Keywords)
}

// Trigger 手动触发一次录制（需要直播会话进行中且当前空闲）。
// 返回是否成功进入录制态。
func (p *Pipeline) Trigger() bool {
	if !p.sessionActive.Load() || p.recorder.State() != recorder.StateIdle {
		return false
	}
	at := time.Now()
	p.recorder.OnTrigger(at, "manual")
	p.detector.Touch(at)
	p.emit(Event{Type: EventTrigger, At: at, Source: "manual", Message: "手动触发"})
	return true
}

// StopRecording 立即结束当前录制并产出切片。返回是否确实结束了录制。
func (p *Pipeline) StopRecording() bool {
	if p.recorder.State() != recorder.StateRecording {
		return false
	}
	p.finishRecording()
	return true
}

func (p *Pipeline) emit(ev Event) {
	p.emitMu.Lock()
	fn := p.onEvent
	p.emitMu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

// setRoomStatus 记录开播状态与标题（等待开播与看门狗都会刷新）。
func (p *Pipeline) setRoomStatus(roomID int, title string, liveStatus int) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	if roomID > 0 {
		p.roomID = roomID
	}
	if title != "" {
		p.roomTitle = title
	}
	p.liveStatus = liveStatus
}

func (p *Pipeline) realRoomID() int {
	p.statusMu.RLock()
	defer p.statusMu.RUnlock()
	return p.roomID
}

// setSession 记录会话开始/结束时刻，并维护会话标记（弹幕触发依赖该标记）。
func (p *Pipeline) setSession(at time.Time, active bool) {
	p.sessionActive.Store(active)
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	if active {
		p.sessionSince = at
	} else {
		p.sessionSince = time.Time{}
	}
}

func (p *Pipeline) setLastASR(text string, at time.Time) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.lastASRText = text
	p.lastASRAt = at
}

func (p *Pipeline) setStreamSince(at time.Time) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	p.streamSince = at
}
