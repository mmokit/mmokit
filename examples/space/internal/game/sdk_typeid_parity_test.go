package game

import (
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"testing"

	"github.com/mmokit/mmokit"
)

// sdkTypeIDRe matches the "<pkg>.<Type> (typeID 0x<hex>)" annotation cmd/sdkgen
// emits into every generated TypeScript declaration.
var sdkTypeIDRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*) \(typeID (0x[0-9a-f]+)\)`)

// sdkFiles are the generated files that record wire type IDs.
var sdkFiles = []string{
	"../../web/sdk/broadcasts.ts",
	"../../web/sdk/client.ts",
	"../../web/sdk/inputs.ts",
	"../../web/sdk/operations.ts",
}

func fnv32aOf(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// TestSDKTypeIDsMatchGoTypeNames asserts that every wire type ID recorded in
// the committed TypeScript SDK is fnv32a of the Go type name printed beside it.
//
// This is the database-free half of the SDK drift gate. `just client-sdk`
// regenerates the SDK by running this game with --dump-schema, which opens
// PostgreSQL, so neither CI nor a contributor without Docker can run it. This
// test needs nothing but tracked files.
//
// It also pins the property that made relocating this game into examples/
// wire-neutral: mmokit.TypeIDOf hashes reflect.Type.String(), which qualifies
// by package NAME. Every ID here derives from "game.Damage" and friends with
// no import path anywhere, so moving the package could not rotate one. See
// TestTypeIDOf_IgnoresImportPath in mmokit for the mechanism itself.
//
// Scope, stated so a pass is not over-read: this checks the SDK against
// ITSELF — each recorded ID against the name recorded beside it. It catches a
// hand-edited SDK and proves the IDs carry no import path, but on its own it
// would not catch a Go type renamed without regenerating, because a stale SDK
// stays internally consistent. TestSDKServerEventsAreRegistered closes that.
func TestSDKTypeIDsMatchGoTypeNames(t *testing.T) {
	total := 0
	for _, f := range sdkFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		matches := sdkTypeIDRe.FindAllStringSubmatch(string(b), -1)
		if len(matches) == 0 {
			t.Errorf("%s: no typeID annotations found — has the sdkgen comment format changed?", f)
			continue
		}
		for _, m := range matches {
			goName, wantHex := m[1], m[2]
			var want uint32
			if _, err := fmt.Sscanf(wantHex, "0x%x", &want); err != nil {
				t.Fatalf("%s: parse %q: %v", f, wantHex, err)
			}
			if got := fnv32aOf(goName); got != want {
				t.Errorf("%s: %s records typeID %s, but fnv32a(%q) = %#x — "+
					"SDK is stale, run `just client-sdk examples/space`",
					f, goName, wantHex, goName, got)
			}
			total++
		}
	}
	if total == 0 {
		t.Fatal("no typeIDs checked — SDK files missing or the annotation format changed")
	}
	t.Logf("verified %d wire type IDs against their Go type names", total)
}

// TestSDKServerEventsAreRegistered closes the half the parity test cannot: that
// type names baked into the committed SDK still correspond to types this game
// registers. A rename that rotates a wire ID leaves the SDK naming a type the
// registry no longer contains, and this fails.
//
// One direction only, SDK -> registry: broadcasts.ts also carries types owned
// by the broadcast and client-input registries, so an entry this registry does
// not know is not by itself an error.
func TestSDKServerEventsAreRegistered(t *testing.T) {
	// Registration is explicit rather than init-time: RegisterServerEvents is
	// what main.go calls during setup. It is idempotent per registry, so
	// calling it here is safe alongside the package's other tests.
	p := mmokit.New(mmokit.Config{CellsX: 1, CellsY: 1, TickRate: 20, Headless: true})
	RegisterServerEvents(p)

	registered := map[string]bool{}
	for _, ty := range p.Wire().ServerEventTypes() {
		registered[ty.String()] = true
	}
	if len(registered) == 0 {
		t.Fatal("no server event types registered — RegisterServerEvents did nothing, so this proves nothing")
	}

	b, err := os.ReadFile("../../web/sdk/broadcasts.ts")
	if err != nil {
		t.Fatalf("read broadcasts.ts: %v", err)
	}

	matched, unmatched := 0, 0
	for _, m := range sdkTypeIDRe.FindAllStringSubmatch(string(b), -1) {
		if registered[m[1]] {
			matched++
		} else {
			unmatched++
		}
	}
	if matched == 0 {
		t.Errorf("broadcasts.ts names %d types and none is a registered server event — "+
			"the SDK is stale, run `just client-sdk examples/space`", unmatched)
	}
	t.Logf("matched %d of %d SDK entries against %d registered server events",
		matched, matched+unmatched, len(registered))
}
