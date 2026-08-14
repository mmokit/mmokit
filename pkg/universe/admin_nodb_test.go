package universe

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingHook records every log line so a test can assert on what an
// operator would actually see, not merely on resulting state.
type capturingHook struct {
	mu    sync.Mutex
	lines []string
}

func (h *capturingHook) Emit(cat, msg string, _ time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, cat+" "+msg)
}

func (h *capturingHook) contains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// TestBuild_AdminWithoutDatabaseDegrades pins the behaviour that makes the
// smallest documented MMOKIT program runnable.
//
// Admin is default-ON: BindFlags binds --admin-enabled to true and
// --admin-listen to ":9101". So a program that never mentions admin still
// enters the admin branch of Build, and before this it panicked there with
// "Admin.Enabled requires a database". That is the exact program at the top of
// pkg/mmokit/README.md, and the only reason nobody hit it is that every recipe
// in this repository passes --postgres-url.
//
// Degrading rather than panicking is the whole fix; the assertion that Build
// still completes is the other half, because an early return here would skip
// debug-command registration, service-kind selection and the coordinator
// service registry that follow in the same function.
func TestBuild_AdminWithoutDatabaseDegrades(t *testing.T) {
	p := New(Config{
		Mode:        "all",
		AdminListen: ":0",
		Headless:    true,
	})
	p.cfg.Admin.Enabled = true
	p.cfg.DBStore = nil

	hook := &capturingHook{}
	p.Log.AddHook(hook)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Build panicked with no database configured: %v", r)
		}
	}()
	p.Build()

	if p.cfg.Admin.Enabled {
		t.Error("Admin.Enabled is still true with no DBStore — the dashboard would mount and fail")
	}
	// Non-vacuous: without this, the test passes even if the branch never runs,
	// because Admin.Enabled defaults to false under `go test` (BindFlags is
	// skipped when flags are already parsed, so the flag's true default never
	// lands). Assert the operator is actually told why the dashboard is gone.
	if !hook.contains("dashboard disabled") {
		t.Errorf("no explanation logged; an operator loses the dashboard silently. lines=%v", hook.lines)
	}
	// Build must have run to completion, not returned early at the admin
	// branch: the coordinator service registry is created after it.
	if p.coordServices == nil {
		t.Error("coordServices is nil — Build returned early instead of falling through")
	}
}

// TestBuild_AdminExplicitlyRequestedWithoutDatabaseStillFails keeps the error
// for an operator who asked for something the process cannot provide. Silently
// dropping a dashboard someone typed a flag for would be worse than a panic.
func TestBuild_AdminExplicitlyRequestedWithoutDatabaseStillFails(t *testing.T) {
	p := New(Config{
		Mode:        "all",
		AdminListen: ":0",
		Headless:    true,
	})
	p.cfg.Admin.Enabled = true
	p.cfg.Admin.EnabledExplicit = true
	p.cfg.DBStore = nil

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic when --admin-enabled was explicit and no database is configured")
		}
		if msg := stringify(r); !strings.Contains(msg, "--admin-enabled requires a database") {
			t.Fatalf("panic message does not explain the problem: %v", r)
		}
	}()
	p.Build()
}

func stringify(v any) string {
	type errish interface{ Error() string }
	if e, ok := v.(errish); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
