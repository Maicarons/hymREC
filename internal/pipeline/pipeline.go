// Package pipeline 组装整个录制流水线：
//
//	B站流 → 音频采集(ffmpeg) → VAD → 环形缓冲 → ASR → 关键词检测 → 录制控制器
//	   └────────────── 弹幕监听(可选) ──→ 密度触发 ────────────────┘
//
// 运行期按「会话」组织：一次开播一个会话（拉流、缓冲、识别、触发），
// 主播下播或断流无法恢复时结束会话并回到等待开播状态。会话内的
// 自愈逻辑见 session.go。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Maicarons/hymREC/internal/asr"
	"github.com/Maicarons/hymREC/internal/bili"
	"github.com/Maicarons/hymREC/internal/config"
	"github.com/Maicarons/hymREC/internal/danmaku"
	"github.com/Maicarons/hymREC/internal/keyword"
	"github.com/Maicarons/hymREC/internal/recorder"
	"github.com/Maicarons/hymREC/internal/ringbuf"
)

// Pipeline 主流水线。
type Pipeline struct {
	cfg    *config.Config
	logger *slog.Logger
	bili   *bili.Client

	asrEngine asr.Engine
	detector  *keyword.Detector
	audioBuf  *ringbuf.Buffer
	videoBuf  *recorder.VideoBuffer
	recorder  *recorder.Controller
	dmClient  *danmaku.Client

	dmDensity *densityCounter

	// sessionActive 标记当前是否处于直播会话中：弹幕触发只在会话内生效，
	// 避免等待开播期间把录制状态机置为录制态（此时没有视频缓冲可回溯）。
	sessionActive atomic.Bool

	// statusMu 保护监控面板读取的状态字段。
	statusMu     sync.RWMutex
	roomTitle    string
	liveStatus   int
	sessionSince time.Time
	streamSince  time.Time
	lastASRText  string
	lastASRAt    time.Time

	// settingsMu 保护可在线调整的规则。
	settingsMu sync.RWMutex
	settings   Settings

	emitMu  sync.Mutex
	onEvent func(Event)

	mu          sync.Mutex
	roomID      int       // 真实房间号
	streamURL   string    // 最近一次获取的流地址
	lastRefresh time.Time // 最近一次刷新流地址的时刻
}

// New 创建流水线（不启动）。
func New(cfg *config.Config, logger *slog.Logger) (*Pipeline, error) {
	return NewWithClient(cfg, logger, nil)
}

// NewWithClient 创建流水线，允许注入 B 站 API 客户端（测试用）。
func NewWithClient(cfg *config.Config, logger *slog.Logger, biliClient *bili.Client) (*Pipeline, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if biliClient == nil {
		biliClient = bili.NewClient(nil)
	}
	eng, err := asr.New(cfg.ASR)
	if err != nil {
		return nil, err
	}
	det := keyword.New(keyword.Options{
		T1:              cfg.Keyword.T1,
		T2:              cfg.Keyword.T2,
		T3:              cfg.Keyword.T3,
		T4:              cfg.Keyword.T4,
		StopDelay:       time.Duration(cfg.Keyword.StopDelaySeconds) * time.Second,
		Cooldown:        time.Duration(cfg.Keyword.CooldownSeconds) * time.Second,
		T2Threshold:     cfg.Keyword.T2Threshold,
		T3CooccurWindow: time.Duration(cfg.Keyword.T3CooccurWindow * float64(time.Second)),
	})

	audioBuf := ringbuf.New(cfg.RingBufferSeconds, cfg.SampleRate, cfg.ChunkSamples())

	videoBuf, err := recorder.NewVideoBuffer(recorder.VideoBufferConfig{
		Dir:            cfg.Recorder.VideoBufferDir,
		SegmentSeconds: cfg.Recorder.VideoSegmentSeconds,
		KeepDuration:   time.Duration(cfg.Recorder.VideoKeepMinutes) * time.Minute,
		FFmpegPath:     cfg.Recorder.FFmpegPath,
	})
	if err != nil {
		return nil, err
	}
	ctrl, err := recorder.NewController(recorder.ControllerConfig{
		OutputDir:      cfg.Recorder.OutputDir,
		FilenamePrefix: cfg.Recorder.FilenamePrefix,
		PreRollSeconds: cfg.RingBufferSeconds,
		MergeFormat:    cfg.Recorder.MergeFormat,
		FFmpegPath:     cfg.Recorder.FFmpegPath,
	}, videoBuf)
	if err != nil {
		return nil, err
	}

	p := &Pipeline{
		cfg:       cfg,
		logger:    logger,
		bili:      biliClient,
		asrEngine: eng,
		detector:  det,
		audioBuf:  audioBuf,
		videoBuf:  videoBuf,
		recorder:  ctrl,
		dmDensity: newDensityCounter(cfg.Danmaku.WindowSeconds, cfg.Danmaku.DensityThreshold, cfg.Danmaku.Keywords),
		settings:  Settings{Keyword: cfg.Keyword, Danmaku: cfg.Danmaku},
	}
	if cfg.Danmaku.Enabled {
		p.dmClient = danmaku.New(danmaku.Options{
			RoomID: cfg.RoomID,
			Logger: logger,
		}, p.onDanmakuMessage)
	}
	return p, nil
}

// Run 运行流水线，直到 ctx 取消或出现无法恢复的错误。返回后所有子进程已停止。
func (p *Pipeline) Run(ctx context.Context) error {
	logger := p.logger
	defer p.asrEngine.Close()

	// ffmpeg 缺失属启动前置条件问题，直接报错而不是无限重试
	if _, err := exec.LookPath(p.cfg.Recorder.FFmpegPath); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败（请确认已安装并加入 PATH）: %w", err)
	}

	dmStarted := false
	startDanmaku := func() {
		if dmStarted || p.dmClient == nil {
			return
		}
		dmStarted = true
		p.setupDanmaku(logger)
		go func() {
			if err := p.dmClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("弹幕监听退出", "err", err)
			}
		}()
	}
	defer func() {
		if dmStarted {
			p.dmClient.Close()
		}
	}()

	for {
		if err := p.waitForLive(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		startDanmaku()

		if err := p.runSession(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("直播会话异常结束，稍后重新检测开播", "err", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		// 会话结束后稍作等待再查开播，避免断流时高频重建
		p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
	}
}

// waitForLive 轮询等待主播开播。
func (p *Pipeline) waitForLive(ctx context.Context) error {
	logger := p.logger
	interval := p.cfg.LiveCheckInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	for {
		st, err := p.bili.GetRoomStatus(p.cfg.RoomID)
		if err != nil {
			logger.Warn("查询开播状态失败", "err", err)
		} else if st.LiveStatus == 1 || st.LiveStatus == 2 {
			// 1=直播中，2=轮播（回放/投稿循环，同样有流可录）
			p.setRoomStatus(st.RoomID, st.Title, st.LiveStatus)
			logger.Info("检测到直播流", "room_id", st.RoomID, "title", st.Title, "live_status", st.LiveStatus)
			return nil
		} else {
			p.setRoomStatus(st.RoomID, st.Title, st.LiveStatus)
			logger.Info("尚未开播，等待中", "room_id", p.cfg.RoomID, "interval", interval)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// finishRecording 结束当前录制会话并产出切片。
func (p *Pipeline) finishRecording() {
	sess := p.recorder.OnStop()
	if sess == nil {
		return
	}
	if sess.Output != "" {
		p.logger.Info("[切片完成] 输出文件", "file", sess.Output,
			"trigger_at", sess.TriggerAt.Format("15:04:05"),
			"duration", sess.StopAt.Sub(sess.TriggerAt).Round(time.Second))
		var size int64
		if info, err := os.Stat(sess.Output); err == nil {
			size = info.Size()
		}
		p.emit(Event{
			Type:    EventClip,
			At:      time.Now(),
			Source:  sess.Source,
			File:    sess.Output,
			Size:    size,
			Message: "切片已产出",
		})
		return
	}
	p.logger.Warn("[切片完成] 未找到可用的缓冲分段", "trigger_at", sess.TriggerAt.Format("15:04:05"))
}

// setupDanmaku 获取弹幕服务器信息（token + 专属地址）并配置客户端。
// B 站公共弹幕服务器已要求 token，须经 getDanmuInfo（wbi 签名）获取。
func (p *Pipeline) setupDanmaku(logger *slog.Logger) {
	if err := p.bili.EnsureBuvid3(); err != nil {
		logger.Warn("获取 buvid3 失败（弹幕连接可能受影响）", "err", err)
	}
	info, err := p.bili.GetDanmuInfo(p.realRoomID())
	if err != nil {
		logger.Warn("获取弹幕服务器信息失败，使用公共服务器（可能连接失败）", "err", err)
		return
	}
	host := info.HostList[0]
	wsURL := fmt.Sprintf("wss://%s:%d/sub", host.Host, host.WSSPort)
	p.dmClient.SetEndpoint(wsURL, info.Token)
	logger.Info("弹幕服务器就绪", "host", host.Host, "port", host.WSSPort)
}

// onDanmakuMessage 弹幕回调：统计关键词密度。
func (p *Pipeline) onDanmakuMessage(msg danmaku.Message) {
	if !p.sessionActive.Load() {
		return
	}
	text := msg.DanmakuText()
	if text == "" {
		return
	}
	now := time.Now()
	counted, dense := p.dmDensity.countHit(text, now)
	if !counted {
		return
	}
	// 录制进行中：命中关键词的弹幕视为持续触发，刷新停止倒计时。
	if p.recorder.State() == recorder.StateRecording {
		p.detector.Touch(now)
	}
	if !dense {
		return
	}
	// 弹幕密度达标：延迟确认后触发，避免误触发。
	// 确认延迟与窗口参数都从当前设置快照读取：它们可被监控面板在线修改。
	settings := p.CurrentSettings()
	delay := settings.Danmaku.ConfirmDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	time.AfterFunc(delay, func() {
		if !p.sessionActive.Load() || !p.dmDensity.stillDense() {
			return
		}
		if p.recorder.State() != recorder.StateIdle {
			return
		}
		at := time.Now()
		p.logger.Info("[触发] 弹幕密度达标", "window_seconds", settings.Danmaku.WindowSeconds)
		p.recorder.OnTrigger(at, "danmaku")
		p.emit(Event{Type: EventTrigger, At: at, Source: "danmaku", Message: "弹幕密度达标"})
		// 弹幕触发的会话同样纳入统一的停止延迟判定
		p.detector.Touch(at)
		p.dmDensity.consume()
	})
}

// currentStreamURL 返回最近一次获取的流地址。
func (p *Pipeline) currentStreamURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streamURL
}

func (p *Pipeline) setStreamURL(u string, refreshed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamURL = u
	if refreshed {
		p.lastRefresh = time.Now()
	}
}

func (p *Pipeline) sleepOrCancel(ctx context.Context, d time.Duration) {
	if d <= 0 {
		d = 5 * time.Second
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
