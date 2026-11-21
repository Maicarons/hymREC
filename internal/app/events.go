package app

import (
	"sync"

	"github.com/Maicarons/hymREC/internal/pipeline"
)

const (
	// maxRecentEvents 新订阅者能立即拿到的历史事件数。
	maxRecentEvents = 50
	// eventBufferSize 单个订阅者的缓冲；满了直接丢弃，避免拖慢流水线。
	eventBufferSize = 64
)

// eventHub 向多个监控前端广播流水线事件。
type eventHub struct {
	mu     sync.Mutex
	subs   map[chan pipeline.Event]struct{}
	recent []pipeline.Event
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[chan pipeline.Event]struct{})}
}

// publish 广播事件；订阅者消费不及则丢弃，不阻塞。
func (h *eventHub) publish(ev pipeline.Event) {
	h.mu.Lock()
	h.recent = append(h.recent, ev)
	if len(h.recent) > maxRecentEvents {
		h.recent = h.recent[len(h.recent)-maxRecentEvents:]
	}
	subs := make([]chan pipeline.Event, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// subscribe 返回最近事件、事件通道与取消函数。
func (h *eventHub) subscribe() ([]pipeline.Event, <-chan pipeline.Event, func()) {
	ch := make(chan pipeline.Event, eventBufferSize)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	recent := append([]pipeline.Event(nil), h.recent...)
	h.mu.Unlock()

	return recent, ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}
