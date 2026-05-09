package admin

import (
	"sync"
	"time"
)

// Subscriber is the contract for any consumer of TopicBus events. Implemented
// by SSE writers (one per HTTP client) and by future remote-stream relays.
type Subscriber interface {
	// Topics returns the set this subscriber receives. Empty slice = wildcard.
	Topics() []string
	// Deliver is called once per matching publish. It must return without
	// blocking; the bus uses bounded per-subscriber channels and drops events
	// (returning false from this method) if the consumer is too slow.
	Deliver(topic string, payload any, ts time.Time) bool
	// Close is called when the subscriber is unregistered.
	Close()
}

// TopicBus is a transport-neutral pub/sub. Publishers call Publish; subscribers
// register via Subscribe. v1 is in-process; v2's remote-admin role re-publishes
// from a MeshControl stream into a process-local bus instance.
type TopicBus struct {
	mu          sync.RWMutex
	subscribers map[Subscriber]*subState
	bufSize     int
	closed      bool
}

type subState struct {
	topics map[string]struct{} // empty == wildcard
	queue  chan busMessage
	done   chan struct{}
}

type busMessage struct {
	topic   string
	payload any
	ts      time.Time
}

// NewTopicBus constructs a bus with the given per-subscriber buffer size
// (default 256 if <=0).
func NewTopicBus(bufSize int) *TopicBus {
	if bufSize <= 0 {
		bufSize = 256
	}
	return &TopicBus{
		subscribers: make(map[Subscriber]*subState),
		bufSize:     bufSize,
	}
}

func (b *TopicBus) Subscribe(s Subscriber, topics ...string) {
	st := &subState{
		topics: make(map[string]struct{}, len(topics)),
		queue:  make(chan busMessage, b.bufSize),
		done:   make(chan struct{}),
	}
	for _, t := range topics {
		st.topics[t] = struct{}{}
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		s.Close()
		return
	}
	b.subscribers[s] = st
	b.mu.Unlock()

	go b.dispatcher(s, st)
}

func (b *TopicBus) Unsubscribe(s Subscriber) {
	b.mu.Lock()
	st, ok := b.subscribers[s]
	if ok {
		delete(b.subscribers, s)
	}
	b.mu.Unlock()
	if ok {
		close(st.done)
	}
}

func (b *TopicBus) Publish(topic string, payload any) {
	msg := busMessage{topic: topic, payload: payload, ts: time.Now()}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, st := range b.subscribers {
		if len(st.topics) > 0 {
			if _, ok := st.topics[topic]; !ok {
				continue
			}
		}
		select {
		case st.queue <- msg:
		default:
			// drop on slow subscriber; surfaces in metrics later
		}
	}
}

// Drain is a test helper that waits for every currently-queued message to
// be delivered. It sends a rendezvous token through each subscriber's queue so
// the caller is guaranteed to observe all prior Deliver calls completing before
// Drain returns. Production code never calls this.
func (b *TopicBus) Drain() {
	b.mu.RLock()
	subs := make([]*subState, 0, len(b.subscribers))
	for _, st := range b.subscribers {
		subs = append(subs, st)
	}
	b.mu.RUnlock()
	for _, st := range subs {
		ack := make(chan struct{})
		// Send a sentinel that the dispatcher will close-ack. Use a select on
		// st.done so we don't deadlock if the subscriber is torn down mid-drain
		// (e.g. from a concurrent Close or Deliver returning false).
		select {
		case st.queue <- busMessage{topic: "\x00drain", payload: ack}:
			select {
			case <-ack:
			case <-st.done:
			}
		case <-st.done:
		}
	}
}

func (b *TopicBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make(map[Subscriber]*subState, len(b.subscribers))
	for s, st := range b.subscribers {
		subs[s] = st
	}
	b.subscribers = nil
	b.mu.Unlock()

	for s, st := range subs {
		close(st.done)
		s.Close()
	}
}

func (b *TopicBus) dispatcher(s Subscriber, st *subState) {
	for {
		select {
		case <-st.done:
			s.Close()
			return
		case msg := <-st.queue:
			if msg.topic == "\x00drain" {
				// Drain rendezvous: close the done channel to unblock Drain().
				close(msg.payload.(chan struct{}))
				continue
			}
			if !s.Deliver(msg.topic, msg.payload, msg.ts) {
				b.Unsubscribe(s)
				return
			}
		}
	}
}
