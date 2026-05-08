package universe

import (
	"testing"
)

func TestParseRoles_Presets(t *testing.T) {
	for _, s := range []string{"", "all"} {
		r, err := ParseRoles(s)
		if err != nil {
			t.Fatalf("ParseRoles(%q): unexpected error: %v", s, err)
		}
		if !r.Equal(PresetAll()) {
			t.Errorf("ParseRoles(%q) = %v, want %v", s, r, PresetAll())
		}
		if !r.Has(RoleCoordinator) || !r.Has(RoleHost) || !r.Has(RoleGateway) || !r.Has(RoleService) {
			t.Errorf("ParseRoles(%q): missing expected roles in PresetAll", s)
		}
	}
}

func TestParseRoles_SingleRoles(t *testing.T) {
	// coordinator alone
	r, err := ParseRoles("coordinator")
	if err != nil {
		t.Fatalf("ParseRoles(coordinator): %v", err)
	}
	if !r.Has(RoleCoordinator) {
		t.Error("expected RoleCoordinator")
	}
	if r.Has(RoleHost) || r.Has(RoleGateway) {
		t.Error("unexpected extra roles for coordinator-only")
	}

	// gateway alone
	r, err = ParseRoles("gateway")
	if err != nil {
		t.Fatalf("ParseRoles(gateway): %v", err)
	}
	if !r.Has(RoleGateway) {
		t.Error("expected RoleGateway")
	}

	// service alone
	r, err = ParseRoles("service")
	if err != nil {
		t.Fatalf("ParseRoles(service): %v", err)
	}
	if !r.Has(RoleService) {
		t.Error("expected RoleService")
	}
}

// TestParseRoles_BareHostValid verifies that bare `host` is a legal parse
// result. The Build-time check enforces that bare `host` requires
// Config.CoordinatorAddr — ParseRoles itself must not reject.
func TestParseRoles_BareHostValid(t *testing.T) {
	r, err := ParseRoles("host")
	if err != nil {
		t.Fatalf("ParseRoles(host): unexpected error: %v", err)
	}
	if len(r) != 1 || !r.Has(RoleHost) {
		t.Errorf("ParseRoles(host) = %v, want only RoleHost", r)
	}
}

func TestParseRoles_ValidCombinations(t *testing.T) {
	cases := []struct {
		input string
		want  Roles
	}{
		{"coordinator,gateway", Roles{RoleCoordinator: {}, RoleGateway: {}}},
		{"coordinator,host", Roles{RoleCoordinator: {}, RoleHost: {}}},
		{"coordinator,host,gateway", Roles{RoleCoordinator: {}, RoleHost: {}, RoleGateway: {}}},
		{"coordinator,gateway,host", Roles{RoleCoordinator: {}, RoleHost: {}, RoleGateway: {}}},
		{"gateway,coordinator", Roles{RoleCoordinator: {}, RoleGateway: {}}},
		{"host,gateway", Roles{RoleHost: {}, RoleGateway: {}}},
		{"coordinator,host,gateway,service", Roles{RoleCoordinator: {}, RoleHost: {}, RoleGateway: {}, RoleService: {}}},
		{"service,gateway", Roles{RoleService: {}, RoleGateway: {}}},
	}
	for _, c := range cases {
		r, err := ParseRoles(c.input)
		if err != nil {
			t.Errorf("ParseRoles(%q): unexpected error: %v", c.input, err)
			continue
		}
		if !r.Equal(c.want) {
			t.Errorf("ParseRoles(%q) = %v, want %v", c.input, r, c.want)
		}
	}
}

// TestParseRoles_NodeRemoved confirms that `--mode=node` now errors with a
// clear migration hint instead of silently parsing as a role. The old
// `RoleNode` constant was folded into `RoleHost` + `CoordinatorAddr`.
func TestParseRoles_NodeRemoved(t *testing.T) {
	for _, input := range []string{"node", "node,coordinator", "coordinator,node"} {
		_, err := ParseRoles(input)
		if err == nil {
			t.Errorf("ParseRoles(%q): expected error, got nil", input)
			continue
		}
		if !containsSubstr(err.Error(), "removed") || !containsSubstr(err.Error(), "--mode=host") {
			t.Errorf("ParseRoles(%q): error %q missing migration hint", input, err.Error())
		}
	}
}

func TestParseRoles_UnknownToken(t *testing.T) {
	_, err := ParseRoles("leader")
	if err == nil {
		t.Error("ParseRoles(leader): expected error for unknown token")
	}
}

func TestParseRoles_WhitespaceTolerant(t *testing.T) {
	r, err := ParseRoles("  coordinator , gateway  ")
	if err != nil {
		t.Fatalf("ParseRoles with whitespace: %v", err)
	}
	if !r.Has(RoleCoordinator) || !r.Has(RoleGateway) {
		t.Error("expected coordinator+gateway roles")
	}
}

func TestRoles_String(t *testing.T) {
	cases := []struct {
		roles Roles
		want  string
	}{
		{PresetAll(), "coordinator,gateway,host,service"},
		{Roles{RoleCoordinator: {}}, "coordinator"},
		{Roles{RoleHost: {}}, "host"},
		{Roles{RoleCoordinator: {}, RoleGateway: {}}, "coordinator,gateway"},
		{Roles{}, "(empty)"},
	}
	for _, c := range cases {
		if got := c.roles.String(); got != c.want {
			t.Errorf("Roles(%v).String() = %q, want %q", c.roles, got, c.want)
		}
	}
}

// TestCoordinatorBuild_BareHostRequiresCoordAddr verifies that the Build()
// check panics when bare RoleHost has no CoordinatorAddr (the
// previously-rejected "host requires coordinator" ParseRoles case now lives
// here as a Build-time guard).
func TestCoordinatorBuild_BareHostRequiresCoordAddr(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when bare RoleHost has no CoordinatorAddr")
		}
		msg, ok := r.(string)
		if !ok {
			// The panic value may be of another type; check via fmt.Sprint.
			msg = toString(r)
		}
		if !containsSubstr(msg, "--coordinator-addr") {
			t.Errorf("panic message missing migration hint: %q", msg)
		}
	}()

	cfg := Config{
		Mode: "host",
	}
	c := New(cfg)
	c.Build()
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return ""
	}
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
