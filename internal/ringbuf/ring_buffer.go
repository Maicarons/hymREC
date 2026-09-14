// Package ringbuf 实现音频环形缓冲：持续写入 PCM 块，
// 保留最近 N 秒的音频与时间戳，供触发后回溯使用。
package ringbuf

import (
	"sync"
	"time"
)

// Chunk 一块带时间戳的音频数据。
type Chunk struct {
	PCM       []int16
	Timestamp time.Time
}

// Buffer 基于块数组的环形缓冲。块长度应固定（= 每块采样点数）。
type Buffer struct {
	mu         sync.Mutex
	maxSeconds int
	sampleRate int
	chunks     []Chunk
	writeIdx   int
	full       bool
}

// New 创建环形缓冲。
// maxSeconds 为保留时长（秒），sampleRate 为采样率（Hz），
// chunkSamples 为每块采样点数（须与写入块一致）。
func New(maxSeconds, sampleRate, chunkSamples int) *Buffer {
	capacity := maxSeconds * sampleRate / chunkSamples
	if capacity < 1 {
		capacity = 1
	}
	return &Buffer{
		maxSeconds: maxSeconds,
		sampleRate: sampleRate,
		chunks:     make([]Chunk, capacity),
	}
}

// Write 写入一块音频。
func (b *Buffer) Write(pcm []int16, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunks[b.writeIdx] = Chunk{PCM: pcm, Timestamp: at}
	b.writeIdx = (b.writeIdx + 1) % len(b.chunks)
	if b.writeIdx == 0 {
		b.full = true
	}
}

// Oldest 返回最旧一块的时间戳；缓冲为空时返回 zero Time。
func (b *Buffer) Oldest() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	oldest, _ := b.rangeLocked()
	return oldest
}

// Newest 返回最新一块的时间戳；缓冲为空时返回 zero Time。
func (b *Buffer) Newest() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, newest := b.rangeLocked()
	return newest
}

// rangeLocked 返回 (最旧, 最新) 时间戳；调用方须已持有锁。
func (b *Buffer) rangeLocked() (oldest, newest time.Time) {
	if !b.full && b.writeIdx == 0 {
		return time.Time{}, time.Time{}
	}
	first := 0
	if b.full {
		first = b.writeIdx
	}
	for i := 0; i < len(b.chunks); i++ {
		c := b.chunks[(first+i)%len(b.chunks)]
		if c.Timestamp.IsZero() {
			continue
		}
		if oldest.IsZero() || c.Timestamp.Before(oldest) {
			oldest = c.Timestamp
		}
		if newest.IsZero() || c.Timestamp.After(newest) {
			newest = c.Timestamp
		}
	}
	return oldest, newest
}

// Len 返回当前已写入的块数。
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.full {
		return len(b.chunks)
	}
	return b.writeIdx
}

// Duration 返回缓冲覆盖的时长（秒）。
func (b *Buffer) Duration() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	oldest, newest := b.rangeLocked()
	if newest.IsZero() {
		return 0
	}
	d := newest.Sub(oldest).Seconds()
	if d < 0 {
		return 0
	}
	return d
}

// ReadFrom 返回 from 之后（含）写入的所有块，按时间先后排序。
func (b *Buffer) ReadFrom(from time.Time) []Chunk {
	b.mu.Lock()
	defer b.mu.Unlock()
	if (!b.full && b.writeIdx == 0) || from.IsZero() {
		return nil
	}
	start := 0
	if b.full {
		start = b.writeIdx
	}
	out := make([]Chunk, 0, len(b.chunks))
	for i := 0; i < len(b.chunks); i++ {
		idx := (start + i) % len(b.chunks)
		c := b.chunks[idx]
		if c.Timestamp.IsZero() {
			continue
		}
		if c.Timestamp.Before(from) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Reset 清空缓冲。
func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.chunks = make([]Chunk, len(b.chunks))
	b.writeIdx = 0
	b.full = false
}
