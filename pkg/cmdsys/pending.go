package cmdsys

import (
	"context"
	"fmt"
	"time"
)

// pendingReq tracks an in-flight remote dispatch awaiting a response.
type pendingReq struct {
	cancel context.CancelFunc
	Done   chan struct{}
	result RemoteResponse
	err    error
}

// allocPending registers a new in-flight remote request. Returns its ID.
func (d *Dispatcher) allocPending(cancel context.CancelFunc) uint64 {
	d.mu.Lock()
	id := d.nextID
	d.nextID++
	d.pending[id] = &pendingReq{
		cancel: cancel,
		Done:   make(chan struct{}),
	}
	d.mu.Unlock()
	return id
}

// resolvePending marks a pending request done and removes it from the map.
func (d *Dispatcher) resolvePending(id uint64, resp RemoteResponse, err error) {
	d.mu.Lock()
	p, ok := d.pending[id]
	if ok {
		p.result = resp
		p.err = err
		p.cancel()
		close(p.Done)
		delete(d.pending, id)
	}
	d.mu.Unlock()
}

// janitor evicts entries whose Done channel has been closed without resolution,
// running every 60s and also responding to Close() immediately.
func (d *Dispatcher) janitor() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.closeCh:
			return
		case <-ticker.C:
			d.mu.Lock()
			for id, p := range d.pending {
				select {
				case <-p.Done:
					delete(d.pending, id)
				default:
				}
			}
			d.mu.Unlock()
		}
	}
}

// closeAllPending cancels and drains every pending request. Called by Close.
func (d *Dispatcher) closeAllPending() {
	d.mu.Lock()
	for id, p := range d.pending {
		p.cancel()
		p.err = fmt.Errorf("dispatcher closed")
		close(p.Done)
		delete(d.pending, id)
	}
	d.mu.Unlock()
}
