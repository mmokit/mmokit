package ops

import (
	"sort"
	"testing"

	"github.com/zenion/mmoserver/pkg/net"
)

// Router.poll is the 0x01 drain for the single-process `all` preset — the one
// `just dev` and `just run` use — because runSessionPump is launched only when
// the gateway owns a HostNetwork. It is one process-wide goroutine on a 5 ms
// ticker servicing every connection, so a panicking frame from one connection
// must not stop the pass, and must not escape to Run's bare goroutine.
func TestRouterPoll_MalformedFrame_KeepsDraining(t *testing.T) {
	cm := net.NewConnManager()

	poisoned := &smokeTransport{}
	healthy := &smokeTransport{}
	connA := cm.AddTransport(poisoned, "")
	connB := cm.AddTransport(healthy, "")
	<-cm.Events()
	<-cm.Events()

	r := NewRouter(cm, NewPlayerSessions())

	var dispatched []uint32
	r.SetTypedOpHandler(func(payload []byte, ctx *OpContext) []byte {
		dispatched = append(dispatched, ctx.ConnID)
		if payload[0] == 0xDE {
			// Stand-in for the reflection decoder's unchecked slice: the
			// concrete panic is exercised in pkg/universe, what matters here
			// is that poll survives one.
			panic("simulated decoder fault")
		}
		return []byte{0x01}
	})

	poisoned.opInput = append(poisoned.opInput, []byte{0xDE, 0xAD})
	healthy.opInput = append(healthy.opInput, []byte{0x01, 0x02})

	r.poll() // must not panic

	sort.Slice(dispatched, func(i, j int) bool { return dispatched[i] < dispatched[j] })
	want := []uint32{connA, connB}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if len(dispatched) != 2 || dispatched[0] != want[0] || dispatched[1] != want[1] {
		t.Fatalf("dispatched conns = %v, want both %v — a poisoned frame stopped the pass",
			dispatched, want)
	}
	if len(healthy.reliable) != 1 {
		t.Fatalf("healthy connection got %d responses, want 1", len(healthy.reliable))
	}
	if len(poisoned.reliable) != 0 {
		t.Fatalf("poisoned frame produced %d responses, want 0", len(poisoned.reliable))
	}
}

// One pass must do a bounded amount of work. Before the cap, poll drained every
// connection's op queue to empty on every 5 ms tick, so the cost of a pass was
// set by how fast the clients chose to send.
func TestRouterPoll_StopsAtPerPollCap(t *testing.T) {
	cm := net.NewConnManager()

	const conns = 8
	const framesPerConn = 40

	transports := make([]*smokeTransport, 0, conns)
	for range conns {
		st := &smokeTransport{}
		for range framesPerConn {
			st.opInput = append(st.opInput, []byte{0x01})
		}
		transports = append(transports, st)
		cm.AddTransport(st, "")
		<-cm.Events()
	}

	r := NewRouter(cm, NewPlayerSessions())
	// Budget below the total so the cap has to fire, and not a multiple of
	// framesPerConn so the stop lands mid-walk.
	r.framesPerPoll = 100

	var dispatched int
	r.SetTypedOpHandler(func([]byte, *OpContext) []byte {
		dispatched++
		return nil
	})

	r.poll()

	total := conns * framesPerConn
	if dispatched >= total {
		t.Fatalf("poll dispatched %d of %d frames — the budget never fired", dispatched, total)
	}
	if dispatched < r.framesPerPoll {
		t.Fatalf("poll dispatched only %d frames, want at least the %d-frame budget",
			dispatched, r.framesPerPoll)
	}

	// The deferred connections keep their queued frames for the next pass
	// 5 ms later; nothing is dropped by the budget itself.
	remaining := 0
	for _, st := range transports {
		remaining += len(st.opInput)
	}
	if dispatched+remaining != total {
		t.Fatalf("dispatched %d + remaining %d != %d — the budget dropped frames",
			dispatched, remaining, total)
	}
}

// The default budget must not be zero or negative: an unset field has to mean
// "use the default", never "process nothing".
func TestRouterPollBudget_DefaultsWhenUnset(t *testing.T) {
	r := &Router{}
	if got := r.pollBudget(); got != defaultFramesPerPoll {
		t.Fatalf("pollBudget with unset field = %d, want %d", got, defaultFramesPerPoll)
	}
	r.framesPerPoll = -1
	if got := r.pollBudget(); got != defaultFramesPerPoll {
		t.Fatalf("pollBudget with negative field = %d, want %d", got, defaultFramesPerPoll)
	}
}
