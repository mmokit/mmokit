package admin

import (
	"context"
	"time"
)

// logPump is the bridge from logger.Hook → LogRing + TopicBus. Emit is
// O(1): append to the ring (locked) and non-blocking-send to the pump
// channel. The drain goroutine handles bus publication.
type logPump struct {
	ring   *LogRing
	bus    *TopicBus
	ch     chan LogEntry
	hostID string
}

// pumpChannelCap is the in-process pump's buffer between Emit and the
// bus-publish goroutine. Larger = better burst tolerance, more memory.
// A burst that fills this drops entries at the channel; the ring still
// has them (the SSE consumer won't see them until they re-emerge from a
// /admin/api/logs/recent fetch).
const pumpChannelCap = 1024

// newLogPump builds an in-process Hook+pump. hostID is the label
// stamped on every entry — typically the coordinator's resolved
// process ID (cfg.HostID, or "coordinator"/"local" as fallback).
// Empty hostID falls back to "local".
func newLogPump(ring *LogRing, bus *TopicBus, hostID string) *logPump {
	if hostID == "" {
		hostID = "local"
	}
	return &logPump{
		ring:   ring,
		bus:    bus,
		ch:     make(chan LogEntry, pumpChannelCap),
		hostID: hostID,
	}
}

// Emit implements logger.Hook. Always non-blocking — drops on full
// channel rather than stalling the game loop.
func (p *logPump) Emit(cat, msg string, t time.Time) {
	e := LogEntry{Host: p.hostID, Cat: cat, Msg: msg, T: t}
	p.ring.Append(e)
	select {
	case p.ch <- e:
	default:
		// drop — pump goroutine is behind. The ring still has the entry,
		// so /admin/api/logs/recent picks it up on the next poll.
	}
}

// Run drains the channel and publishes each entry to the bus until ctx
// cancels. Called from a goroutine in NewServer.
func (p *logPump) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-p.ch:
			p.bus.Publish("logs", e)
		}
	}
}
