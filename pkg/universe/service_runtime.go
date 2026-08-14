package universe

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	meshpb "github.com/zenion/mmokit/gen/go/meshpb"
	"github.com/zenion/mmokit/pkg/persist/postgres"
	"github.com/zenion/mmokit/pkg/service"
)

const (
	// serviceShutdownGrace is how long to wait between coordinator
	// unregister + PeerList rebroadcast and the actual Service.Shutdown
	// call so gateways stop routing to us before our handlers go away.
	serviceShutdownGrace = 2 * time.Second

	// serviceShutdownDeadline is the per-instance deadline passed to
	// Service.Shutdown.
	serviceShutdownDeadline = 5 * time.Second
)

// runningService is a Service instance live on this process plus the
// runtime metadata needed to track it (CoordInstance for announcement,
// op-counters wired by Phase 9 metrics auto-wrap).
type runningService struct {
	kind     service.Kind
	svc      service.Service
	instance service.CoordInstance

	// opCount is incremented by the metric wrapper around each handler.
	// Read by `service list` / `service info` console fanout.
	opCount uint64

	// lastError is the last error returned by any handler.
	lastError string
}

// applyServicesToRoutingIndex converts the meshpb.ServiceRecord slice
// from a PeerList broadcast into pkg/service.ServiceRecord and applies
// it to the process-local RoutingIndex. Errors are logged and dropped —
// the coordinator validated at announce time, so any conflict here is
// programmer error.
func (c *Process) applyServicesToRoutingIndex(records []*meshpb.ServiceRecord) {
	if c.serviceRouting == nil {
		return
	}
	out := make([]service.ServiceRecord, 0, len(records))
	for _, r := range records {
		out = append(out, service.ServiceRecord{
			Kind:       r.GetKind(),
			InstanceID: r.GetInstanceId(),
			HostID:     r.GetHostId(),
			OpCodes:    append([]uint32(nil), r.GetOpCodes()...),
		})
	}
	if err := c.serviceRouting.Apply(out); err != nil {
		c.Log.Log(CatMeshCell, "service: PeerList apply failed: %v", err)
	}
}

// serviceContext builds a fresh *service.Context for the given kind +
// instanceID. Reused across Init and any future per-instance hooks.
func (c *Process) serviceContext(kindName, instanceID string) *service.Context {
	return &service.Context{
		KindName:   kindName,
		InstanceID: instanceID,
		Logger:     c.Log,
		DB:         c.serviceDBStore(),
		Roles:      map[string]struct{}(c.roles),
		Bus:        c.bus,
	}
}

// serviceDBStore returns the cluster's *postgres.Store handle threaded
// through Config.DBStore. Service kinds with RequiresDB=true are guaranteed
// non-nil here because Build validates Config.PostgresURL is set.
func (c *Process) serviceDBStore() *postgres.Store {
	return c.cfg.DBStore
}

// localServiceHostID returns the stable host identifier this process
// uses when announcing services. For most setups this is c.cfg.HostID;
// for gateway,service processes (no --host-id) it falls back to the
// gateway's own identifier so service announces are uniquely keyed and
// the coordinator can route service-bound commands back through the
// gateway control stream. In-process all-roles dev servers without
// either ID fall back to "local" so auto-generated instance IDs stay
// readable.
func (c *Process) localServiceHostID() string {
	if c.cfg.HostID != "" {
		return c.cfg.HostID
	}
	// Gateway,service mode: no --host-id, but the gateway has its own
	// identifier. Use it so service announces are uniquely keyed and the
	// coord can route service-bound commands back through the gateway
	// control stream.
	if c.gateway != nil && c.gateway.id != "" {
		return c.gateway.id
	}
	return "local"
}

// startServices is called from Start when the process role set includes
// RoleService. It selects kinds named in cfg.ServiceKinds, instantiates
// each one, runs Init, registers the instances with the coordinator
// (local or remote), and stashes the running instance for shutdown.
//
// Plan 2 Phase 5 retired the legacy code-keyed router; auth + echo (and
// all future services) register typed-op handlers via
// mmokit.RegisterOp[Req, Res] at package init, so there is no per-instance
// op-wiring step here.
func (c *Process) startServices(ctx context.Context) error {
	c.runningServices = map[string]*runningService{}

	hostID := c.localServiceHostID()
	if hostID == "" {
		return fmt.Errorf("startServices: hostID is empty")
	}

	kinds, err := c.services.SelectKinds(c.cfg.ServiceKinds)
	if err != nil {
		return fmt.Errorf("startServices: %w", err)
	}

	for _, k := range kinds {
		// Instance IDs are host-independent: a service migrating between
		// hosts keeps its identity stable in coord-side audit logs even
		// though its CoordInstance.HostID changes. Format: "<kind>-<uuid>"
		// — the kind prefix keeps human grep-ability, the UUID guarantees
		// global uniqueness across multi-host setups (where two hosts may
		// run instances of the same kind simultaneously).
		instanceID := fmt.Sprintf("%s-%s", k.Name, uuid.NewString()[:8])
		svcCtx := c.serviceContext(k.Name, instanceID)

		svc := k.Factory(svcCtx)
		if svc == nil {
			return fmt.Errorf("startServices: kind %q Factory returned nil", k.Name)
		}
		if err := svc.Init(svcCtx); err != nil {
			return fmt.Errorf("startServices: kind %q Init: %w", k.Name, err)
		}

		c.Log.RegisterCategories("services:" + k.Name)
		c.Log.Log("services:"+k.Name, "service %q instance %q started",
			k.Name, instanceID)

		c.runningServices[k.Name] = &runningService{
			kind: k,
			svc:  svc,
			instance: service.CoordInstance{
				Kind:       k.Name,
				InstanceID: instanceID,
				HostID:     hostID,
				OpCodes:    k.OpCodes,
				JoinedAt:   time.Now(),
			},
		}
	}

	// Force-flush the bus subscription set to coord. Init may have
	// registered Subscribe handlers; we want them visible cluster-wide
	// before announceServices triggers the first PeerList rebroadcast.
	if c.bus != nil {
		c.sendServiceEventSubscribe(c.bus.SubscribedTypeNames())
	}

	return c.announceServices()
}

// announceServices posts every running instance to the coordinator's
// service registry (in-process when colocated, MeshControl when remote)
// and triggers a PeerList re-broadcast on success.
func (c *Process) announceServices() error {
	for _, rs := range c.runningServices {
		if c.coordServices != nil {
			if err := c.coordServices.Register(rs.instance); err != nil {
				return fmt.Errorf("announceServices local: %w", err)
			}
		} else {
			if err := c.sendServiceAnnounce(rs.instance); err != nil {
				return fmt.Errorf("announceServices remote: %w", err)
			}
		}
	}
	if c.coordServices != nil {
		c.broadcastPeerListOnServiceChange()
	}
	return nil
}

// sendServiceAnnounce posts a ServiceAnnounce HostMessage to the
// coordinator. Used by service-bearing processes that don't run a
// coordinator locally — either remote hosts (host's mesh-control
// client) or standalone gateways with --mode=gateway,service (the
// gateway's mesh-gateway client). Both client types expose the same
// send(*meshpb.HostMessage) signature.
func (c *Process) sendServiceAnnounce(inst service.CoordInstance) error {
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ServiceAnnounce{
			ServiceAnnounce: &meshpb.ServiceAnnounce{
				Kind:       inst.Kind,
				InstanceId: inst.InstanceID,
				HostId:     inst.HostID,
				OpCodes:    append([]uint32(nil), inst.OpCodes...),
			},
		},
	}
	if c.controlClient != nil {
		return c.controlClient.send(msg)
	}
	if c.gateway != nil && c.gateway.controlClient != nil {
		// The gateway's control client opens its stream asynchronously
		// from controlClient.Start(); send may race the dial when the
		// service announces at startServices time. Brief poll so a slow
		// dial doesn't panic the process; fail fast if the stream
		// genuinely never comes up.
		deadline := time.Now().Add(3 * time.Second)
		for {
			err := c.gateway.controlClient.send(msg)
			if err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("sendServiceAnnounce gateway: %w", err)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return fmt.Errorf("sendServiceAnnounce: no controlClient (need remote-host or standalone-gateway mode)")
}

// sendServiceLeave posts a ServiceLeave HostMessage to the coordinator
// over the MeshControl client.
func (c *Process) sendServiceLeave(instanceID string) error {
	if c.controlClient == nil {
		return fmt.Errorf("sendServiceLeave: no controlClient")
	}
	return c.controlClient.send(&meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ServiceLeave{
			ServiceLeave: &meshpb.ServiceLeave{
				InstanceId: instanceID,
			},
		},
	})
}

// broadcastPeerListOnServiceChange triggers a PeerList re-broadcast.
// Safe to call from any goroutine; no-op if the process has no
// assignmentEngine (i.e. not the coordinator).
func (c *Process) broadcastPeerListOnServiceChange() {
	if c.assignmentEngine == nil {
		return
	}
	c.assignmentEngine.broadcastPeerList()
}

// stopServices is called from Shutdown when this process is exiting. It
// (a) tells the coordinator to stop routing to us by removing our
// instances from the registry, (b) waits a short grace period for the
// PeerList re-broadcast to land at every gateway, then (c) calls
// Service.Shutdown on each instance with a deadline.
func (c *Process) stopServices(ctx context.Context) {
	if len(c.runningServices) == 0 {
		return
	}

	for _, rs := range c.runningServices {
		if c.coordServices != nil {
			c.coordServices.Unregister(rs.instance.InstanceID)
		} else {
			_ = c.sendServiceLeave(rs.instance.InstanceID)
		}
	}
	if c.coordServices != nil {
		c.broadcastPeerListOnServiceChange()
	}

	// Grace for PeerList propagation — gateways stop routing to us.
	// Skip when shutdown ctx is already cancelled to avoid extra delay
	// during forced exits.
	select {
	case <-ctx.Done():
	case <-time.After(serviceShutdownGrace):
	}

	for _, rs := range c.runningServices {
		shutCtx, cancel := context.WithTimeout(context.Background(), serviceShutdownDeadline)
		if err := rs.svc.Shutdown(shutCtx); err != nil {
			c.Log.Log("services:"+rs.kind.Name, "shutdown error: %v", err)
		}
		cancel()
	}
}
