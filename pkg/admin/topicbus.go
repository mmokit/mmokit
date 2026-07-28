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

// dispatcherJoinTimeout bounds how long Unsubscribe and Close wait for a
// subscriber's dispatcher goroutine to stop. The join exists so a Subscriber is
// never touched after its owner has torn down (see subState.stopped), but a
// Deliver wedged on a dead socket must not pin the caller forever — an HTTP
// handler blocked here holds a connection and a goroutine open.
const dispatcherJoinTimeout = 5 * time.Second

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
	// stopped is closed by the dispatcher goroutine on its way out, after its
	// final Deliver and Close calls have returned. Unsubscribe and TopicBus.Close
	// both wait on it: without that join, a dispatcher can still be inside
	// sseWriter.Deliver — writing and flushing an http.ResponseWriter — after the
	// handler that owns that ResponseWriter has returned, which races net/http's
	// own finishRequest and violates the ResponseWriter contract.
	stopped chan struct{}
	// closeOnce guards done against a self-unsubscribing dispatcher racing the
	// handler's deferred Unsubscribe, and against Unsubscribe racing Close.
	closeOnce sync.Once
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
		topics:  make(map[string]struct{}, len(topics)),
		queue:   make(chan busMessage, b.bufSize),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
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

// Unsubscribe removes s from the bus and blocks until its dispatcher goroutine
// has stopped, so that when it returns s is guaranteed not to be delivered to
// again. Callers rely on that: handleStream unsubscribes from a deferred call,
// and its sseWriter wraps an http.ResponseWriter that becomes illegal to touch
// the moment the handler returns. The wait is bounded by dispatcherJoinTimeout.
func (b *TopicBus) Unsubscribe(s Subscriber) {
	b.unsubscribe(s, true)
}

// unsubscribe is Unsubscribe with the join made optional. The dispatcher itself
// calls it with join=false when Deliver reports the subscriber is finished:
// waiting there would be waiting on its own exit, which deadlocks until the
// timeout expires.
func (b *TopicBus) unsubscribe(s Subscriber, join bool) {
	b.mu.Lock()
	st, ok := b.subscribers[s]
	b.mu.Unlock()
	if !ok {
		// Either s was never subscribed, or its dispatcher already removed
		// itself — which it only does after closing st.stopped, so there is
		// nothing left to wait for either way.
		return
	}
	// The dispatcher, not Unsubscribe, removes the map entry; that ordering is
	// what makes the !ok branch above safe to return from without joining.
	st.closeOnce.Do(func() { close(st.done) })
	if !join {
		return
	}
	select {
	case <-st.stopped:
	case <-time.After(dispatcherJoinTimeout):
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
		// Unsubscribed but not yet reaped by its dispatcher: queueing here would
		// only feed a consumer that is already on its way out.
		select {
		case <-st.done:
			continue
		default:
		}
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

// Close shuts the bus down and, like Unsubscribe, joins every dispatcher before
// returning. Close is the point after which the caller may tear down whatever
// its subscribers write to, so returning while a Deliver is still in flight
// would hand the same use-after-teardown race back to every subscriber at once.
// The joins share a single dispatcherJoinTimeout budget.
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

	for _, st := range subs {
		st.closeOnce.Do(func() { close(st.done) })
	}
	deadline := time.Now().Add(dispatcherJoinTimeout)
	for _, st := range subs {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case <-st.stopped:
		case <-timer.C:
		}
		timer.Stop()
	}
	// Idempotent: each dispatcher also closes its own subscriber on exit. This
	// covers subscribers whose dispatcher blew the join budget.
	for s := range subs {
		s.Close()
	}
}

func (b *TopicBus) dispatcher(s Subscriber, st *subState) {
	defer func() {
		s.Close()
		// Ordering is load-bearing. stopped closes only after the last Deliver
		// and Close have returned, and the map entry is removed only after that,
		// so a concurrent Unsubscribe either finds the entry and waits on
		// stopped, or misses it because everything above already happened.
		close(st.stopped)
		b.mu.Lock()
		delete(b.subscribers, s)
		b.mu.Unlock()
	}()
	for {
		// Give done priority over queued work: an unsubscribing caller is
		// waiting on this goroutine, and a backlog it no longer wants delivered
		// must not extend that wait.
		select {
		case <-st.done:
			return
		default:
		}
		select {
		case <-st.done:
			return
		case msg := <-st.queue:
			if msg.topic == "\x00drain" {
				// Drain rendezvous: close the done channel to unblock Drain().
				close(msg.payload.(chan struct{}))
				continue
			}
			if !s.Deliver(msg.topic, msg.payload, msg.ts) {
				b.unsubscribe(s, false)
				return
			}
		}
	}
}
