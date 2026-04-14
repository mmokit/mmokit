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
		if r != PresetAll {
			t.Errorf("ParseRoles(%q) = %v, want %v", s, r, PresetAll)
		}
		if !r.Has(RoleCoordinator) || !r.Has(RoleHost) || !r.Has(RoleGateway) {
			t.Errorf("ParseRoles(%q): missing expected roles", s)
		}
	}
}

func TestParseRoles_SingleRoles(t *testing.T) {
	cases := []struct {
		input string
		role  Role
	}{
		{"coordinator", RoleCoordinator},
		{"host", RoleHost},  // invalid combination — host needs coordinator; test below
		{"gateway", RoleGateway},
		{"node", RoleNode},
	}

	// coordinator alone
	r, err := ParseRoles("coordinator")
	if err != nil {
		t.Fatalf("ParseRoles(coordinator): %v", err)
	}
	if !r.Has(RoleCoordinator) {
		t.Error("expected RoleCoordinator")
	}
	if r.Has(RoleHost) || r.Has(RoleGateway) || r.Has(RoleNode) {
		t.Error("unexpected extra roles for coordinator-only")
	}
	_ = cases

	// gateway alone
	r, err = ParseRoles("gateway")
	if err != nil {
		t.Fatalf("ParseRoles(gateway): %v", err)
	}
	if !r.Has(RoleGateway) {
		t.Error("expected RoleGateway")
	}

	// node alone
	r, err = ParseRoles("node")
	if err != nil {
		t.Fatalf("ParseRoles(node): %v", err)
	}
	if !r.Has(RoleNode) {
		t.Error("expected RoleNode")
	}
}

func TestParseRoles_ValidCombinations(t *testing.T) {
	cases := []struct {
		input string
		want  Roles
	}{
		{"coordinator,gateway", Roles(RoleCoordinator | RoleGateway)},
		{"coordinator,host", Roles(RoleCoordinator | RoleHost)},
		{"coordinator,host,gateway", Roles(RoleCoordinator | RoleHost | RoleGateway)},
		{"coordinator,gateway,host", Roles(RoleCoordinator | RoleHost | RoleGateway)},
		{"gateway,coordinator", Roles(RoleCoordinator | RoleGateway)},
	}
	for _, c := range cases {
		r, err := ParseRoles(c.input)
		if err != nil {
			t.Errorf("ParseRoles(%q): unexpected error: %v", c.input, err)
			continue
		}
		if r != c.want {
			t.Errorf("ParseRoles(%q) = %v, want %v", c.input, r, c.want)
		}
	}
}

func TestParseRoles_InvalidCombinations(t *testing.T) {
	cases := []struct {
		input     string
		errSubstr string
	}{
		{"node,coordinator", "cannot combine"},
		{"node,gateway", "cannot combine"},
		{"node,host", "cannot combine"},
		{"host", "requires"},
		{"host,gateway", "requires"},
	}
	for _, c := range cases {
		_, err := ParseRoles(c.input)
		if err == nil {
			t.Errorf("ParseRoles(%q): expected error, got nil", c.input)
			continue
		}
		if !containsSubstr(err.Error(), c.errSubstr) {
			t.Errorf("ParseRoles(%q): error %q does not contain %q", c.input, err.Error(), c.errSubstr)
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
		{PresetAll, "coordinator,host,gateway"},
		{Roles(RoleCoordinator), "coordinator"},
		{Roles(RoleNode), "node"},
		{Roles(RoleCoordinator | RoleGateway), "coordinator,gateway"},
		{Roles(0), "(empty)"},
	}
	for _, c := range cases {
		if got := c.roles.String(); got != c.want {
			t.Errorf("Roles(%d).String() = %q, want %q", c.roles, got, c.want)
		}
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
