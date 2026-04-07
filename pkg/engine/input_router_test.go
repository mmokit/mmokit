package engine

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"
)

// --- test helpers ---

// mockTransport implements net.Transport with an injectable input queue.
type mockTransport struct {
	mu    sync.Mutex
	input [][]byte
}

func (m *mockTransport) SendReliable(data []byte)   {}
func (m *mockTransport) SendUnreliable(data []byte)  {}
func (m *mockTransport) Close()                      {}
func (m *mockTransport) DrainOpInput() [][]byte      { return nil }

func (m *mockTransport) DrainInput() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.input
	m.input = nil
	return out
}

func (m *mockTransport) enqueue(msgs ...[]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.input = append(m.input, msgs...)
}

// testEnvelopeParser parses 4-byte LE uint32 code + remaining data.
func testEnvelopeParser(raw []byte) (uint32, []byte, error) {
	if len(raw) < 4 {
		return 0, nil, errors.New("short message")
	}
	code := binary.LittleEndian.Uint32(raw[:4])
	return code, raw[4:], nil
}

// makeTestMsg builds a test envelope: 4-byte LE code + data.
func makeTestMsg(code uint32, data []byte) []byte {
	buf := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(buf[:4], code)
	copy(buf[4:], data)
	return buf
}

// addActivePlayer creates an active player session with a mock transport and returns the transport.
func addActivePlayer(eng *Engine, username string) (*PlayerSession, *mockTransport) {
	mt := &mockTransport{}
	connID := eng.ConnMgr.AddTransport(mt)
	// Drain the connect event so it doesn't pile up
	<-eng.ConnMgr.Events()

	sess := eng.Players.createSession(connID)
	sess.Username = username
	sess.State = StateActive
	eng.Players.byUsername[username] = sess
	return sess, mt
}

// --- tests ---

func TestInputRouter_BasicDispatch(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	var gotCode bool
	var gotData []byte
	router.Handle(42, States(StateActive), func(ctx *InputContext, data []byte) {
		gotCode = true
		gotData = make([]byte, len(data))
		copy(gotData, data)
	})

	_, mt := addActivePlayer(eng, "alice")
	mt.enqueue(makeTestMsg(42, []byte("hello")))

	router.ProcessInput()

	if !gotCode {
		t.Fatal("handler for code 42 was not called")
	}
	if string(gotData) != "hello" {
		t.Errorf("handler got data %q, want %q", gotData, "hello")
	}
}

func TestInputRouter_StateMaskFiltering(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	customState := eng.Players.RegisterState("custom")
	called := false
	// Handler only for custom state
	router.Handle(10, States(customState), func(ctx *InputContext, data []byte) {
		called = true
	})

	// Player is StateActive — should NOT match
	_, mt := addActivePlayer(eng, "bob")
	mt.enqueue(makeTestMsg(10, nil))

	router.ProcessInput()

	if called {
		t.Error("handler fired for wrong state; expected custom state only")
	}
}

func TestInputRouter_StateGroupFilter(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	called := false
	router.Handle(20, States(StateActive), func(ctx *InputContext, data []byte) {
		called = true
	})

	// State filter returns false for all active players — should skip handler
	router.StateFilter(StateActive, func(ctx *InputContext) bool {
		return false
	})

	_, mt := addActivePlayer(eng, "carol")
	mt.enqueue(makeTestMsg(20, nil))

	router.ProcessInput()

	if called {
		t.Error("handler fired despite state filter returning false")
	}

	// Verify messages were still drained
	remaining := mt.DrainInput()
	if len(remaining) != 0 {
		t.Errorf("expected messages to be drained, got %d remaining", len(remaining))
	}
}

func TestInputRouter_PerHandlerGuard(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	guardedCalled := false
	unguardedCalled := false

	// Handler with guard that returns false
	router.Handle(30, States(StateActive), func(ctx *InputContext, data []byte) {
		guardedCalled = true
	}, WithGuard(func(ctx *InputContext) bool { return false }))

	// Handler without guard
	router.Handle(31, States(StateActive), func(ctx *InputContext, data []byte) {
		unguardedCalled = true
	})

	_, mt := addActivePlayer(eng, "dave")
	mt.enqueue(makeTestMsg(30, nil), makeTestMsg(31, nil))

	router.ProcessInput()

	if guardedCalled {
		t.Error("guarded handler should not have fired")
	}
	if !unguardedCalled {
		t.Error("unguarded handler should have fired")
	}
}

func TestInputRouter_DuplicateCodePanics(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	router.Handle(99, States(StateActive), func(ctx *InputContext, data []byte) {})

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate code registration")
		}
	}()

	router.Handle(99, States(StateTransferring), func(ctx *InputContext, data []byte) {})
}

func TestInputRouter_PendingSessionsExcluded(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	called := false
	// Register handler that accepts StatePending
	router.Handle(50, States(StatePending), func(ctx *InputContext, data []byte) {
		called = true
	})

	// Create a pending session (not yet logged in)
	mt := &mockTransport{}
	connID := eng.ConnMgr.AddTransport(mt)
	<-eng.ConnMgr.Events()

	sess := eng.Players.createSession(connID)
	sess.Username = "pending_user"
	// State defaults to StatePending — ForEachConnected skips these

	mt.enqueue(makeTestMsg(50, nil))

	router.ProcessInput()

	if called {
		t.Error("handler should not fire for pending sessions (ForEachConnected excludes them)")
	}
}

func TestInputRouter_Handle(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	type testMsg struct {
		Value string
	}

	var gotMsg testMsg
	Handle(router, uint32(60), States(StateActive),
		func(data []byte) (testMsg, error) {
			return testMsg{Value: string(data)}, nil
		},
		func(ctx *InputContext, msg testMsg) {
			gotMsg = msg
		},
	)

	_, mt := addActivePlayer(eng, "eve")
	mt.enqueue(makeTestMsg(60, []byte("typed-payload")))

	router.ProcessInput()

	if gotMsg.Value != "typed-payload" {
		t.Errorf("Handle got %q, want %q", gotMsg.Value, "typed-payload")
	}
}

func TestInputRouter_Handle_UnmarshalError(t *testing.T) {
	eng := newTestEngine()
	router := NewInputRouter(eng, testEnvelopeParser)

	type testMsg struct{ X int }
	called := false

	Handle(router, uint32(70), States(StateActive),
		func(data []byte) (testMsg, error) {
			return testMsg{}, errors.New("bad data")
		},
		func(ctx *InputContext, msg testMsg) {
			called = true
		},
	)

	_, mt := addActivePlayer(eng, "frank")
	mt.enqueue(makeTestMsg(70, []byte("garbage")))

	router.ProcessInput()

	if called {
		t.Error("handler should not fire when unmarshal returns error")
	}
}
