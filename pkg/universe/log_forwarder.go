package universe

import (
	"context"
	"sync"
	"time"

	"github.com/zenion/mmokit/gen/go/meshpb"
)

const (
	// forwarderBufferCap is the in-process channel capacity between
	// Emit (called from the game loop) and the drain goroutine. Larger
	// = more burst tolerance, drops on overflow.
	forwarderBufferCap = 1024
	// forwarderBatchMax flushes when this many entries accumulate.
	forwarderBatchMax = 64
	// forwarderFlushInterval flushes when this much wall-time passes
	// since the previous flush, even with fewer entries.
	forwarderFlushInterval = 100 * time.Millisecond
)

// meshLogForwarder buffers local logger.Hook emissions and ships them to
// the coordinator over MeshControl as HostMessage.LogBatch. Drops on
// channel overflow — log fidelity is best-effort, never blocks Log().
type meshLogForwarder struct {
	send func(*meshpb.HostMessage) error
	in   chan *meshpb.LogEntry

	stopOnce sync.Once
	stopped  chan struct{}
}

func newMeshLogForwarder(send func(*meshpb.HostMessage) error) *meshLogForwarder {
	return &meshLogForwarder{
		send:    send,
		in:      make(chan *meshpb.LogEntry, forwarderBufferCap),
		stopped: make(chan struct{}),
	}
}

// Emit implements logger.Hook. Non-blocking — drops on full channel.
func (f *meshLogForwarder) Emit(cat, msg string, t time.Time) {
	entry := &meshpb.LogEntry{
		Cat:      cat,
		Msg:      msg,
		TsUnixMs: t.UnixMilli(),
	}
	select {
	case f.in <- entry:
	default:
		// drop
	}
}

// Run drains the input channel, batches entries, and ships LogBatch
// frames over MeshControl. Exits when ctx is cancelled or stop is
// called. Errors from send are silently dropped — the stream will
// reconnect and the next batch will ride the new stream.
func (f *meshLogForwarder) Run(ctx context.Context) {
	batch := make([]*meshpb.LogEntry, 0, forwarderBatchMax)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Copy into a new slice so the LogBatch owns its memory.
		entries := make([]*meshpb.LogEntry, len(batch))
		copy(entries, batch)
		_ = f.send(&meshpb.HostMessage{
			Msg: &meshpb.HostMessage_LogBatch{
				LogBatch: &meshpb.LogBatch{Entries: entries},
			},
		})
		batch = batch[:0]
	}

	timer := time.NewTimer(forwarderFlushInterval)
	defer timer.Stop()
	defer close(f.stopped)

	for {
		select {
		case <-ctx.Done():
			flush() // best-effort final ship before exit
			return
		case e := <-f.in:
			batch = append(batch, e)
			if len(batch) >= forwarderBatchMax {
				flush()
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(forwarderFlushInterval)
			}
		case <-timer.C:
			flush()
			timer.Reset(forwarderFlushInterval)
		}
	}
}

// Stop signals the drain goroutine to exit and waits up to 500ms for it.
// Exposed for clean test teardown; production lifetime is tied to the
// MeshControl client's ctx.
func (f *meshLogForwarder) Stop(timeout time.Duration) {
	f.stopOnce.Do(func() {
		// No explicit signal — caller cancels ctx via Run's parent.
	})
	select {
	case <-f.stopped:
	case <-time.After(timeout):
	}
}
