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
//	hosts    — 1 Hz batched snapshot (shared ticker with gateways + players)
//	gateways — 1 Hz batched snapshot (shared ticker with hosts + players)
//	players  — 1 Hz batched snapshot (shared ticker with hosts + gateways)
//	topology — on commit (delegated through commitPublisher)
//	events   — on every CommitLog append (delegated through commitPublisher)
//	alerts   — on invariant violation (delegated through commitPublisher)
func startPublishers(ctx context.Context, p *universe.Process, view ClusterView, bus *TopicBus) {
	go cellsPublisher(ctx, view, bus)
	go rosterPublisher(ctx, view, bus)
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

// rosterPublisher fans the host roster, gateway roster, and player roster
// onto the bus at 1 Hz on a single ticker. These three topics share a cadence
// because the underlying state changes are infrequent (login/logout/cell-
// migration) — a per-second snapshot is plenty to keep tables in sync.
func rosterPublisher(ctx context.Context, view ClusterView, bus *TopicBus) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			bus.Publish("hosts", view.Hosts())
			bus.Publish("gateways", view.Gateways())
			bus.Publish("players", view.Players(PlayerFilter{Limit: 1000}))
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

// isTopologyEvent fires the "topology" SSE topic for any commit step that
// finalizes ownership change. The hosts/cells maps are stable after these
// steps fire, so the dashboard can rerender ownership tiles. Step names
// match PlanStep.Name in commit_builders_{split,merge,migrate}.go.
func isTopologyEvent(ev universe.CommitEvent) bool {
	if ev.Kind != universe.EventCommitSplit &&
		ev.Kind != universe.EventCommitMerge &&
		ev.Kind != universe.EventCommitMigrate {
		return false
	}
	switch ev.Step {
	case "release-parent-host", // split: parent retired
		"release-donors",      // merge: donor cells retired
		"release-src-host",    // migrate: source host released
		"broadcast-peer-list": // post-finalize: PeerList shipped to all peers
		return true
	}
	return false
}

func isInvariantViolation(ev universe.CommitEvent) bool {
	return ev.Kind == universe.EventInvariantViolation
}
