package admin

import (
	"context"
	"sync"
	"testing"
	"time"
)

type captureSub struct {
	mu     sync.Mutex
	events map[string][]any
}

func (c *captureSub) Topics() []string { return nil }
func (c *captureSub) Deliver(topic string, payload any, _ time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.events == nil {
		c.events = make(map[string][]any)
	}
	c.events[topic] = append(c.events[topic], payload)
	return true
}
func (c *captureSub) Close() {}

func TestCellsPublisher_Tick(t *testing.T) {
	t.Parallel()
	bus := NewTopicBus(0)
	defer bus.Close()
	cap := &captureSub{}
	bus.Subscribe(cap, "cells")

	view := stubView{cells: []CellInfo{{ID: "0_0"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cellsPublisher(ctx, view, bus)

	// Wait for at least one tick (250ms cadence).
	time.Sleep(400 * time.Millisecond)
	cancel()
	bus.Drain()

	cap.mu.Lock()
	got := len(cap.events["cells"])
	cap.mu.Unlock()
	if got < 1 {
		t.Fatalf("expected >=1 cells publish, got %d", got)
	}
}

// stubView satisfies ClusterView with canned data.
type stubView struct {
	cells []CellInfo
}

func (s stubView) Cluster() ClusterInfo                { return ClusterInfo{} }
func (s stubView) Cells() []CellInfo                   { return s.cells }
func (s stubView) Cell(string) (CellInfo, error)       { return CellInfo{}, ErrCellNotFound }
func (s stubView) Hosts() []HostInfo                   { return nil }
func (s stubView) Gateways() []GatewayInfo             { return nil }
func (s stubView) Players(PlayerFilter) []PlayerInfo   { return nil }
func (s stubView) Player(string) (PlayerInfo, error)   { return PlayerInfo{}, ErrPlayerNotFound }
func (s stubView) CommitLog(CommitQuery) []CommitEvent { return nil }
func (s stubView) Perf(string) (PerfSnapshot, error)   { return PerfSnapshot{}, ErrUnavailable }
