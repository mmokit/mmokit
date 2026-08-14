package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/logger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// admissionFixture builds a bare meshControlServer with real registries. It
// deliberately avoids a whole Process: admission is a pure predicate over the
// stream maps and the registry, and standing up coordinators would bind ports.
func admissionFixture(t *testing.T) *meshControlServer {
	t.Helper()
	log := logger.New()
	return &meshControlServer{
		log:             log,
		registry:        NewHostRegistry(log),
		gatewayRegistry: NewGatewayRegistry(log),
		streams:         make(map[string]*controlStream),
		gatewayStreams:  make(map[string]*controlStream),
	}
}

// installHostStream fakes a live host control stream for id.
func installHostStream(s *meshControlServer, id string) *controlStream {
	cs := &controlStream{kill: make(chan struct{}), gen: s.streamSeq.Add(1)}
	s.mu.Lock()
	s.streams[id] = cs
	s.mu.Unlock()
	return cs
}

func installGatewayStream(s *meshControlServer, id string) *controlStream {
	cs := &controlStream{kill: make(chan struct{}), gen: s.streamSeq.Add(1)}
	s.mu.Lock()
	s.gatewayStreams[id] = cs
	s.mu.Unlock()
	return cs
}

func wantRejected(t *testing.T, err error, why string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected rejection (%s), got admission", why)
	}
	if got := status.Code(err); got != codes.AlreadyExists {
		t.Fatalf("status code = %v, want AlreadyExists (%s)", got, why)
	}
}

func wantAdmitted(t *testing.T, err error, why string) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected admission (%s), got %v", why, err)
	}
}

// TestAdmission_LiveIncumbentIsDefended is the case the guard exists for: two
// processes sharing a --host-id used to flap forever, each evicting the other
// every backoff interval.
func TestAdmission_LiveIncumbentIsDefended(t *testing.T) {
	s := admissionFixture(t)
	installHostStream(s, "host-a")
	s.registry.Register("host-a", "127.0.0.1:1111", false, false)
	s.registry.Touch("host-a")

	wantRejected(t, s.admitHostRegistration("host-a"), "incumbent is heartbeating")

	if got := s.registry.Get("host-a").GrpcAddr; got != "127.0.0.1:1111" {
		t.Fatalf("rejected registration mutated GrpcAddr to %q", got)
	}
}

// TestAdmission_StaleIncumbentIsReplacedAndAddressRefreshes pins the half of
// the criterion that was originally worded backwards. Every production
// NewHostNetwork binds ":0", so a restarted host advertises a NEW address;
// freezing GrpcAddr would leave it unreachable on the payload plane while its
// control stream looked healthy. The assertion is that the address DOES change.
func TestAdmission_StaleIncumbentIsReplacedAndAddressRefreshes(t *testing.T) {
	s := admissionFixture(t)
	installHostStream(s, "host-a")
	s.registry.Register("host-a", "127.0.0.1:1111", false, false)

	// Age the incumbent past deadThreshold.
	s.registry.mu.Lock()
	s.registry.hosts["host-a"].LastHeartbeat = time.Now().Add(-2 * deadThreshold)
	s.registry.mu.Unlock()

	wantAdmitted(t, s.admitHostRegistration("host-a"), "incumbent went silent past deadThreshold")

	s.registry.Register("host-a", "127.0.0.1:2222", false, false)
	if got := s.registry.Get("host-a").GrpcAddr; got != "127.0.0.1:2222" {
		t.Fatalf("GrpcAddr = %q, want the restarted host's new ephemeral port", got)
	}
}

// TestAdmission_KilledIncumbentIsNotDefended pins the killClosed term.
//
// cancelStream closes the kill channel but leaves the map entry in place until
// the handler's defer runs. The killed host redials after ~200ms, far inside
// deadThreshold, so a predicate looking only at map presence plus heartbeat
// freshness would reject it — turning `host kill`'s documented ~3s
// reassignment into a lockout.
func TestAdmission_KilledIncumbentIsNotDefended(t *testing.T) {
	s := admissionFixture(t)
	installHostStream(s, "host-a")
	s.registry.Register("host-a", "127.0.0.1:1111", false, false)
	s.registry.Touch("host-a") // fresh heartbeat: only killClosed can admit

	if !s.cancelStream("host-a") {
		t.Fatal("cancelStream reported no stream to cancel")
	}

	wantAdmitted(t, s.admitHostRegistration("host-a"), "operator killed the incumbent")
}

// TestAdmission_WedgedRegisteredHostIsAdmitted pins why State != Dead is
// combined with staleness rather than used alone. checkLiveness requires
// State == Live before it tests staleness, so a host that registers and never
// heartbeats stays Registered forever and is never marked Dead. A rule phrased
// "admit only when the incumbent is Dead" would lock that ID out permanently.
func TestAdmission_WedgedRegisteredHostIsAdmitted(t *testing.T) {
	s := admissionFixture(t)
	installHostStream(s, "host-a")
	s.registry.Register("host-a", "127.0.0.1:1111", false, false)

	s.registry.mu.Lock()
	s.registry.hosts["host-a"].State = RemoteHostRegistered
	s.registry.hosts["host-a"].LastHeartbeat = time.Now().Add(-2 * deadThreshold)
	s.registry.mu.Unlock()

	wantAdmitted(t, s.admitHostRegistration("host-a"), "host wedged in Registered, never heartbeated")
}

// TestAdmission_LocalEntriesAreNeverHandedOver pins the unconditional Local
// clause. checkLiveness skips Local hosts BEFORE the staleness test and Touch
// is unreachable without a control stream, so a Local entry's LastHeartbeat is
// frozen at RegisterLocal time and reads stale forever. A staleness-only rule
// would hand the well-known IDs "local" and "inproc" to any caller — and the
// test asserts that at both zero and simulated-long uptime.
func TestAdmission_LocalEntriesAreNeverHandedOver(t *testing.T) {
	s := admissionFixture(t)
	s.registry.RegisterLocal("local", "", nil, false)

	wantRejected(t, s.admitHostRegistration("local"), "local entry, fresh process")

	s.registry.mu.Lock()
	s.registry.hosts["local"].LastHeartbeat = time.Now().Add(-60 * time.Second)
	s.registry.mu.Unlock()

	wantRejected(t, s.admitHostRegistration("local"), "local entry, 60s uptime")
}

// TestAdmission_CrossMapCollisionIsRejected pins clause (b).
// sendCoordMessageToHost falls back to gatewayStreams when a host ID has no
// host stream, so a guard consulting only its own map is bypassable by
// registering a host under a live gateway's ID.
func TestAdmission_CrossMapCollisionIsRejected(t *testing.T) {
	s := admissionFixture(t)
	installGatewayStream(s, "shared-id")

	wantRejected(t, s.admitHostRegistration("shared-id"), "id held by a live gateway stream")

	s2 := admissionFixture(t)
	installHostStream(s2, "shared-id")
	wantRejected(t, s2.admitGatewayRegistration("shared-id"), "id held by a live host stream")
}

// TestAdmission_UnknownIDIsAdmitted is the baseline: a first-time registration
// must sail through, or the guard has broken cluster formation outright.
func TestAdmission_UnknownIDIsAdmitted(t *testing.T) {
	s := admissionFixture(t)
	wantAdmitted(t, s.admitHostRegistration("brand-new"), "no incumbent at all")
	wantAdmitted(t, s.admitGatewayRegistration("brand-new-gw"), "no incumbent at all")
}

// TestAdmission_GatewayLiveIncumbentIsDefended mirrors the host case on the
// gateway side, which uses gatewayDeadThreshold so the guard and
// checkGatewayLiveness agree on what "still alive" means.
func TestAdmission_GatewayLiveIncumbentIsDefended(t *testing.T) {
	s := admissionFixture(t)
	installGatewayStream(s, "gw-1")
	s.gatewayRegistry.Register("gw-1", "127.0.0.1:8080", "127.0.0.1:9090")
	s.gatewayRegistry.Touch("gw-1")

	wantRejected(t, s.admitGatewayRegistration("gw-1"), "gateway incumbent is heartbeating")

	if !s.cancelGatewayStream("gw-1") {
		t.Fatal("cancelGatewayStream reported no stream to cancel")
	}
	wantAdmitted(t, s.admitGatewayRegistration("gw-1"), "operator killed the gateway stream")
}
