package cmdsys

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		line      string
		wantPat   string
		wantAllow bool
		wantErr   bool
	}{
		{"+cell.split", "cell.split", true, false},
		{"-cell.split", "cell.split", false, false},
		{"cell.split", "cell.split", true, false},
		{"entity.*", "entity.*", true, false},
		{"-entity.*", "entity.*", false, false},
		{"*.*", "", false, true},      // always rejected
		{"-*.*", "", false, true},     // even deny form
		{"", "", false, true},         // empty
	}
	for _, c := range cases {
		g, err := Parse(c.line)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): expected error, got nil", c.line)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", c.line, err)
			continue
		}
		if g.Pattern != c.wantPat {
			t.Errorf("Parse(%q): pattern %q want %q", c.line, g.Pattern, c.wantPat)
		}
		if g.Allow != c.wantAllow {
			t.Errorf("Parse(%q): allow %v want %v", c.line, g.Allow, c.wantAllow)
		}
	}
}

func TestParse_GlobalWildcardError(t *testing.T) {
	_, err := Parse("*.*")
	if !errors.Is(err, ErrRefuseGlobalWildcard) {
		t.Errorf("expected ErrRefuseGlobalWildcard, got %v", err)
	}
}

func TestInMemoryGrantStore(t *testing.T) {
	store := NewInMemoryGrantStore()

	grants := []Grant{
		{Pattern: "cell.*", Allow: true},
		{Pattern: "cell.wipe", Allow: false},
	}
	store.Set("user1", grants)

	got := store.Grants("user1")
	if len(got) != 2 {
		t.Fatalf("got %d grants, want 2", len(got))
	}
	if got[0].Pattern != "cell.*" {
		t.Errorf("grant[0] pattern: got %q", got[0].Pattern)
	}

	// mutations to returned slice must not affect stored value
	got[0].Pattern = "tampered"
	stored := store.Grants("user1")
	if stored[0].Pattern == "tampered" {
		t.Error("stored grants were mutated via returned slice")
	}

	// unknown caller returns nil
	if store.Grants("nobody") != nil {
		t.Error("expected nil for unknown caller")
	}
}
