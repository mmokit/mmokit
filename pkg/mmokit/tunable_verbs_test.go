package mmokit

import "testing"

func TestTuneVerbsRegistered(t *testing.T) {
	proc := New(Config{Name: "tunetest", Headless: true})
	for _, verb := range []string{"tune.list", "tune.get", "tune.set", "tune.reset"} {
		if _, ok := proc.CmdRegistry().Lookup(verb); !ok {
			t.Fatalf("verb %q not registered", verb)
		}
	}
}
