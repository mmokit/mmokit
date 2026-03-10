package persist

import (
	"log"
	"sync"
)

// AsyncWriter wraps a Store for non-blocking writes from the game loop.
type AsyncWriter struct {
	store Store
	ops   chan Op
	wg    sync.WaitGroup
}

// NewAsyncWriter creates an AsyncWriter with the given channel buffer size.
func NewAsyncWriter(store Store, bufSize int) *AsyncWriter {
	return &AsyncWriter{
		store: store,
		ops:   make(chan Op, bufSize),
	}
}

// Start launches the background goroutine that processes write operations.
func (w *AsyncWriter) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for op := range w.ops {
			var err error
			if op.Value == nil {
				err = w.store.Delete(op.Collection, op.Key)
			} else {
				err = w.store.Put(op.Collection, op.Key, op.Value)
			}
			if err != nil {
				log.Printf("persist: write error collection=%s key=%s: %v", op.Collection, op.Key, err)
			}
		}
	}()
}

// Enqueue adds a write operation to the background queue.
// Non-blocking — drops the op with a warning if the channel is full.
func (w *AsyncWriter) Enqueue(op Op) {
	select {
	case w.ops <- op:
	default:
		log.Printf("persist: write queue full, dropping op collection=%s key=%s", op.Collection, op.Key)
	}
}

// Flush closes the channel and blocks until all pending ops are processed.
// Call only during shutdown.
func (w *AsyncWriter) Flush() {
	close(w.ops)
	w.wg.Wait()
}
