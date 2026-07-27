// Package universe — service_event_dispatch.go bridges the leaf-level
// service.Bus to the universe-side peer-mesh fabric. It registers a
// RemotePublishFunc on the Bus that:
//
//   - reflection-marshals the event payload via universe.ReflectMarshal
//     (the same codec used for typed wire events),
//   - wraps it in a meshpb.ServiceEvent + meshpb.MeshFrame,
//   - dispatches one frame per peer via HostNetwork.SendOrdered (host
//     peers) or SendOrderedToGateway (gateway peers) — the dispatch
//     function locates the right kind from the local PeerList view.
//
// Receive-side decode lives in host_network.go::routeInboundFrame
// (ServiceEvent case added in Task 8).
package universe

import (
	"fmt"
	"sync"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/service"
)

// installServiceEventDispatch wires the universe-side RemotePublishFunc
// into c.bus. Idempotent (last-write-wins on the Bus); call from
// Process.Build (or New) once the Process value is wired enough that
// localHostNetwork() can find a transport at publish time. Lookup is
// lazy, so calling early is safe even if c.gateway / c.Hosts are still
// being populated.
func (c *Process) installServiceEventDispatch() {
	if c.bus == nil {
		return
	}
	c.bus.SetRemotePublish(func(call service.RemotePublishCall) {
		c.dispatchServiceEvent(call)
	})
}

func (c *Process) dispatchServiceEvent(call service.RemotePublishCall) {
	hn := c.localHostNetwork()
	if hn == nil {
		c.Log.Log(CatServicesBus, "service_event: drop %s — no HostNetwork on this process",
			call.TypeName)
		return
	}
	payload, err := ReflectMarshal(call.Payload)
	if err != nil {
		c.Log.Log(CatServicesBus, "service_event: drop %s — encode failed: %v",
			call.TypeName, err)
		return
	}
	frame := &meshpb.MeshFrame{
		Msg: &meshpb.MeshFrame_ServiceEvent{
			ServiceEvent: &meshpb.ServiceEvent{
				SourceProcessId: c.processID(),
				TypeName:        call.TypeName,
				Payload:         payload,
				Sequence:        call.Sequence,
			},
		},
	}
	for _, peerID := range call.PeerIDs {
		if err := c.sendServiceEventToPeer(hn, peerID, frame); err != nil {
			c.Log.Log(CatServicesBus, "service_event %s → %s failed: %v",
				call.TypeName, peerID, err)
		}
	}
}

// sendServiceEventToPeer dispatches frame to peerID over the appropriate
// kind of MeshData stream. Tries host-peer first; falls back to
// gateway-peer if the local peer registry classifies peerID that way.
func (c *Process) sendServiceEventToPeer(hn *HostNetwork, peerID string, frame *meshpb.MeshFrame) error {
	if err := hn.SendOrdered(peerID, frame); err == nil {
		return nil
	}
	if err := hn.SendOrderedToGateway(peerID, frame); err == nil {
		return nil
	}
	return fmt.Errorf("no host or gateway peer named %q", peerID)
}

// localHostNetwork returns the HostNetwork live on this process — gateway,
// in-process host, or service-host. Returns nil if none is wired (e.g. a
// pure coord process with no MeshData listener and no remote subscribers
// to reach).
func (c *Process) localHostNetwork() *HostNetwork {
	if c.gateway != nil && c.gateway.hostNetwork != nil {
		return c.gateway.hostNetwork
	}
	for _, h := range c.Hosts {
		if h != nil && h.Network != nil {
			return h.Network
		}
	}
	return nil
}

// installSubscriptionFlush wires a SubscriptionFlushFunc into c.bus that
// debounces (50ms) and ships a ServiceEventSubscribe HostMessage to coord
// over MeshControl.
//
// Debounce: a service.Init that calls Subscribe[T1], Subscribe[T2], ...
// fires the flush per-call; we want one wire message after the last one
// settles, not N. The debounce window is short enough that no in-flight
// publisher misses a fresh subscriber for long.
func (c *Process) installSubscriptionFlush() {
	if c.bus == nil {
		return
	}
	var (
		mu      sync.Mutex
		timer   *time.Timer
		pending []string
	)
	flushNow := func() {
		mu.Lock()
		set := append([]string(nil), pending...)
		pending = nil
		timer = nil
		mu.Unlock()
		c.sendServiceEventSubscribe(set)
	}
	c.bus.SetSubscriptionFlush(func(typeNames []string) {
		mu.Lock()
		pending = typeNames
		if timer == nil {
			timer = time.AfterFunc(50*time.Millisecond, flushNow)
		}
		mu.Unlock()
	})
}

// sendServiceEventSubscribe ships a HostMessage.ServiceEventSubscribe to
// coord. On a coordinator-bearing process the message is delivered in-
// process (the router's UpdateProcess is called directly + a PeerList
// re-broadcast triggered). Remote-host / standalone-gateway processes
// route via their MeshControl client.
//
// Filters typeNames to wire-eligible types only. Process-local-only
// subscriptions (e.g. AuthLoginSucceededEvent — see Hard prereq #1)
// must not pollute coord's routing table or PeerList broadcasts:
// they're invisible to peerIDsExceptSelf at publish time anyway, so
// announcing them is pure overhead.
func (c *Process) sendServiceEventSubscribe(typeNames []string) {
	wireOnly := make([]string, 0, len(typeNames))
	for _, n := range typeNames {
		if service.IsWireEligible(n) {
			wireOnly = append(wireOnly, n)
		}
	}
	procID := c.processID()
	// In-process path: coord lives here.
	if c.serviceEventRouter != nil && c.assignmentEngine != nil {
		c.serviceEventRouter.UpdateProcess(procID, wireOnly)
		c.broadcastPeerListOnServiceChange()
		return
	}
	// Wire path: send via MeshControl.
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ServiceEventSubscribe{
			ServiceEventSubscribe: &meshpb.ServiceEventSubscribe{
				TypeNames: wireOnly,
			},
		},
	}
	if c.controlClient != nil {
		if err := c.controlClient.send(msg); err != nil {
			c.Log.Log(CatServicesBus, "ServiceEventSubscribe send failed: %v", err)
		}
		return
	}
	if c.gateway != nil && c.gateway.controlClient != nil {
		if err := c.gateway.controlClient.send(msg); err != nil {
			c.Log.Log(CatServicesBus, "ServiceEventSubscribe send (gateway) failed: %v", err)
		}
		return
	}
	c.Log.Log(CatServicesBus, "ServiceEventSubscribe: no controlClient, dropping (procID=%s)", procID)
}

// processID returns the stable per-process identifier used by the Bus
// for self-echo skip + diagnostics. Mirrors processIDFromConfig but is
// safe to call after Build, when gateway IDs may have been generated.
func (c *Process) processID() string {
	if c.cfg.HostID != "" {
		return c.cfg.HostID
	}
	if c.gateway != nil && c.gateway.id != "" {
		return c.gateway.id
	}
	if c.cfg.GatewayID != "" {
		return c.cfg.GatewayID
	}
	return "local"
}
