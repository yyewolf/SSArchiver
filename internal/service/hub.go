package service

import (
	"sync"
)

// Update is one "something changed" notification for the live UI. It carries
// no payload: subscribers re-read the state they display.
type Update struct {
	PlayerID string // "" for instance-wide changes (worker state, settings)
	Kind     string // sync-event kind that caused the update (model.Kind*)
}

// UpdateBuffer is each subscription's update buffer; a subscriber that falls
// behind loses updates (the UI re-syncs on reconnect and via its slow poll).
const UpdateBuffer = 16

// Subscription receives matching updates until Close.
type Subscription struct {
	C <-chan Update

	sub  *subscriber
	once sync.Once
}

type subscriber struct {
	hub  *hub
	ch   chan Update
	want func(Update) bool
}

// hub is an in-process pub/sub. Publish never blocks: updates are dropped for
// subscribers whose buffer is full.
type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

func newHub() *hub {
	return &hub{subs: make(map[*subscriber]struct{})}
}

// Subscribe returns a subscription receiving the updates want accepts
// (nil matches everything).
func (h *hub) subscribe(want func(Update) bool) *Subscription {
	if want == nil {
		want = func(Update) bool { return true }
	}
	s := &subscriber{hub: h, ch: make(chan Update, UpdateBuffer), want: want}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return &Subscription{C: s.ch, sub: s}
}

func (h *hub) publish(u Update) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if !s.want(u) {
			continue
		}
		select {
		case s.ch <- u:
		default: // slow subscriber: drop
		}
	}
}

func (h *hub) cancel(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
}

// Subscribe registers for live UI updates (nil matches everything).
func (s *Service) Subscribe(want func(Update) bool) *Subscription {
	return s.hub.subscribe(want)
}

// Publish notifies every matching subscriber; it never blocks.
func (s *Service) Publish(u Update) {
	s.hub.publish(u)
}

// Close unsubscribes and closes the update channel. It is safe to call twice.
func (sub *Subscription) Close() {
	sub.once.Do(func() {
		sub.sub.hub.cancel(sub.sub)
		close(sub.sub.ch)
	})
}
