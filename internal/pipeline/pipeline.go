// Package pipeline 组装整个录制流水线：
//
//	B站流 → 音频采集(ffmpeg) → VAD → 环形缓冲 → ASR → 关键词检测 → 录制控制器
//	   └────────────── 弹幕监听(可选) ──→ 密度触发 ────────────────┘
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Maicarons/hymREC/internal/asr"
	"github.com/Maicarons/hymREC/internal/bili"
	"github.com/Maicarons/hymREC/internal/capture"
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

	roomID        int    // 真实房间号
	lastStreamURL string // 最近一次获取的流地址
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
		dmDensity: newDensityCounter(cfg.Danmaku.WindowSeconds, cfg.Danmaku.DensityThreshold),
	}
	if cfg.Danmaku.Enabled {
		p.dmClient = danmaku.New(danmaku.Options{
			RoomID: cfg.RoomID,
			Logger: logger,
		}, p.onDanmakuMessage)
	}
	return p, nil
}

// RoomID 返回解析后的真实房间号（Run 前为 0）。
func (p *Pipeline) RoomID() int { return p.roomID }

// Run 运行流水线，直到 ctx 取消或出错。返回后所有资源已释放。
func (p *Pipeline) Run(ctx context.Context) error {
	logger := p.logger

	// 1. 等待开播
	if err := p.waitForLive(ctx); err != nil {
		return err
	}

	// 2. 获取流地址
	stream, err := p.bili.GetStreamURL(p.cfg.RoomID, p.cfg.Quality, p.cfg.Platform)
	if err != nil {
		return err
	}
	p.lastStreamURL = stream.URL
	logger.Info("流地址获取成功", "url", stream.URL, "format", stream.Format)

	// 3. 启动视频环形缓冲（TS 分段持续录制）
	if err := p.videoBuf.Start(stream.URL); err != nil {
		return err
	}
	defer p.videoBuf.Stop()

	// 4. 启动弹幕监听（先获取房间专属弹幕服务器与 token）
	if p.dmClient != nil {
		p.setupDanmaku(logger)
		go func() {
			if err := p.dmClient.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("弹幕监听退出", "err", err)
			}
		}()
		defer p.dmClient.Close()
	}

	// 5. 音频采集与识别主循环
	logger.Info("系统启动",
		"room_id", p.cfg.RoomID,
		"stop_delay", p.cfg.Keyword.StopDelaySeconds,
		"pre_roll", p.cfg.RingBufferSeconds,
		"danmaku", p.cfg.Danmaku.Enabled,
		"asr", p.cfg.ASR.Type,
	)
	return p.captureLoop(ctx)
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
			p.roomID = st.RoomID
			logger.Info("检测到直播流", "room_id", st.RoomID, "title", st.Title, "live_status", st.LiveStatus)
			return nil
		} else {
			logger.Info("尚未开播，等待中", "room_id", p.cfg.RoomID, "interval", interval)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// captureLoop 主循环：采集音频 -> VAD -> 缓冲 -> ASR -> 关键词检测 -> 录制控制。
func (p *Pipeline) captureLoop(ctx context.Context) error {
	logger := p.logger
	src, err := capture.New(capture.Options{
		StreamURL:    p.streamURL(),
		FFmpegPath:   p.cfg.Recorder.FFmpegPath,
		SampleRate:   p.cfg.SampleRate,
		ChunkSamples: p.cfg.ChunkSamples(),
	})
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Start(); err != nil {
		return err
	}

	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.finishRecording()
			return nil
		case <-cleanupTicker.C:
			p.videoBuf.Cleanup()
		default:
		}

		pcm, err := src.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				logger.Warn("音频流中断，等待重连...")
				p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
				continue
			}
			// ffmpeg 进程可能因流变化退出，重建
			logger.Warn("读取音频失败，重建采集器", "err", err)
			src.Close()
			time.Sleep(p.cfg.StreamRetryDelay)
			src, err = capture.New(capture.Options{
				StreamURL:    p.streamURL(),
				FFmpegPath:   p.cfg.Recorder.FFmpegPath,
				SampleRate:   p.cfg.SampleRate,
				ChunkSamples: p.cfg.ChunkSamples(),
			})
			if err != nil {
				return err
			}
			if err := src.Start(); err != nil {
				logger.Warn("重建采集器失败", "err", err)
			}
			continue
		}

		now := time.Now()
		p.audioBuf.Write(pcm, now)

		// VAD：静音跳过 ASR（省资源，避免无效关键词扫描）
		if p.cfg.VAD.Enabled && isSilence(pcm, p.cfg.VAD.EnergyThreshold) {
			continue
		}

		text, err := p.asrEngine.ProcessChunk(pcm)
		if err != nil {
			logger.Debug("ASR 调用失败", "err", err)
			continue
		}
		if text == "" {
			continue
		}

		triggered, shouldStop := p.detector.Feed(text, now)
		if triggered {
			p.recorder.OnTrigger(now, "asr")
			logger.Info("[触发] ASR 关键词命中", "text", text)
		}
		if shouldStop {
			p.finishRecording()
		}
	}
}

// finishRecording 结束当前录制会话并产出切片。
func (p *Pipeline) finishRecording() {
	sess := p.recorder.OnStop()
	if sess != nil {
		if sess.Output != "" {
			p.logger.Info("[切片完成] 输出文件", "file", sess.Output,
				"trigger_at", sess.TriggerAt.Format("15:04:05"),
				"duration", sess.StopAt.Sub(sess.TriggerAt).Round(time.Second))
		} else {
			p.logger.Warn("[切片完成] 未找到可用的缓冲分段", "trigger_at", sess.TriggerAt.Format("15:04:05"))
		}
	}
}

// setupDanmaku 获取弹幕服务器信息（token + 专属地址）并配置客户端。
// B 站公共弹幕服务器已要求 token，须经 getDanmuInfo（wbi 签名）获取。
func (p *Pipeline) setupDanmaku(logger *slog.Logger) {
	if err := p.bili.EnsureBuvid3(); err != nil {
		logger.Warn("获取 buvid3 失败（弹幕连接可能受影响）", "err", err)
	}
	info, err := p.bili.GetDanmuInfo(p.roomID)
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
	text := msg.DanmakuText()
	if text == "" {
		return
	}
	now := time.Now()
	if !p.dmDensity.feed(text, now) {
		return
	}
	// 弹幕密度达标：延迟确认后触发，避免误触发
	delay := p.cfg.Danmaku.ConfirmDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	go func() {
		time.Sleep(delay)
		if p.dmDensity.stillDense(now) && p.recorder.State() == recorder.StateIdle {
			p.logger.Info("[触发] 弹幕密度达标", "window_seconds", p.cfg.Danmaku.WindowSeconds)
			p.recorder.OnTrigger(time.Now(), "danmaku")
		}
	}()
}

// streamURL 返回最近一次获取的流地址（重连采集器时使用）。
func (p *Pipeline) streamURL() string {
	return p.lastStreamURL
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

// densityCounter 弹幕关键词密度计数器（滑动窗口）。
type densityCounter struct {
	mu            sync.Mutex
	windowSeconds int
	threshold     int
	keywords      []string
	hits          []time.Time
}

func newDensityCounter(windowSeconds, threshold int) *densityCounter {
	if windowSeconds <= 0 {
		windowSeconds = 30
	}
	if threshold <= 0 {
		threshold = 5
	}
	return &densityCounter{
		windowSeconds: windowSeconds,
		threshold:     threshold,
		keywords:      []string{"星瞳", "瞳", "XT", "小星星"},
	}
}

// feed 计入一条弹幕，返回当前窗口是否达标（达标后窗口计数清零，避免反复触发）。
func (d *densityCounter) feed(text string, at time.Time) bool {
	keywords := d.keywords
	hit := false
	for _, kw := range keywords {
		if containsRune(text, kw) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hits = append(d.hits, at)
	d.trimLocked(at)
	if len(d.hits) >= d.threshold {
		d.hits = nil
		return true
	}
	return false
}

// stillDense 判断在基准时刻之后窗口内是否仍达标（延迟确认用）。
func (d *densityCounter) stillDense(base time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trimLocked(time.Now())
	return len(d.hits) >= d.threshold
}

func (d *densityCounter) trimLocked(at time.Time) {
	cutoff := at.Add(-time.Duration(d.windowSeconds) * time.Second)
	kept := d.hits[:0]
	for _, t := range d.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	d.hits = kept
}

// containsRune 判断字符串是否包含目标子串。
func containsRune(s, substr string) bool {
	rs := []rune(s)
	rt := []rune(substr)
	if len(rt) == 0 || len(rs) < len(rt) {
		return false
	}
	for i := 0; i <= len(rs)-len(rt); i++ {
		ok := true
		for j := range rt {
			if rs[i+j] != rt[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// isSilence 基于 RMS 能量判断静音。
func isSilence(pcm []int16, threshold float64) bool {
	if len(pcm) == 0 {
		return true
	}
	var sum float64
	for _, v := range pcm {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	return rms < threshold
}
