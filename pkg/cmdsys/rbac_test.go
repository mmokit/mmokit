package cmdsys

import "testing"

func TestMatchScore(t *testing.T) {
	cases := []struct {
		pattern string
		cap     string
		want    int
	}{
		{"*.*", "cell.split", 0},
		{"cell.*", "cell.split", 4},
		{"entity.debug.*", "entity.debug.spawn", 12},
		{"entity.*", "entity.debug.spawn", 6},
		{"cell.split", "cell.split", 1_000_000},
		{"cell.split", "cell.merge", -1},
		{"entity.*", "cell.split", -1},
	}
	for _, c := range cases {
		got := matchScore(c.pattern, Capability(c.cap))
		if got != c.want {
			t.Errorf("matchScore(%q, %q) = %d, want %d", c.pattern, c.cap, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		grants []Grant
		cap    Capability
		want   bool
	}{
		{
			"literal beats wildcard deny",
			[]Grant{{"entity.*", true}, {"entity.wipe", false}},
			"entity.wipe", false,
		},
		{
			"*.* allows cell.split",
			[]Grant{{"*.*", true}},
			"cell.split", true,
		},
		{
			"literal deny beats wildcard allow",
			[]Grant{{"cell.*", true}, {"cell.split", false}},
			"cell.split", false,
		},
		{
			"tie breaks toward deny",
			[]Grant{{"test.tp", true}, {"test.tp", false}},
			"test.tp", false,
		},
		{
			"no match denies",
			[]Grant{},
			"cell.split", false,
		},
		{
			"no match denies even with unrelated grants",
			[]Grant{{"cell.*", true}},
			"test.tp", false,
		},
		{
			"longer prefix wildcard beats shorter",
			[]Grant{{"test.*", true}, {"test.debug.*", false}},
			"test.debug.spawn", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := Caller{ID: "test", Grants: tt.grants}
			got := Check(caller, tt.cap)
			if got != tt.want {
				t.Errorf("Check(..., %q) = %v, want %v", tt.cap, got, tt.want)
			}
		})
	}
}

func TestNewOperatorIdentity(t *testing.T) {
	op := NewOperatorIdentity("ops")
	if op.ID != "ops" {
		t.Errorf("ID: got %q want ops", op.ID)
	}
	if len(op.Grants) != 1 || op.Grants[0].Pattern != "*.*" || !op.Grants[0].Allow {
		t.Errorf("operator grants incorrect: %v", op.Grants)
	}
	if !Check(op, "anything.at.all") {
		t.Error("operator identity should allow all capabilities")
	}
}
