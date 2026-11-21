package pipeline

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Maicarons/hymREC/internal/bili"
	"github.com/Maicarons/hymREC/internal/capture"
)

const (
	// stopCheckInterval 停止判定的检查间隔。
	//
	// 停止判定不依赖 ASR 出字：主播长时间沉默（VAD 判静音或识别为空）时，
	// 若仅在 Feed 内部判定超时，录制将无法结束。
	stopCheckInterval = 5 * time.Second
	// cleanupInterval 视频缓冲清理间隔。
	cleanupInterval = 30 * time.Second
	// healthCheckInterval 会话健康检查（开播状态、视频缓冲）间隔。
	healthCheckInterval = 15 * time.Second
	// segmentStallTimeout 视频缓冲超过该时长没有新分段即视为卡死，重启缓冲。
	segmentStallTimeout = 60 * time.Second
	// streamRefreshMinInterval 两次流地址刷新的最小间隔，避免频繁请求 B 站 API。
	streamRefreshMinInterval = 30 * time.Second
)

// runSession 处理一次直播会话：拉流 → 启动视频缓冲与音频采集 → 识别与触发，
// 直到 ctx 取消、主播下播或流无法恢复。返回时会话内的子进程均已停止，
// 未结束的录制会照常合并输出切片。
func (p *Pipeline) runSession(ctx context.Context) error {
	logger := p.logger

	stream, err := p.bili.GetStreamURL(p.cfg.RoomID, p.cfg.Quality, p.cfg.Platform)
	if err != nil {
		if errors.Is(err, bili.ErrNotLive) {
			return nil // 没有可用流，交给外层重新等待开播
		}
		return err
	}
	p.setStreamURL(stream.URL, true)
	p.setStreamSince(time.Now())
	logger.Info("流地址获取成功", "url", stream.URL, "format", stream.Format)

	if err := p.videoBuf.Start(stream.URL); err != nil {
		return err
	}
	defer p.videoBuf.Stop()

	p.setSession(time.Now(), true)
	p.emit(Event{Type: EventSessionStart, At: time.Now(), Message: "直播会话开始"})
	defer func() {
		p.setSession(time.Time{}, false)
		p.emit(Event{Type: EventSessionEnd, At: time.Now(), Message: "直播会话结束"})
	}()

	// 新会话从干净状态开始：上一场的关键词命中与弹幕计数不应影响本次。
	p.detector.Reset()
	p.dmDensity.reset()

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.watchStream(sessCtx, cancel)
	}()

	err = p.captureLoop(sessCtx)
	if sessCtx.Err() != nil {
		err = nil // 会话被取消（下播或退出）属正常结束
	}
	cancel()
	wg.Wait()

	// 关闭本轮识别：FunASR 需要结束帧才会关闭当前的流式句，
	// 否则下一场直播的首段音频会被接在上一场的残余状态后识别。
	if tail, ferr := p.asrEngine.Flush(); ferr != nil {
		logger.Debug("ASR 收尾失败", "err", ferr)
	} else if tail != "" {
		logger.Info("ASR 收尾文本", "text", tail)
	}

	// 先停缓冲（不再追加分段）再合并，避免读取仍在写入的分段文件
	p.videoBuf.Stop()
	p.finishRecording()
	return err
}

// watchStream 会话看门狗：
//   - 主播下播 → 结束会话，回到等待开播；
//   - 视频缓冲长时间没有新分段（ffmpeg 退出、流地址过期）→ 刷新流地址并重启缓冲。
func (p *Pipeline) watchStream(ctx context.Context, endSession context.CancelFunc) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		st, err := p.bili.GetRoomStatus(p.cfg.RoomID)
		if err != nil {
			p.logger.Warn("查询开播状态失败", "err", err)
		} else if st.LiveStatus == 0 {
			p.setRoomStatus(st.RoomID, st.Title, st.LiveStatus)
			p.logger.Info("主播已下播，结束本次会话")
			endSession()
			return
		}

		if stall := p.videoBuf.StallDuration(); stall >= segmentStallTimeout {
			p.logger.Warn("视频缓冲无新分段，尝试刷新流地址", "stall", stall.Round(time.Second))
			if err := p.restartVideoBuffer(ctx); err != nil {
				p.logger.Warn("重启视频缓冲失败", "err", err)
				p.emit(Event{Type: EventError, At: time.Now(), Message: "重启视频缓冲失败: " + err.Error()})
			}
		}
	}
}

// restartVideoBuffer 刷新流地址并重启视频缓冲。
//
// 直播流地址带有效期，过期后 ffmpeg 进程仍然存活但不再产出分段；
// 重启后新的分段继续写入同一目录，回溯不受影响。
func (p *Pipeline) restartVideoBuffer(ctx context.Context) error {
	if err := p.refreshStream(ctx); err != nil {
		return err
	}
	p.videoBuf.Stop()
	if err := p.videoBuf.Start(p.currentStreamURL()); err != nil {
		return err
	}
	p.setStreamSince(time.Now())
	p.logger.Info("视频缓冲已使用新流地址重启")
	p.emit(Event{Type: EventBufferRestart, At: time.Now(), Message: "视频缓冲已用新流地址重启"})
	return nil
}

// refreshStream 重新获取流地址；距上次刷新不足 streamRefreshMinInterval 时跳过，
// 避免断流重连期间反复请求 B 站 API。
func (p *Pipeline) refreshStream(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	tooSoon := !p.lastRefresh.IsZero() && time.Since(p.lastRefresh) < streamRefreshMinInterval
	if !tooSoon {
		p.lastRefresh = time.Now()
	}
	p.mu.Unlock()
	if tooSoon {
		return nil
	}

	stream, err := p.bili.GetStreamURL(p.cfg.RoomID, p.cfg.Quality, p.cfg.Platform)
	if err != nil {
		return err
	}
	p.setStreamURL(stream.URL, false)
	p.logger.Info("流地址已刷新", "url", stream.URL)
	p.emit(Event{Type: EventStreamRefresh, At: time.Now(), Message: "流地址已刷新"})
	return nil
}

// captureLoop 采集音频并驱动识别与检测，直到 ctx 取消。
//
// 采集器断流（流地址过期、CDN 抖动）时先尝试刷新流地址再重建：
// 每次重建都由新的 Source 持有进程句柄，旧进程立即回收。
func (p *Pipeline) captureLoop(ctx context.Context) error {
	var src *capture.Source
	defer func() {
		if src != nil {
			src.Close()
		}
	}()

	start := func() bool {
		if src != nil {
			src.Close()
			src = nil
		}
		_ = p.refreshStream(ctx) // 限速：首次启动与 30 秒内的重试复用当前地址
		s, err := capture.New(capture.Options{
			StreamURL:    p.currentStreamURL(),
			FFmpegPath:   p.cfg.Recorder.FFmpegPath,
			SampleRate:   p.cfg.SampleRate,
			ChunkSamples: p.cfg.ChunkSamples(),
		})
		if err != nil {
			p.logger.Warn("创建音频采集器失败", "err", err)
			return false
		}
		if err := s.Start(); err != nil {
			p.logger.Warn("启动音频采集失败，稍后重试", "err", err)
			return false
		}
		src = s
		return true
	}

	if !start() {
		p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
	}

	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()
	stopCheckTicker := time.NewTicker(stopCheckInterval)
	defer stopCheckTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cleanupTicker.C:
			p.videoBuf.Cleanup()
		case <-stopCheckTicker.C:
			if p.detector.Tick(time.Now()) {
				p.finishRecording()
			}
		default:
		}

		if src == nil {
			if !start() {
				p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
			}
			continue
		}

		pcm, err := src.Next()
		if err != nil {
			p.logger.Warn("音频流中断，重建采集器", "err", err)
			src.Close()
			src = nil
			p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
			if !start() {
				p.sleepOrCancel(ctx, p.cfg.StreamRetryDelay)
			}
			continue
		}
		p.handleAudio(pcm)
	}
}

// handleAudio 处理一块音频：写环形缓冲 → VAD → ASR → 关键词检测 → 录制控制。
func (p *Pipeline) handleAudio(pcm []int16) {
	now := time.Now()
	p.audioBuf.Write(pcm, now)

	// VAD：静音跳过 ASR（省资源，避免无效关键词扫描）
	if p.cfg.VAD.Enabled && isSilence(pcm, p.cfg.VAD.EnergyThreshold) {
		return
	}

	text, err := p.asrEngine.ProcessChunk(pcm)
	if err != nil {
		p.logger.Debug("ASR 调用失败", "err", err)
		return
	}
	if text == "" {
		return
	}

	p.setLastASR(text, now)
	triggered, shouldStop := p.detector.Feed(text, now)
	if triggered {
		p.recorder.OnTrigger(now, "asr")
		p.logger.Info("[触发] ASR 关键词命中", "text", text)
		p.emit(Event{Type: EventTrigger, At: now, Source: "asr", Message: text})
	}
	if shouldStop {
		p.finishRecording()
	}
}
