package universe

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// gateFor builds a Process whose SchemaFingerprint() answers want, without
// standing up a protocol: the gate only ever asks for the number.
type fakeProtocol struct{ fp uint32 }

func (f fakeProtocol) SchemaFingerprint() uint32 { return f.fp }

func gateFor(t *testing.T, want uint32, allowMissing bool) (http.HandlerFunc, *bool) {
	t.Helper()
	c := &Process{Log: newGateTestLogger()}
	c.cfg.AllowUnfingerprintedClients = allowMissing
	if want != 0 {
		c.protocol = fakeProtocol{fp: want}
	}
	reached := false
	h := c.schemaGate(func(http.ResponseWriter, *http.Request) { reached = true })
	return h, &reached
}

func call(t *testing.T, h http.HandlerFunc, query string) int {
	t.Helper()
	url := "/ws"
	if query != "" {
		url += "?" + query
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec.Code
}

func TestSchemaGate(t *testing.T) {
	const want uint32 = 0xB85844E7
	match := FormatSchemaFingerprint(want)

	t.Run("matching fingerprint passes", func(t *testing.T) {
		h, reached := gateFor(t, want, false)
		if code := call(t, h, "schema="+match); code != http.StatusOK {
			t.Errorf("status %d, want 200", code)
		}
		if !*reached {
			t.Error("handler not reached on a matching fingerprint")
		}
	})

	// The refusal must happen BEFORE the wrapped handler, because on the
	// WebSocket route that handler is the upgrade: once it runs the connection
	// exists, and this repo has no reason-bearing close to hang it up with.
	t.Run("mismatched fingerprint is refused before the handler", func(t *testing.T) {
		h, reached := gateFor(t, want, false)
		if code := call(t, h, "schema=deadbeef"); code != http.StatusConflict {
			t.Errorf("status %d, want 409", code)
		}
		if *reached {
			t.Error("handler ran despite a mismatched fingerprint")
		}
	})

	// Absent must refuse, or every stale client bypasses the gate by simply
	// not sending the parameter.
	t.Run("absent fingerprint is refused by default", func(t *testing.T) {
		h, reached := gateFor(t, want, false)
		if code := call(t, h, ""); code != http.StatusConflict {
			t.Errorf("status %d, want 409", code)
		}
		if *reached {
			t.Error("handler ran with no fingerprint presented")
		}
	})

	// The escape hatch permits ABSENT only. A wrong value is never a human at
	// a terminal, it is a stale build.
	t.Run("allow-unfingerprinted permits absent but not wrong", func(t *testing.T) {
		h, reached := gateFor(t, want, true)
		if code := call(t, h, ""); code != http.StatusOK {
			t.Errorf("absent with the escape hatch: status %d, want 200", code)
		}
		if !*reached {
			t.Error("handler not reached with the escape hatch and no fingerprint")
		}

		h2, reached2 := gateFor(t, want, true)
		if code := call(t, h2, "schema=deadbeef"); code != http.StatusConflict {
			t.Errorf("wrong value with the escape hatch: status %d, want 409", code)
		}
		if *reached2 {
			t.Error("the escape hatch admitted a WRONG fingerprint")
		}
	})

	// A malformed parameter is a mismatch, not a pass. Parsing leniently is a
	// way to be admitted by accident.
	t.Run("malformed values are refused", func(t *testing.T) {
		for _, bad := range []string{"zzzzzzzz", "b85844e", "b85844e77", "0xb85844e7", "%20b85844e7"} {
			h, _ := gateFor(t, want, false)
			if code := call(t, h, "schema="+bad); code != http.StatusConflict {
				t.Errorf("schema=%q: status %d, want 409", bad, code)
			}
		}
	})

	// A process with no protocol has nothing to compare against — the state
	// every pkg/universe fixture that skips the facade is in.
	t.Run("no protocol installed passes everything", func(t *testing.T) {
		h, reached := gateFor(t, 0, false)
		if code := call(t, h, ""); code != http.StatusOK {
			t.Errorf("status %d, want 200", code)
		}
		if !*reached {
			t.Error("a process with no schema should not gate")
		}
	})

	// The refusal must not tell the client what the server expects: echoing it
	// turns the gate into an oracle a stale client can replay past.
	t.Run("the refusal is not an oracle", func(t *testing.T) {
		c := &Process{Log: newGateTestLogger()}
		c.protocol = fakeProtocol{fp: want}
		rec := httptest.NewRecorder()
		c.schemaGate(func(http.ResponseWriter, *http.Request) {})(rec,
			httptest.NewRequest(http.MethodGet, "/ws?schema=deadbeef", nil))

		if body := rec.Body.String(); containsFold(body, match) {
			t.Errorf("the 409 body leaks the server fingerprint: %q", body)
		}
		for k, v := range rec.Header() {
			for _, hv := range v {
				if containsFold(hv, match) {
					t.Errorf("header %s leaks the server fingerprint: %q", k, hv)
				}
			}
		}
	})
}

func TestParseSchemaFingerprintRoundTrips(t *testing.T) {
	for _, fp := range []uint32{1, 0xDEADBEEF, 0xB85844E7, 0xFFFFFFFF} {
		got, ok := ParseSchemaFingerprint(FormatSchemaFingerprint(fp))
		if !ok || got != fp {
			t.Errorf("round trip of %08x: got %08x ok=%v", fp, got, ok)
		}
	}
}
