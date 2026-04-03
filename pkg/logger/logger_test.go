package logger

import (
	"regexp"
	"testing"
)

func TestNewWithEnabledCategories(t *testing.T) {
	l := New("combat:hit", "economy:mining")

	tests := []struct {
		cat  string
		want bool
	}{
		{"combat:hit", true},
		{"economy:mining", true},
		{"unknown", false},
	}

	for _, tt := range tests {
		t.Run(tt.cat, func(t *testing.T) {
			if got := l.IsEnabled(tt.cat); got != tt.want {
				t.Errorf("IsEnabled(%q) = %v, want %v", tt.cat, got, tt.want)
			}
		})
	}
}

func TestRegisterCategories(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica", "combat:hit")

	cats := l.Categories()
	if len(cats) != 3 {
		t.Fatalf("Categories() len = %d, want 3", len(cats))
	}

	// Deduplication: registering again should not add duplicates.
	l.RegisterCategories("mesh:replica", "combat:hit", "combat:kill")
	cats = l.Categories()
	if len(cats) != 4 {
		t.Fatalf("after dedup, Categories() len = %d, want 4", len(cats))
	}
}

func TestGroupExtraction(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica", "combat:hit", "standalone")

	groups := l.Groups()
	if len(groups) != 2 {
		t.Fatalf("Groups() len = %d, want 2", len(groups))
	}

	meshCats := l.CategoriesInGroup("mesh")
	if len(meshCats) != 2 {
		t.Fatalf("CategoriesInGroup(mesh) len = %d, want 2", len(meshCats))
	}

	// "standalone" has no colon, should not appear in any group
	standaloneCats := l.CategoriesInGroup("standalone")
	if len(standaloneCats) != 0 {
		t.Fatalf("CategoriesInGroup(standalone) len = %d, want 0", len(standaloneCats))
	}
}

func TestEnableGroupExpansion(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica", "mesh:proxy", "combat:hit")

	l.Enable("mesh")

	for _, cat := range []string{"mesh:transfer", "mesh:replica", "mesh:proxy"} {
		if !l.IsEnabled(cat) {
			t.Errorf("expected %q enabled after Enable(mesh)", cat)
		}
	}
	if l.IsEnabled("combat:hit") {
		t.Error("combat:hit should not be enabled by Enable(mesh)")
	}
}

func TestDisableGroupExpansion(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica", "combat:hit")
	l.Enable("mesh:transfer", "mesh:replica", "combat:hit")

	l.Disable("mesh")

	if l.IsEnabled("mesh:transfer") || l.IsEnabled("mesh:replica") {
		t.Error("mesh subcategories should be disabled after Disable(mesh)")
	}
	if !l.IsEnabled("combat:hit") {
		t.Error("combat:hit should still be enabled after Disable(mesh)")
	}
}

func TestEnableDisableSingleCategory(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica")

	l.Enable("mesh:transfer")
	if !l.IsEnabled("mesh:transfer") {
		t.Fatal("expected mesh:transfer enabled")
	}
	if l.IsEnabled("mesh:replica") {
		t.Fatal("mesh:replica should not be enabled")
	}

	l.Disable("mesh:transfer")
	if l.IsEnabled("mesh:transfer") {
		t.Fatal("expected mesh:transfer disabled")
	}
}

func TestEnableAutoRegisters(t *testing.T) {
	l := New()
	l.Enable("new:cat")

	cats := l.Categories()
	found := false
	for _, c := range cats {
		if c == "new:cat" {
			found = true
		}
	}
	if !found {
		t.Fatal("Enable should auto-register unknown category")
	}

	groups := l.Groups()
	foundGroup := false
	for _, g := range groups {
		if g == "new" {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatal("Enable should auto-register group from new category")
	}
}

func TestLogDisabledNoOp(t *testing.T) {
	l := New()
	// Should not panic when category is disabled.
	l.Log("disabled", "message %d", 42)
}

func TestFilterSubstring(t *testing.T) {
	l := New("economy:loot")
	l.RegisterCategories("economy:loot")

	l.SetFilter("economy:loot", regexp.MustCompile(regexp.QuoteMeta("User1")), "User1")

	// Can't easily capture log output, but verify filter state
	filters := l.Filters()
	if filters["economy:loot"] != "User1" {
		t.Fatalf("expected filter User1, got %q", filters["economy:loot"])
	}
}

func TestFilterGroupExpansion(t *testing.T) {
	l := New()
	l.RegisterCategories("combat:hit", "combat:kill", "combat:ability")

	pat := regexp.MustCompile("player1")
	l.SetFilter("combat", pat, "player1")

	filters := l.Filters()
	for _, cat := range []string{"combat:hit", "combat:kill", "combat:ability"} {
		if filters[cat] != "player1" {
			t.Errorf("expected filter on %s, got %q", cat, filters[cat])
		}
	}
}

func TestClearFilter(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica")

	l.SetFilter("mesh", regexp.MustCompile("node-01"), "node-01")
	l.ClearFilter("mesh:transfer")

	filters := l.Filters()
	if _, ok := filters["mesh:transfer"]; ok {
		t.Error("mesh:transfer filter should be cleared")
	}
	if filters["mesh:replica"] != "node-01" {
		t.Error("mesh:replica filter should still be active")
	}

	// Clear all
	l.ClearFilter()
	if len(l.Filters()) != 0 {
		t.Error("all filters should be cleared")
	}
}

func TestClearFilterGroup(t *testing.T) {
	l := New()
	l.RegisterCategories("mesh:transfer", "mesh:replica", "combat:hit")

	l.SetFilter("mesh", regexp.MustCompile("x"), "x")
	l.SetFilter("combat:hit", regexp.MustCompile("y"), "y")

	l.ClearFilter("mesh")

	filters := l.Filters()
	if len(filters) != 1 {
		t.Fatalf("expected 1 filter remaining, got %d", len(filters))
	}
	if filters["combat:hit"] != "y" {
		t.Error("combat:hit filter should survive mesh group clear")
	}
}
