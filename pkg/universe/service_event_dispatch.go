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
	payload := ReflectMarshal(call.Payload)
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
