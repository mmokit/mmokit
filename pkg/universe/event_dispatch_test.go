package universe_test

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// mustMarshal is the external-test-package form of ReflectMarshal for values
// expected to fit the wire; the encoder length guards are asserted directly in
// the in-package reflect_marshal_test.go.
func mustMarshal(tb testing.TB, ptr any) []byte {
	tb.Helper()
	data, err := pkguniverse.ReflectMarshal(ptr)
	if err != nil {
		tb.Fatalf("ReflectMarshal(%T): %v", ptr, err)
	}
	return data
}

// mustUnmarshal is its decode-side mirror; the decoder bounds guards are
// likewise asserted directly in the in-package reflect_marshal_bounds_test.go.
func mustUnmarshal(tb testing.TB, data []byte, ptr any) {
	tb.Helper()
	if err := pkguniverse.ReflectUnmarshal(data, ptr); err != nil {
		tb.Fatalf("ReflectUnmarshal(%T): %v", ptr, err)
	}
}

// evDispatchInput is the test message routed through the inbound typed-event
// dispatcher. Two int32 fields → 8-byte body via the reflection codec.
type evDispatchInput struct {
	V int32
	W int32
}

// evDispatchOther is a second registered type used to verify multi-entry
// dispatch decodes each entry against the correct registered Go type.
type evDispatchOther struct {
	X int32
}

// buildTypedEntry packs one [typeID:u32 LE][bodyLen:u32 LE][body] entry.
func buildTypedEntry(typeID uint32, body []byte) []byte {
	out := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(out[0:4], typeID)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(body)))
	copy(out[8:], body)
	return out
}

// dispatchTestProcess builds a 1x1 Headless Process. Caller registers
// HandleClient closures, then calls Build() to materialize the stage.
func dispatchTestProcess(t *testing.T) *pkguniverse.Process {
	t.Helper()
	return mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
}

// TestDispatchInboundEventFrame_SingleEntry pins the happy-path: one entry
// in the payload, the registered handler fires once with the decoded msg.
func TestDispatchInboundEventFrame_SingleEntry(t *testing.T) {
	p := dispatchTestProcess(t)

	var fired struct {
		count   int
		netID   uint32
		v, w    int32
	}
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *evDispatchInput) {
		fired.count++
		fired.netID = player.NetID()
		fired.v, fired.w = msg.V, msg.W
	})

	p.Build()

	stage := singleStage(t, p)

	body := mustMarshal(t, &evDispatchInput{V: 42, W: 7})
	typeID := mmokit.TypeIDOf(reflect.TypeFor[evDispatchInput]())
	payload := buildTypedEntry(typeID, body)

	const playerNetID uint32 = 100
	pkguniverse.DispatchInboundEventFrame(stage, playerNetID, payload)

	if fired.count != 1 {
		t.Fatalf("handler fired %d times, want 1", fired.count)
	}
	if fired.netID != playerNetID {
		t.Errorf("handler player.NetID = %d, want %d", fired.netID, playerNetID)
	}
	if fired.v != 42 || fired.w != 7 {
		t.Errorf("handler msg = (%d,%d), want (42,7)", fired.v, fired.w)
	}
}

// TestDispatchInboundEventFrame_MultiEntry packs two entries (different
// types) into one payload and asserts both handlers fire in order.
func TestDispatchInboundEventFrame_MultiEntry(t *testing.T) {
	p := dispatchTestProcess(t)

	var seq []string

	mmokit.HandleClient(p, func(player mmokit.Entity, msg *evDispatchInput) {
		seq = append(seq, "input")
	})
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *evDispatchOther) {
		seq = append(seq, "other")
	})

	p.Build()

	stage := singleStage(t, p)

	id1 := mmokit.TypeIDOf(reflect.TypeFor[evDispatchInput]())
	id2 := mmokit.TypeIDOf(reflect.TypeFor[evDispatchOther]())

	body1 := mustMarshal(t, &evDispatchInput{V: 1, W: 2})
	body2 := mustMarshal(t, &evDispatchOther{X: 9})

	payload := append(buildTypedEntry(id1, body1), buildTypedEntry(id2, body2)...)

	pkguniverse.DispatchInboundEventFrame(stage, 100, payload)

	if len(seq) != 2 {
		t.Fatalf("handlers fired %d times, want 2: seq=%v", len(seq), seq)
	}
	if seq[0] != "input" || seq[1] != "other" {
		t.Errorf("handler order = %v, want [input other]", seq)
	}
}

// TestDispatchInboundEventFrame_TruncatedBody verifies a payload whose
// declared body_len exceeds the remaining bytes is logged and stops cleanly
// (no panic, no handler fires for the truncated entry).
func TestDispatchInboundEventFrame_TruncatedBody(t *testing.T) {
	p := dispatchTestProcess(t)

	fired := false
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *evDispatchInput) {
		fired = true
	})

	p.Build()
	stage := singleStage(t, p)

	typeID := mmokit.TypeIDOf(reflect.TypeFor[evDispatchInput]())

	// Header declares 100 body bytes; we only supply 4. Must be detected
	// before the read goes out of bounds.
	payload := make([]byte, 8+4)
	binary.LittleEndian.PutUint32(payload[0:4], typeID)
	binary.LittleEndian.PutUint32(payload[4:8], 100)
	// trailing 4 bytes left zero — definitely shorter than declared 100

	// Should not panic.
	pkguniverse.DispatchInboundEventFrame(stage, 100, payload)

	if fired {
		t.Errorf("handler fired despite truncated body")
	}
}

// TestDispatchInboundEventFrame_UnknownTypeID verifies an entry with a
// typeID not in the client-input registry is skipped and the next valid
// entry still dispatches. Confirms the loop is a `continue`, not a `return`.
func TestDispatchInboundEventFrame_UnknownTypeID(t *testing.T) {
	p := dispatchTestProcess(t)

	var fired int
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *evDispatchInput) {
		fired++
	})

	p.Build()
	stage := singleStage(t, p)

	knownID := mmokit.TypeIDOf(reflect.TypeFor[evDispatchInput]())
	knownBody := mustMarshal(t, &evDispatchInput{V: 11, W: 13})

	// Layout: [unknown typeID, len=0, no body][known typeID, body]
	payload := buildTypedEntry(0xDEADBEEF, nil)
	payload = append(payload, buildTypedEntry(knownID, knownBody)...)

	pkguniverse.DispatchInboundEventFrame(stage, 100, payload)

	if fired != 1 {
		t.Errorf("handler fired %d times, want 1 (unknown should skip, known should fire)", fired)
	}
}

// singleStage extracts the single stage from a 1x1 Process.
func singleStage(t *testing.T, p *pkguniverse.Process) *pkguniverse.Stage {
	t.Helper()
	if len(p.Cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(p.Cells))
	}
	for _, c := range p.Cells {
		return c.Stage
	}
	return nil
}
