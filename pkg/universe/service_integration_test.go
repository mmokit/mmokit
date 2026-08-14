package universe

import (
	"context"
	"strings"
	"testing"

	"github.com/mmokit/mmokit/pkg/service"
)

// stubServiceImpl satisfies service.Service for tests that don't need real
// behavior — it counts Init / Shutdown calls. Op codes ride the typed-op
// registry now (Plan 2 Phase 5), so no per-instance op wiring lives here.
type stubServiceImpl struct {
	codes         []uint32
	initCalls     int
	shutdownCalls int
}

func (s *stubServiceImpl) Init(*service.Context) error {
	s.initCalls++
	return nil
}

func (s *stubServiceImpl) Shutdown(context.Context) error {
	s.shutdownCalls++
	return nil
}

func newStubKind(name string, codes ...uint32) (service.Kind, *stubServiceImpl) {
	impl := &stubServiceImpl{codes: codes}
	return service.Kind{
		Name:        name,
		OpCodes:     codes,
		Description: "stub service for tests",
		Factory: func(*service.Context) service.Service {
			return impl
		},
	}, impl
}

// TestRegisterService_BeforeBuild_OK verifies the happy path: register a
// kind, build, the registry contains it.
func TestRegisterService_BeforeBuild_OK(t *testing.T) {
	p := New(Config{Mode: "all"})
	kind, _ := newStubKind("alpha", 1000)
	if err := p.RegisterService(kind); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}
	got, ok := p.services.Get("alpha")
	if !ok {
		t.Fatal("kind 'alpha' not in registry")
	}
	if got.Name != "alpha" || len(got.OpCodes) != 1 {
		t.Fatalf("unexpected kind: %+v", got)
	}
}

// TestRegisterService_AfterBuild_Error confirms registration is rejected
// once Build has run.
func TestRegisterService_AfterBuild_Error(t *testing.T) {
	p := New(Config{Mode: "all"})
	p.Build()
	kind, _ := newStubKind("late", 2000)
	err := p.RegisterService(kind)
	if err == nil || !strings.Contains(err.Error(), "after Build") {
		t.Fatalf("expected post-Build error, got %v", err)
	}
}

// TestRegisterService_Duplicate_Error confirms duplicate name detection.
func TestRegisterService_Duplicate_Error(t *testing.T) {
	p := New(Config{Mode: "all"})
	kind, _ := newStubKind("dup", 3000)
	if err := p.RegisterService(kind); err != nil {
		t.Fatal(err)
	}
	err := p.RegisterService(kind)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

// TestBuild_RoleService_WithoutServicesList_Allowed confirms that having
// RoleService in the role set without --services= is a silent no-op —
// no panic, no instances. "Having the role" means "this binary CAN host
// services"; instantiation is gated by the explicit --services= list.
func TestBuild_RoleService_WithoutServicesList_Allowed(t *testing.T) {
	p := New(Config{
		Mode: "coordinator,host,gateway,service",
	})
	p.Build()
	// Build must succeed; runningServices must be nil/empty.
	if len(p.runningServices) != 0 {
		t.Fatalf("expected no running services, got %d", len(p.runningServices))
	}
}

// TestBuild_ServicesList_RequiresRoleService confirms that --services= is
// rejected when the role set excludes RoleService — almost certainly an
// operator typo. (The inverse — RoleService without --services= — is now
// a silent no-op; see TestBuild_RoleService_WithoutServicesList_Allowed.)
func TestBuild_ServicesList_RequiresRoleService(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if !containsSubstr(toString(r), "RoleService") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	p := New(Config{
		Mode:         "coordinator,host,gateway",
		ServiceKinds: []string{"alpha"},
	})
	p.Build()
}

// TestBuild_RequiresDB_Without_DB confirms the validation: a kind with
// RequiresDB=true and no DB configured fails Build.
func TestBuild_RequiresDB_Without_DB(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if !containsSubstr(toString(r), "requires DB") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	p := New(Config{
		Mode:         "coordinator,host,gateway,service",
		ServiceKinds: []string{"db_kind"},
	})
	_ = p.RegisterService(service.Kind{
		Name:       "db_kind",
		OpCodes:    []uint32{4000},
		Factory:    func(*service.Context) service.Service { return &stubServiceImpl{codes: []uint32{4000}} },
		RequiresDB: true,
	})
	p.Build()
}

// TestBuild_OpCodeOverlap_Rejected confirms two kinds claiming the same
// op code fail Build.
func TestBuild_OpCodeOverlap_Rejected(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if !containsSubstr(toString(r), "code 5000") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	p := New(Config{
		Mode: "all",
	})
	a, _ := newStubKind("a", 5000)
	b, _ := newStubKind("b", 5000)
	_ = p.RegisterService(a)
	_ = p.RegisterService(b)
	p.Build()
}

// TestServiceRouting_PeerListUpdatesIndex verifies that calling
// applyServicesToRoutingIndex on a fresh PeerList propagates correctly.
func TestServiceRouting_PeerListUpdatesIndex(t *testing.T) {
	p := New(Config{Mode: "all"})
	p.Build()
	if p.serviceRouting == nil {
		t.Fatal("serviceRouting is nil")
	}
	if got := p.serviceRouting.LookupKind(7000); got != "" {
		t.Fatalf("want unclaimed before apply, got %q", got)
	}
	// Simulate a PeerList apply via the meshpb-aware helper.
	// Here we exercise the internal path directly.
	recs := []service.ServiceRecord{
		{Kind: "chat", InstanceID: "inst-0", HostID: "host-1", OpCodes: []uint32{7000, 7001}},
	}
	if err := p.serviceRouting.Apply(recs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := p.serviceRouting.LookupKind(7000); got != "chat" {
		t.Fatalf("want chat, got %q", got)
	}
	route, ok := p.serviceRouting.PickInstance("chat", 0)
	if !ok || route.InstanceID != "inst-0" {
		t.Fatalf("want inst-0, got %+v ok=%v", route, ok)
	}
}
