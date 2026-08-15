package universe

import (
	"context"
	"fmt"
	"reflect"

	"github.com/mmokit/mmokit/pkg/ops"
)

// DispatchCellRoutedOp resolves the player's authoritative cell on this
// process and schedules the handler on that cell's engine via
// engine.RunOnLoop. The handler runs synchronously on the loop goroutine
// (so it has safe ECS access); the response frame is encoded and sent via
// the cell engine's connection manager after the handler returns.
//
// In single-process modes (`all`, `coord+gateway+host`, etc.) the cell
// engine's ConnMgr is the same WebSocket-serving ConnManager the gateway
// uses, so the response reaches the client over its existing socket. In
// remote-host mode the cell engine's ConnMgr is the VirtualConnManager
// which wraps the response in a MeshFrame.ClientFrame and forwards it to
// the originating gateway over MeshData.
//
// Returns nil on successful scheduling (response is sent asynchronously).
// Returns a non-nil OperationError frame synchronously when:
//   - The OpContext has no username (caller is unauthenticated)
//   - No active session exists for that username on this process
//     (player is offline, on a remote host's cell, or hasn't completed
//     auth yet)
//   - The named cell isn't owned by this process (transient — split /
//     merge / migrate may have moved it; the client should retry)
//
// A request body the decoder refuses is answered ASYNCHRONOUSLY, from inside
// the loop job, with OperationError{opErrorDecodeFailed} — it is found on the
// loop goroutine, after this function has already returned nil.
//
// Implements universe.CellOpRouter for *Process. Wired into
// DispatchTypedOpInbound by Process.installTypedOpDispatch when the
// router goroutine starts.
func (p *Process) DispatchCellRoutedOp(
	reqType reflect.Type,
	resTypeID uint32,
	requestID uint64,
	body []byte,
	handler any,
	ctx *ops.OpContext,
) []byte {
	if ctx.Username == "" {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s: unauthenticated session", reqType.String()))
	}

	cellID, ok := p.findActiveUserCell(ctx.Username)
	if !ok {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s: no active cell for user %q on this process",
				reqType.String(), ctx.Username))
	}
	cell, ok := p.getCell(cellID)
	if !ok {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s: cell %s not owned by this process",
				reqType.String(), cellID))
	}
	if cell.Stage == nil || cell.Stage.eng == nil {
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s: cell %s has no engine",
				reqType.String(), cellID))
	}

	eng := cell.Stage.eng
	connID := ctx.ConnID
	// Resolve the profile off-loop: the closure below runs on the cell
	// goroutine and p.wireLimits is frozen at New(), but reading it here keeps
	// the loop job's captures to plain values.
	lim := p.clientWireLimits()

	// Schedule the handler on the cell engine loop. SubmitLoopJob is
	// fire-and-forget — the typed-op poll goroutine returns immediately
	// and the response frame is sent inside the closure once the handler
	// runs on the loop goroutine. RunOnLoop's blocking variant would
	// stall the poll goroutine for ~50ms (one tick) which would back up
	// every other op for that connection.
	// Stash the cell's Stage on the OpContext bag so handlers can reach
	// it via OpContextStage(ctx) without threading another argument
	// through the typed-op signature. See pkg/universe/op_dispatch_cell.go
	// (this file) for the contract; mmokit re-exports the helper.
	ctx.Bag().Store(opContextStageKey, cell.Stage)

	queued := eng.SubmitLoopJob(func() error {
		// Decode the request on the loop goroutine. We could decode
		// off-loop and capture the *Req pointer, but reflection-based
		// decode is the same cost either way and keeping it on-loop
		// means no allocations escape across goroutines.
		reqPtr := reflect.New(reqType)
		// Strict, and under the client profile: this body came off a client
		// socket, so a trailing surplus is a type disagreement rather than an
		// appended blob. Same rule as the RouteGatewayLocal arm in
		// DispatchTypedOpInbound.
		decodeErr := ReflectUnmarshalStrict(cell.Stage, body, reqPtr.Interface(), lim)

		var respFrame []byte
		if decodeErr != nil {
			// Answer here rather than returning the error. SubmitLoopJob is
			// fire-and-forget: GameLoop assigns the job's error and closes its
			// done channel, but nothing reads either, so a returned error is
			// dropped and the client's typed-op promise never settles. Before
			// this the decode panicked and the loop-job recover turned that
			// same hang into a silent one.
			respFrame = encodeOpErrorViaHooks(requestID, opErrorDecodeFailed,
				fmt.Sprintf("op %s: decode request: %v", reqType.String(), decodeErr))
		} else {
			handlerVal := reflect.ValueOf(handler)
			results := handlerVal.Call([]reflect.Value{reflect.ValueOf(ctx), reqPtr})

			resPtr := results[0]
			errVal := results[1]

			if !errVal.IsNil() {
				respFrame = encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
					errVal.Interface().(error).Error())
			} else if resPtr.IsNil() {
				respFrame = EncodeTypedOpFrame(resTypeID, requestID, nil)
			} else if resBody, mErr := ReflectMarshal(resPtr.Interface()); mErr != nil {
				// Handler succeeded but its response does not fit the wire; answer
				// with a typed error so the client's requestID is not left pending.
				respFrame = encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
					fmt.Sprintf("encode response: %v", mErr))
			} else {
				respFrame = EncodeTypedOpFrame(resTypeID, requestID, resBody)
			}
		}

		if respFrame == nil {
			// MakeOperationErrorBody unwired — universe-only test path.
			// Drop silently; tests with real wiring will hit the success
			// branch above.
			return nil
		}
		if eng.ConnMgr != nil {
			eng.ConnMgr.SendReliable(connID, respFrame)
		}
		return nil
	})

	if !queued {
		// Loop queue full — handler did not run. Surface as
		// OperationError so the client doesn't hang on its pending
		// promise indefinitely.
		return encodeOpErrorViaHooks(requestID, opErrorHandlerFailed,
			fmt.Sprintf("op %s: cell %s loop queue full",
				reqType.String(), cellID))
	}

	// Successful async dispatch — the cell engine will send the response
	// frame once the handler runs.
	_ = context.Background // placeholder for ctx-aware future variant
	return nil
}

// opContextStageKeyType is a private type used as the bag key for the
// OpContext-stashed *Stage so handler code can recover it via
// OpContextStage(ctx). Using a unique unexported type avoids collisions
// with any other key strings callers might try.
type opContextStageKeyType struct{}

var opContextStageKey = opContextStageKeyType{}

// OpContextStage returns the *Stage stashed on the OpContext by
// Process.DispatchCellRoutedOp. Returns nil when the OpContext was not
// produced by the cell-routed dispatch path (e.g. RouteGatewayLocal ops,
// tests). RoutePlayerCell handlers can rely on a non-nil return.
func OpContextStage(ctx *ops.OpContext) *Stage {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Bag().Load(opContextStageKey)
	if !ok {
		return nil
	}
	stage, _ := v.(*Stage)
	return stage
}

// findActiveUserCell looks up the active session's cell for username on
// this process. Returns ("", false) when the user has no active session
// here (either offline globally, or routed to a remote host in
// distributed mode).
func (p *Process) findActiveUserCell(username string) (MeshCellID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	loc := p.players[username]
	if loc == nil || !loc.Active {
		return "", false
	}
	if loc.CellID == "" {
		return "", false
	}
	return loc.CellID, true
}
