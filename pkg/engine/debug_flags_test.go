package engine

import (
	"sync"
	"testing"
)

func TestDebugFlag_Bitmask(t *testing.T) {
	var f DebugFlag
	f |= 1 << 0
	f |= 1 << 2
	if f&(1<<0) == 0 {
		t.Errorf("bit 0 should be set")
	}
	if f&(1<<1) != 0 {
		t.Errorf("bit 1 should not be set")
	}
	if f&(1<<2) == 0 {
		t.Errorf("bit 2 should be set")
	}
}

func TestDebugFlagRegistry_RegisterAndLookup(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("test_flag", 5, "test description")
	got, ok := DebugFlagByName("test_flag")
	if !ok {
		t.Fatalf("test_flag not found")
	}
	if got != DebugFlag(1<<5) {
		t.Errorf("got bit %v, want bit 5", got)
	}
	name, ok := DebugFlagName(DebugFlag(1 << 5))
	if !ok {
		t.Fatalf("name lookup failed for bit 5")
	}
	if name != "test_flag" {
		t.Errorf("got name %q, want test_flag", name)
	}
}

func TestDebugFlagRegistry_DoubleRegisterPanics(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("x", 5, "")
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on double-register of bit 5")
		}
	}()
	RegisterDebugFlag("y", 5, "")
}

func TestDebugFlagRegistry_ListDebugFlags(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("a", 0, "")
	RegisterDebugFlag("b", 3, "")
	names := ListDebugFlags()
	if len(names) != 2 {
		t.Errorf("got %d flags, want 2", len(names))
	}
	if names[0] != "a" || names[1] != "b" {
		t.Errorf("unordered list: %v", names)
	}
}

func TestDebugFlagRegistry_Concurrent(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("x", 0, "")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = DebugFlagByName("x")
		}()
	}
	wg.Wait()
}

// TestDebugFlagRegistry_NamesRoundtrip pins the JSONB persistence
// contract: DebugFlagsFromNames(DebugFlagsToNames(f)) == f for any
// f built from registered names. The DB load/save path relies on
// this invariant.
func TestDebugFlagRegistry_NamesRoundtrip(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("topology", 0, "")
	RegisterDebugFlag("perf", 3, "")
	RegisterDebugFlag("net", 7, "")

	cases := []DebugFlag{
		0,
		1 << 0,
		1 << 3,
		1 << 7,
		(1 << 0) | (1 << 3),
		(1 << 0) | (1 << 3) | (1 << 7),
	}
	for _, want := range cases {
		names := DebugFlagsToNames(want)
		got := DebugFlagsFromNames(names)
		if got != want {
			t.Errorf("roundtrip 0b%b: got 0b%b via names %v", want, got, names)
		}
	}
}

// TestDebugFlagRegistry_DoubleRegisterNamePanics covers the second
// panic path — re-registering the same name with a different bit.
func TestDebugFlagRegistry_DoubleRegisterNamePanics(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("dup_name", 5, "")
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on double-register of name dup_name")
		}
	}()
	RegisterDebugFlag("dup_name", 6, "")
}

// TestDebugFlagsFromNames_DropsUnknown verifies the load path is
// resilient to flag names that no longer exist in the registry
// (e.g. a flag was removed between writes).
func TestDebugFlagsFromNames_DropsUnknown(t *testing.T) {
	resetDebugFlagRegistryForTest()
	RegisterDebugFlag("known", 2, "")
	got := DebugFlagsFromNames([]string{"known", "no_such_flag", "another_unknown"})
	want := DebugFlag(1 << 2)
	if got != want {
		t.Errorf("got 0b%b, want 0b%b (unknown names should be dropped)", got, want)
	}
}
