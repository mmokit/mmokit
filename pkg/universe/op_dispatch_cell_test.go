package universe

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
	"github.com/zenion/mmoserver/pkg/ops"
)

// captureConnSender records SendReliable bytes per connID. Same shape as
// the helper used by send_event_test.go but kept package-internal so this
// file can poke at *Process internals.
type captureConnSender struct {
	mu   sync.Mutex
	sent map[uint32][]byte
}

func newCaptureConn() *captureConnSender {
	return &captureConnSender{sent: make(map[uint32][]byte)}
}

func (c *captureConnSender) Send(connID uint32, data []byte) pkgnet.SendResult {
	c.mu.Lock()
	c.sent[connID] = append([]byte(nil), data...)
	c.mu.Unlock()
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}
func (c *captureConnSender) SendReliable(connID uint32, data []byte) pkgnet.SendResult {
	c.mu.Lock()
	c.sent[connID] = append([]byte(nil), data...)
	c.mu.Unlock()
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}
func (c *captureConnSender) InjectInput(connID uint32, data []byte) {}
func (c *captureConnSender) DrainInput(connID uint32) [][]byte      { return nil }
func (c *captureConnSender) DrainOpInput(connID uint32) [][]byte    { return nil }
func (c *captureConnSender) get(connID uint32) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sent[connID]
}

var _ pkgnet.ConnSender = (*captureConnSender)(nil)

// cellOpReq / cellOpRes are a stand-in for a typed-op pair. We don't go
// through mmokit.RegisterOp here because that registers globally and we
// only need to exercise the *Process side (DispatchCellRoutedOp does its
// own reflect.New + handler call without consulting the registry).
type cellOpReq struct {
	X int32
}

type cellOpRes struct {
	Y int32
}

// installFrameworkHookStubs ensures TypedOpHooks.MakeOperationErrorBody is
// non-nil so encodeOpErrorViaHooks returns frames. mmokit's init normally
// wires these but this test file is in package universe and avoids the
// mmokit import cycle. Restores original values on cleanup.
func installFrameworkHookStubs(t *testing.T) {
	t.Helper()
	prevMakeBody := TypedOpHooks.MakeOperationErrorBody
	prevErrTypeID := TypedOpHooks.OperationErrorTypeID
	t.Cleanup(func() {
		TypedOpHooks.MakeOperationErrorBody = prevMakeBody
		TypedOpHooks.OperationErrorTypeID = prevErrTypeID
	})
	if TypedOpHooks.MakeOperationErrorBody == nil {
		TypedOpHooks.MakeOperationErrorBody = func(uint32, string) []byte { return []byte{0xEE} }
		TypedOpHooks.OperationErrorTypeID = 0xDEADDEAD
	}
}

// newCellRoutedOpFixture builds the scaffolding DispatchCellRoutedOp needs to
// reach a handler: a Process owning cell_0_0, a Stage on a real engine, "alice"
// registered as active in that cell, and a running GameLoop — the loop is what
// marks the loop goroutine and drains SubmitLoopJob entries, so without it the
// job is queued and never runs.
func newCellRoutedOpFixture(t *testing.T) (*Process, *captureConnSender) {
	t.Helper()

	conn := newCaptureConn()
	p := minimalCoordWithCell(t, "host-a", "cell_0_0")

	log := logger.New()
	eng := engine.New(engine.Config{TickRate: 100}, conn, log)
	cellID, _ := ParseCellID("cell_0_0")
	stage := NewStage(eng, cellID, 300, nil)
	stage.coord = p
	cell := NewCell(cellID.MeshID(), cellID)
	cell.Stage = stage
	cell.Inbox = make(chan CellMessage, 8)
	cell.Events = make(chan pkgnet.PlayerEvent, 8)
	if p.Cells == nil {
		p.Cells = make(map[MeshCellID]*Cell)
	}
	p.Cells[cellID.MeshID()] = cell

	// Register the user as active in cell_0_0.
	p.players["alice"] = &PlayerLocation{
		HostID: "host-a",
		CellID: cellID.MeshID(),
		Active: true,
	}

	gl := engine.NewGameLoop(eng, nil, nil, engine.Hooks{})
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		gl.Run(ctx)
		close(loopDone)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-loopDone:
		case <-time.After(2 * time.Second):
			t.Error("game loop did not exit within 2s of cancel")
		}
	})
	return p, conn
}

// awaitFrame polls conn for a frame on connID. The response is sent from the
// loop goroutine after this test's call already returned, so there is nothing
// to synchronize on but the send itself.
func awaitFrame(t *testing.T, conn *captureConnSender, connID uint32) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if frame := conn.get(connID); frame != nil {
			return frame
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no response frame sent to conn %d within timeout", connID)
	return nil
}

// TestProcessDispatchCellRoutedOp_HappyPath exercises the full cell-routed
// path: Process resolves the user's cell, schedules the handler on that
// cell's engine via SubmitLoopJob, the loop runs the handler, encodes the
// response, and sends via the engine's ConnMgr.
func TestProcessDispatchCellRoutedOp_HappyPath(t *testing.T) {
	installFrameworkHookStubs(t)
	p, conn := newCellRoutedOpFixture(t)

	body := mustMarshal(t, &cellOpReq{X: 21})
	const requestID uint64 = 42
	const respTypeID uint32 = 0x33333333

	handler := func(ctx *ops.OpContext, req *cellOpReq) (*cellOpRes, error) {
		if ctx.Username != "alice" {
			t.Errorf("handler ctx.Username = %q, want alice", ctx.Username)
		}
		return &cellOpRes{Y: req.X * 2}, nil
	}

	resp := p.DispatchCellRoutedOp(
		reflect.TypeFor[cellOpReq](),
		respTypeID,
		requestID,
		body,
		handler,
		&ops.OpContext{ConnID: 7, Username: "alice"},
	)
	if resp != nil {
		t.Fatalf("expected nil sync response (async), got %x", resp)
	}

	// Wait for the loop to drain and send the response.
	frame := awaitFrame(t, conn, 7)
	if frame[0] != pkgnet.ChannelOperation {
		t.Errorf("response channel byte = %#x, want 0x01", frame[0])
	}
	gotTypeID, gotReqID, gotBody, err := DecodeTypedOpFrame(frame[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if gotTypeID != respTypeID {
		t.Errorf("response typeID = %#x, want %#x", gotTypeID, respTypeID)
	}
	if gotReqID != requestID {
		t.Errorf("response requestID = %d, want %d", gotReqID, requestID)
	}
	var got cellOpRes
	mustUnmarshal(t, gotBody, &got)
	if got.Y != 42 {
		t.Errorf("response Y = %d, want 42 (handler doubles input)", got.Y)
	}
}

// TestProcessDispatchCellRoutedOp_OfflineUser hits the synchronous
// OperationError path when the user isn't in the active session map.
func TestProcessDispatchCellRoutedOp_OfflineUser(t *testing.T) {
	installFrameworkHookStubs(t)

	p := minimalCoordWithCell(t, "host-a", "cell_0_0")

	body := mustMarshal(t, &cellOpReq{X: 1})
	handler := func(*ops.OpContext, *cellOpReq) (*cellOpRes, error) {
		t.Fatal("handler should not run when user is offline")
		return nil, nil
	}

	resp := p.DispatchCellRoutedOp(
		reflect.TypeFor[cellOpReq](),
		0x11111111,
		7,
		body,
		handler,
		&ops.OpContext{ConnID: 1, Username: "ghost"},
	)
	if resp == nil {
		t.Fatal("expected synchronous OperationError frame for offline user")
	}
	if resp[0] != pkgnet.ChannelOperation {
		t.Errorf("channel byte = %#x, want 0x01", resp[0])
	}
}

// cellOpStrReq is the cell-routed mirror of dispStrReq: a request whose first
// field is a length-prefixed string, so a truncated body is refused by the
// decoder rather than silently arriving as an empty string.
type cellOpStrReq struct {
	Username string
}

// opErrorRecorder captures what encodeOpErrorViaHooks was asked to encode.
// pkg/universe cannot import pkg/mmokit (import cycle), so swapping the hook is
// how an in-package test reads an OperationError's code and message.
type opErrorRecorder struct {
	mu       sync.Mutex
	codes    []uint32
	messages []string
}

func (r *opErrorRecorder) record(code uint32, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes = append(r.codes, code)
	r.messages = append(r.messages, message)
}

func (r *opErrorRecorder) snapshot() ([]uint32, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.codes...), append([]string(nil), r.messages...)
}

// installOpErrorRecorder replaces the OperationError body hook unconditionally
// — unlike installFrameworkHookStubs, which defers to mmokit's real hook when
// the external test package has linked it in. The hook fires on the cell loop
// goroutine, hence the mutex.
func installOpErrorRecorder(t *testing.T) *opErrorRecorder {
	t.Helper()
	rec := &opErrorRecorder{}
	prevMakeBody := TypedOpHooks.MakeOperationErrorBody
	prevErrTypeID := TypedOpHooks.OperationErrorTypeID
	t.Cleanup(func() {
		TypedOpHooks.MakeOperationErrorBody = prevMakeBody
		TypedOpHooks.OperationErrorTypeID = prevErrTypeID
	})
	TypedOpHooks.MakeOperationErrorBody = func(code uint32, message string) []byte {
		rec.record(code, message)
		return []byte{0xEE}
	}
	TypedOpHooks.OperationErrorTypeID = 0xDEADDEAD
	return rec
}

// TestProcessDispatchCellRoutedOp_DecodeError pins the user-visible behaviour
// change CE-002 unit 9 makes on the cell-routed path.
//
// The decode runs inside eng.SubmitLoopJob, which is fire-and-forget: GameLoop
// records a job's returned error and closes its done channel, but nothing reads
// either. So a decode failure reported by returning an error would be dropped
// and the client's pending typed-op promise would never settle — exactly the
// hang the pre-CE-002 panic produced, minus the log line. The answer has to be
// built and sent from inside the closure, and it is asserted here.
func TestProcessDispatchCellRoutedOp_DecodeError(t *testing.T) {
	rec := installOpErrorRecorder(t)
	p, conn := newCellRoutedOpFixture(t)

	handler := func(*ops.OpContext, *cellOpStrReq) (*cellOpRes, error) {
		t.Error("handler ran on a body the decoder refused; it would have seen " +
			"an empty Username indistinguishable from a real one")
		return &cellOpRes{}, nil
	}

	const requestID uint64 = 4242
	// Declares a 64-byte Username and supplies none of it.
	resp := p.DispatchCellRoutedOp(
		reflect.TypeFor[cellOpStrReq](),
		0x33333333,
		requestID,
		[]byte{64, 0},
		handler,
		&ops.OpContext{ConnID: 7, Username: "alice"},
	)
	if resp != nil {
		t.Fatalf("expected nil sync response: the body is not decoded until the "+
			"loop job runs, got %x", resp)
	}

	frame := awaitFrame(t, conn, 7)
	if frame[0] != pkgnet.ChannelOperation {
		t.Errorf("response channel byte = %#x, want 0x01", frame[0])
	}
	gotTypeID, gotReqID, _, err := DecodeTypedOpFrame(frame[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if gotTypeID != TypedOpHooks.OperationErrorTypeID {
		t.Errorf("response typeID = %#x, want OperationError %#x",
			gotTypeID, TypedOpHooks.OperationErrorTypeID)
	}
	if gotReqID != requestID {
		t.Errorf("response requestID = %d, want %d (the client correlates on it)",
			gotReqID, requestID)
	}

	codes, messages := rec.snapshot()
	if len(codes) != 1 {
		t.Fatalf("OperationError bodies encoded = %d, want exactly 1", len(codes))
	}
	if codes[0] != opErrorDecodeFailed {
		t.Errorf("code = %d, want opErrorDecodeFailed(%d)", codes[0], opErrorDecodeFailed)
	}
	if !strings.Contains(messages[0], "decode request") {
		t.Errorf("message = %q, want it to name the decode failure", messages[0])
	}
}
