package universe

import (
	"net/http"

	"github.com/mmokit/mmokit/pkg/metrics"
)

// schemaFingerprintParam is the query parameter both connection-setup routes
// carry the client's fingerprint in.
//
// A query parameter rather than a header because the WebSocket route has to be
// reachable from a browser, and `new WebSocket(url)` cannot set headers. Using
// the same mechanism on both routes keeps one rule to explain.
const schemaFingerprintParam = "schema"

// schemaGate refuses a client whose compiled-in protocol fingerprint disagrees
// with this process's, BEFORE the wrapped handler runs.
//
// Placement is the whole design. On the WebSocket route the check happens
// before websocket.Accept, so a refused client never gets a connID, never
// enters ConnManager.conns, and is invisible to every drain and to
// ActiveConnIDs. That matters because this repo has no reason-bearing close —
// Conn.Close calls CloseNow() and the websocketConn interface exposes nothing
// else — so a gate that ran after the upgrade could only hang up silently.
// Refusing at the HTTP layer produces a status code and a body instead.
//
// The server side of the check is the enforcing one. A design where the server
// only publishes its own fingerprint and trusts the client to compare is
// honest-client-only, and the 2D/3D failure is bidirectional: a stale client
// mis-decodes the server's snapshots AND encodes inputs and ops the server
// mis-decodes. Only rejecting here stops the second.
//
// Absent parameter rejects, rather than passing. Treating "missing" as "fine"
// would let every stale client bypass the gate by simply not sending it.
// Config.AllowUnfingerprintedClients reopens that door for wscat and browser
// devtools; it permits a MISSING parameter only, never a wrong one, because a
// wrong one is never a human at a terminal.
//
// A process with fingerprint 0 has no protocol installed and therefore nothing
// to compare against, so it passes everything through. That is the state of
// every fixture that builds a Process through universe.New without the facade.
func (c *Process) schemaGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := c.SchemaFingerprint()
		if want == 0 {
			next(w, r)
			return
		}

		got := r.URL.Query().Get(schemaFingerprintParam)
		if got == "" {
			if c.cfg.AllowUnfingerprintedClients {
				next(w, r)
				return
			}
			c.rejectSchema(w, r, want, "", "missing")
			return
		}

		parsed, ok := ParseSchemaFingerprint(got)
		if !ok || parsed != want {
			c.rejectSchema(w, r, want, got, "mismatch")
			return
		}
		next(w, r)
	}
}

// rejectSchema answers a refused connection setup and records it.
//
// The response deliberately does NOT echo the server's fingerprint. It is
// tempting for diagnostics and it would turn this route into an oracle: a
// stale client could read the expected value, resend it, pass the gate, and
// then mis-decode exactly as before. An operator reads the expected value from
// `--dump-schema` or from the server log line below, both of which require
// access the client does not have.
//
// 409 Conflict rather than 426 Upgrade Required: 426 obliges the response to
// name a target protocol in an Upgrade header, and there is no meaningful
// value for that here. 409 states the case and is unambiguous in an access log
// beside the origin-check failure the upgrade path can also produce.
func (c *Process) rejectSchema(w http.ResponseWriter, r *http.Request, want uint32, got, kind string) {
	metrics.Ingress().RecordRejected(metrics.SurfaceClient, metrics.ReasonSchemaMismatch)
	c.Log.Log(CatNetConn,
		"schema gate: refused %s %s from %s — client fingerprint %s (%s), this process is %s; regenerate the SDK against this build",
		r.Method, r.URL.Path, r.RemoteAddr, quotedOrNone(got), kind, FormatSchemaFingerprint(want))
	http.Error(w, "schema fingerprint mismatch", http.StatusConflict)
}

func quotedOrNone(s string) string {
	if s == "" {
		return "(absent)"
	}
	return `"` + s + `"`
}
