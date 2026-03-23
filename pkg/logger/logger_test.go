package logger

import "testing"

func TestNewWithEnabledCategories(t *testing.T) {
	l := New("combat", "mining")

	tests := []struct {
		cat  string
		want bool
	}{
		{"combat", true},
		{"mining", true},
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
	l.RegisterCategories("a", "b", "c")

	cats := l.Categories()
	if len(cats) != 3 {
		t.Fatalf("Categories() len = %d, want 3", len(cats))
	}

	// Deduplication: registering again should not add duplicates.
	l.RegisterCategories("b", "c", "d")
	cats = l.Categories()
	if len(cats) != 4 {
		t.Fatalf("after dedup, Categories() len = %d, want 4", len(cats))
	}
}

func TestEnableDisable(t *testing.T) {
	l := New()

	l.Enable("combat")
	if !l.IsEnabled("combat") {
		t.Fatal("expected combat enabled after Enable")
	}

	l.Disable("combat")
	if l.IsEnabled("combat") {
		t.Fatal("expected combat disabled after Disable")
	}
}

func TestEnableAutoRegisters(t *testing.T) {
	l := New()
	l.Enable("newcat")

	cats := l.Categories()
	found := false
	for _, c := range cats {
		if c == "newcat" {
			found = true
		}
	}
	if !found {
		t.Fatal("Enable should auto-register unknown category")
	}
}

func TestLogDisabledNoOp(t *testing.T) {
	l := New()
	// Should not panic when category is disabled.
	l.Log("disabled", "message %d", 42)
}
