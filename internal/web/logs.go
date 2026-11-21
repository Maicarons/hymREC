package web

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry 一条结构化日志，供监控面板的日志控制台展示。
type LogEntry struct {
	Seq     uint64            `json:"seq"`
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

const maxLogSubscribers = 32

// LogStore 保存最近日志并广播给订阅者（SSE 推送）。
type LogStore struct {
	mu   sync.Mutex
	ring []LogEntry
	seq  uint64
	subs map[chan LogEntry]struct{}
	cap  int
}

// NewLogStore 创建日志存储；capacity 为保留条数。
func NewLogStore(capacity int) *LogStore {
	if capacity <= 0 {
		capacity = 500
	}
	return &LogStore{
		ring: make([]LogEntry, 0, capacity),
		subs: make(map[chan LogEntry]struct{}),
		cap:  capacity,
	}
}

// add 写入一条日志并广播；订阅者消费不及则丢弃。
func (s *LogStore) add(e LogEntry) {
	s.mu.Lock()
	s.seq++
	e.Seq = s.seq
	if len(s.ring) == s.cap {
		copy(s.ring, s.ring[1:])
		s.ring[len(s.ring)-1] = e
	} else {
		s.ring = append(s.ring, e)
	}
	subs := make([]chan LogEntry, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Recent 返回最近 limit 条日志（按时间升序）。
func (s *LogStore) Recent(limit int) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.ring) {
		limit = len(s.ring)
	}
	out := make([]LogEntry, limit)
	copy(out, s.ring[len(s.ring)-limit:])
	return out
}

// Subscribe 订阅新日志，返回通道与取消函数。
func (s *LogStore) Subscribe() (<-chan LogEntry, func()) {
	ch := make(chan LogEntry, 32)
	s.mu.Lock()
	if len(s.subs) < maxLogSubscribers {
		s.subs[ch] = struct{}{}
	}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// Handler 包装终端 handler：本工具支持的日志级别范围内，日志同时进入面板。
func (s *LogStore) Handler(next slog.Handler) slog.Handler {
	return &logHandler{store: s, next: next}
}

// logHandler 把 slog 记录复制一份到 LogStore。
type logHandler struct {
	store *LogStore
	next  slog.Handler
	attrs []slog.Attr
	group []string
}

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	entry := LogEntry{
		Time:    r.Time,
		Level:   levelName(r.Level),
		Message: r.Message,
	}
	attrs := map[string]string{}
	for _, a := range h.attrs {
		attrs[h.key(a.Key)] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[h.key(a.Key)] = a.Value.String()
		return true
	})
	if len(attrs) > 0 {
		entry.Attrs = attrs
	}
	h.store.add(entry)
	return h.next.Handle(ctx, r)
}

func (h *logHandler) WithAttrs(as []slog.Attr) slog.Handler {
	cp := *h
	cp.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &cp
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	cp := *h
	cp.group = append(append([]string(nil), h.group...), name)
	return &cp
}

func (h *logHandler) key(k string) string {
	if len(h.group) == 0 {
		return k
	}
	return strings.Join(h.group, ".") + "." + k
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}
