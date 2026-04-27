package universe

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	netpkg "github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/persist/postgres"
	"github.com/zenion/mmoserver/pkg/service"
)

// defaultOpRequestParser decodes the standard enginepb.OperationRequest
// envelope from a channel-0x01 frame's payload bytes. Game code can supply
// its own parser via Config.OpRouter; this is the engine fallback used
// when the engine auto-creates an OpRouter (e.g. for service hosting).
func defaultOpRequestParser(raw []byte) (ops.ParsedRequest, error) {
	var req enginepb.OperationRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		return ops.ParsedRequest{}, err
	}
	return ops.ParsedRequest{Code: req.Code, RequestID: req.RequestId, Data: req.Data}, nil
}

// defaultOpResponseFrameBuilder mirrors mmokit.MakeOpResponse but lives in
// pkg/universe so the auto-created OpRouter doesn't take an mmokit dep.
func defaultOpResponseFrameBuilder(code, reqID uint32, returnCode int32, errorMsg string, payload []byte) []byte {
	resp := &enginepb.OperationResponse{
		Code:       code,
		RequestId:  reqID,
		ReturnCode: returnCode,
		ErrorMsg:   errorMsg,
		Data:       payload,
	}
	respData, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("defaultOpResponseFrameBuilder: marshal: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(respData))
	frame[0] = netpkg.ChannelOperation
	copy(frame[1:], respData)
	return frame
}

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

// snapshotRouterCodes returns the set of op codes currently registered
// on the router as a map for set-difference checks.
func snapshotRouterCodes(r *ops.Router) map[uint32]bool {
	if r == nil {
		return map[uint32]bool{}
	}
	out := map[uint32]bool{}
	for _, c := range r.Codes() {
		out[c] = true
	}
	return out
}

// codesAdded returns the codes present in post but absent in pre.
func codesAdded(pre, post map[uint32]bool) []uint32 {
	out := []uint32{}
	for c := range post {
		if !pre[c] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// equalCodeSet reports whether two slices contain the same op codes,
// ignoring order.
func equalCodeSet(a []uint32, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[uint32]bool{}
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			return false
		}
	}
	return true
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
		SendEvent:  c.serviceSendEvent,
	}
}

// serviceDBStore returns the cluster's *postgres.Store handle threaded
// through Config.DBStore. Service kinds with RequiresDB=true are guaranteed
// non-nil here because Build validates Config.PostgresURL is set.
func (c *Process) serviceDBStore() *postgres.Store {
	return c.cfg.DBStore
}

// serviceSendEvent forwards a server event to a connected client. Goes
// through the engine's ConnManager — the same path cell-originated
// events use. Returns an error if the connection has dropped or if this
// process doesn't have the gateway role (no ConnMgr).
func (c *Process) serviceSendEvent(connID uint32, code uint16, msg proto.Message) error {
	if c.ConnMgr == nil {
		return fmt.Errorf("service.SendEvent: no ConnManager (gateway role required)")
	}
	frame := makeEventFrame(uint32(code), msg)
	if frame == nil {
		return fmt.Errorf("service.SendEvent: failed to build event frame")
	}
	c.ConnMgr.SendReliable(connID, frame)
	return nil
}

// localServiceHostID returns the stable host identifier this process
// uses when announcing services. For most setups this is c.cfg.HostID;
// for in-process all-roles dev servers it falls back to "local" so
// auto-generated instance IDs stay readable.
func (c *Process) localServiceHostID() string {
	if c.cfg.HostID != "" {
		return c.cfg.HostID
	}
	return "local"
}

// startServices is called from Start when the process role set includes
// RoleService. It selects kinds named in cfg.ServiceKinds, instantiates
// each one, runs Init + RegisterOps under op-code cross-check, registers
// the instances with the coordinator (local or remote), and stashes the
// running instance for shutdown.
func (c *Process) startServices(ctx context.Context) error {
	if c.cfg.OpRouter == nil {
		// Should never happen: New auto-creates an OpRouter when one
		// isn't supplied, so this is a programmer-error guard.
		return fmt.Errorf("startServices: Config.OpRouter is unexpectedly nil")
	}

	c.runningServices = map[string]*runningService{}

	hostID := c.localServiceHostID()
	if hostID == "" {
		return fmt.Errorf("startServices: hostID is empty")
	}

	kinds, err := c.services.SelectKinds(c.cfg.ServiceKinds)
	if err != nil {
		return fmt.Errorf("startServices: %w", err)
	}

	for i, k := range kinds {
		instanceID := fmt.Sprintf("%s-%s-%d", hostID, k.Name, i)
		svcCtx := c.serviceContext(k.Name, instanceID)

		svc := k.Factory(svcCtx)
		if svc == nil {
			return fmt.Errorf("startServices: kind %q Factory returned nil", k.Name)
		}
		if err := svc.Init(svcCtx); err != nil {
			return fmt.Errorf("startServices: kind %q Init: %w", k.Name, err)
		}

		preCodes := snapshotRouterCodes(c.cfg.OpRouter)
		if err := svc.RegisterOps(c.cfg.OpRouter); err != nil {
			return fmt.Errorf("startServices: kind %q RegisterOps: %w", k.Name, err)
		}
		postCodes := snapshotRouterCodes(c.cfg.OpRouter)
		newCodes := codesAdded(preCodes, postCodes)
		if !equalCodeSet(newCodes, k.OpCodes) {
			return fmt.Errorf("startServices: kind %q registered codes %v but Kind.OpCodes is %v",
				k.Name, newCodes, k.OpCodes)
		}

		c.Log.RegisterCategories("services:" + k.Name)
		c.Log.Log("services:"+k.Name, "service %q instance %q started (codes=%v)",
			k.Name, instanceID, k.OpCodes)

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
// coordinator over the MeshControl client. Only used by remote
// service-host processes (no local coordinator).
func (c *Process) sendServiceAnnounce(inst service.CoordInstance) error {
	if c.controlClient == nil {
		return fmt.Errorf("sendServiceAnnounce: no controlClient (need remote-host mode)")
	}
	return c.controlClient.send(&meshpb.HostMessage{
		Msg: &meshpb.HostMessage_ServiceAnnounce{
			ServiceAnnounce: &meshpb.ServiceAnnounce{
				Kind:       inst.Kind,
				InstanceId: inst.InstanceID,
				HostId:     inst.HostID,
				OpCodes:    append([]uint32(nil), inst.OpCodes...),
			},
		},
	})
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
