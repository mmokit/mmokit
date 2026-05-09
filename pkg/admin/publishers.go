package admin

import (
	"context"
	"time"

	"github.com/zenion/mmoserver/pkg/universe"
)

// startPublishers spawns goroutines that fan engine state changes onto the
// TopicBus. They run for the lifetime of the admin Server.
//
// Topic cadences:
//
//	cells    — 4 Hz batched snapshot
//	hosts    — 1 Hz batched snapshot
//	topology — on commit (delegated through commitPublisher)
//	events   — on every CommitLog append (delegated through commitPublisher)
//	alerts   — on invariant violation (delegated through commitPublisher)
func startPublishers(ctx context.Context, p *universe.Process, view ClusterView, bus *TopicBus) {
	go cellsPublisher(ctx, view, bus)
	go hostsPublisher(ctx, view, bus)
	go commitPublisher(ctx, p, bus)
}

func cellsPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("cells", view.Cells())
		}
	}
}

func hostsPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("hosts", view.Hosts())
		}
	}
}

// commitPublisher subscribes to the universe.CommitLog feed and republishes
// to events/topology/alerts according to the event kind. Reuses
// mapCommitEvent from view_local.go for the type conversion.
func commitPublisher(ctx context.Context, p *universe.Process, bus *TopicBus) {
	cl := p.CommitLog()
	if cl == nil {
		return
	}
	feed := cl.Subscribe()
	defer cl.Unsubscribe(feed)
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-feed:
			if !ok {
				return
			}
			payload := mapCommitEvent(raw)
			bus.Publish("events", payload)
			if isTopologyEvent(raw) {
				bus.Publish("topology", payload)
			}
			if isInvariantViolation(raw) {
				bus.Publish("alerts", payload)
			}
		}
	}
}

func isTopologyEvent(ev universe.CommitEvent) bool {
	// Scenario is a typed CommitKind; .String() returns "Split"|"Merge"|"Migrate".
	switch ev.Scenario.String() {
	case "Split", "Merge", "Migrate":
		return ev.Step == "topology-commit" || ev.Step == "release-donors"
	}
	return false
}

func isInvariantViolation(ev universe.CommitEvent) bool {
	return ev.Step == "invariant-violation"
}
