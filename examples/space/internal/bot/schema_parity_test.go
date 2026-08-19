package bot

import (
	"encoding/json"
	"os"
	"testing"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/quantize"
)

// kindName resolves a kind byte to its schema name, for readable failures.
func kindName(doc schemaDoc, kind uint8) string {
	for _, e := range doc.Entities {
		if e.Kind == kind {
			return e.Name
		}
	}
	return "?"
}

// The schema golden, read as data. Only the pieces this test needs.
type schemaDoc struct {
	Entities []struct {
		Kind     uint8  `json:"kind"`
		Name     string `json:"name"`
		Bindings []struct {
			Fields []struct {
				Name     string `json:"name"`
				Encoding string `json:"encoding"`
				Size     int    `json:"size"`
				Initial  bool   `json:"initial"`
			} `json:"fields"`
		} `json:"bindings"`
	} `json:"entities"`
}

// loadSchemaDoc reads the committed schema golden — the same document
// --dump-schema emits and CI byte-compares.
func loadSchemaDoc(t *testing.T) schemaDoc {
	t.Helper()
	raw, err := os.ReadFile("../../../../testdata/schema/space.json")
	if err != nil {
		t.Fatalf("read schema golden: %v (this is the only cross-check the bot has)", err)
	}
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema golden: %v", err)
	}
	return doc
}

// schemaSnapshotWidth returns a kind's per-tick snapshot width from the golden,
// for fixtures that need a correctly-sized buffer. Hard-coding these is how the
// old ones came to encode a layout the server had not emitted for a long time.
func schemaSnapshotWidth(t *testing.T, kind uint8) int {
	t.Helper()
	w, ok := snapshotWidth(t, loadSchemaDoc(t), kind)
	if !ok {
		t.Fatalf("kind %d absent from the schema golden", kind)
	}
	return w
}

// snapshotWidth returns the fixed byte width of a kind's per-tick snapshot
// stream, which is what decodeSnapshot walks. Initial fields live in a separate
// buffer and are excluded.
func snapshotWidth(t *testing.T, doc schemaDoc, kind uint8) (int, bool) {
	t.Helper()
	for _, e := range doc.Entities {
		if e.Kind != kind {
			continue
		}
		total := 0
		for _, b := range e.Bindings {
			for _, f := range b.Fields {
				if f.Initial {
					continue
				}
				if f.Size < 0 {
					t.Fatalf("kind %d field %q has variable width in the fixed stream", kind, f.Name)
				}
				total += f.Size
			}
		}
		return total, true
	}
	return 0, false
}

// decodeSnapshot is hand-rolled against a layout the server generates, so the
// only thing keeping the two in agreement is this test.
//
// They were badly out of agreement: rotation was read fifth where the schema
// emits `angle` last, health was read as two uint16s where the schema says
// four-byte floats, and five Ship fields were absent — so every field past the
// fourth decoded from the wrong bytes. CI was green throughout, because no gate
// compared the bot to the schema and a load-test client's wrong values look
// like gameplay rather than like a decode error.
//
// Asserting consumed-byte count catches insertion, deletion and width changes.
// It cannot catch two same-width fields swapping, which is why decodeSnapshot
// carries the field order in comments beside the reads.
func TestBotDecoderMatchesTheSchema(t *testing.T) {
	doc := loadSchemaDoc(t)

	for _, kind := range []uint8{
		gamecomp.KindShip,
		gamecomp.KindNPC,
		gamecomp.KindAsteroid,
		gamecomp.KindLootCrate,
	} {
		want, ok := snapshotWidth(t, doc, kind)
		if !ok {
			t.Errorf("kind %d absent from the schema golden", kind)
			continue
		}
		// A buffer of exactly the schema's width. A decoder reading the right
		// fields consumes all of it and no more.
		r := quantize.NewSnapshotReader(make([]byte, want))
		if es := decodeSnapshotFrom(r, 1, kind); es == nil {
			t.Errorf("kind %d: decodeSnapshotFrom returned nil", kind)
			continue
		}
		if left := r.Remaining(); left != 0 {
			t.Errorf("kind %d (%s): decoder consumed %d of %d snapshot bytes, %d left over — "+
				"it disagrees with the schema about this kind's layout",
				kind, kindName(doc, kind), want-left, want, left)
		}
	}
}
