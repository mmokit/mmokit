package universe

import (
	"reflect"
	"testing"
)

// bindClientInputTypeID forces id → typ in the process registry's client-input
// index, restoring the previous binding when the test ends.
//
// Registration derives the typeID from the type, and that is the right rule
// everywhere except here: FuzzDispatchInboundEventFrame's committed seed corpus
// and the ingress load harness both encode hand-chosen typeIDs into their
// frames, and re-deriving them would invalidate testdata that
// TestFuzzSeedCorpus pins. Reaching into the map keeps those fixtures honest
// about what they are doing rather than adding a production API that only
// tests want.
func bindClientInputTypeID(tb testing.TB, id uint32, typ reflect.Type) {
	tb.Helper()
	w := globalWire
	w.mu.Lock()
	prev, had := w.ciByType[id]
	w.ciByType[id] = typ
	w.mu.Unlock()
	tb.Cleanup(func() {
		w.mu.Lock()
		if had {
			w.ciByType[id] = prev
		} else {
			delete(w.ciByType, id)
		}
		w.mu.Unlock()
	})
}
