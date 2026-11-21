package pipeline

import (
	"math"
	"strings"
	"sync"
	"time"
)

// densityCounter 弹幕关键词密度计数器（滑动窗口）。
// 关键词来自 danmaku.keywords 配置，匹配大小写不敏感。
type densityCounter struct {
	mu        sync.Mutex
	window    time.Duration
	threshold int
	keywords  []string
	hits      []time.Time
}

func newDensityCounter(windowSeconds, threshold int, keywords []string) *densityCounter {
	if windowSeconds <= 0 {
		windowSeconds = 30
	}
	if threshold <= 0 {
		threshold = 5
	}
	return &densityCounter{
		window:    time.Duration(windowSeconds) * time.Second,
		threshold: threshold,
		keywords:  normalizeKeywords(keywords),
	}
}

// normalizeKeywords 去空白并转小写，丢弃空词。
func normalizeKeywords(keywords []string) []string {
	out := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		if kw = strings.ToLower(strings.TrimSpace(kw)); kw != "" {
			out = append(out, kw)
		}
	}
	return out
}

// update 在线替换窗口、阈值与关键词；窗口计数按新参数重新裁剪。
func (d *densityCounter) update(windowSeconds, threshold int, keywords []string) {
	if windowSeconds <= 0 {
		windowSeconds = 30
	}
	if threshold <= 0 {
		threshold = 5
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window = time.Duration(windowSeconds) * time.Second
	d.threshold = threshold
	d.keywords = normalizeKeywords(keywords)
	d.trimLocked(time.Now())
}

// count 返回窗口内当前的命中数。
func (d *densityCounter) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trimLocked(time.Now())
	return len(d.hits)
}

// countHit 计入一条弹幕，返回（是否命中关键词并计入窗口, 当前窗口是否达标）。
// 达标后由调用方在确认通过、真正触发时调用 consume 清空窗口，
// 使延迟确认期间仍能复查窗口密度。
func (d *densityCounter) countHit(text string, at time.Time) (counted, dense bool) {
	if !d.matchKeyword(text) {
		return false, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hits = append(d.hits, at)
	d.trimLocked(at)
	return true, len(d.hits) >= d.threshold
}

// consume 清空窗口：触发后调用，避免同一波弹幕反复触发。
func (d *densityCounter) consume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hits = nil
}

// matchKeyword 判断弹幕是否包含配置的关键词。
func (d *densityCounter) matchKeyword(text string) bool {
	if len(d.keywords) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, kw := range d.keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// stillDense 判断窗口内计数是否仍达标（延迟确认用）。
func (d *densityCounter) stillDense() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trimLocked(time.Now())
	return len(d.hits) >= d.threshold
}

// reset 清空窗口（切换直播会话时使用）。
func (d *densityCounter) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hits = nil
}

func (d *densityCounter) trimLocked(at time.Time) {
	cutoff := at.Add(-d.window)
	kept := d.hits[:0]
	for _, t := range d.hits {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	d.hits = kept
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
