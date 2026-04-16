package universe

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newCmdsysTestCoord(t *testing.T, hosts []string) *Coordinator {
	t.Helper()
	cfg := Config{
		CellsX:              2,
		CellsY:              1,
		TestHosts:           hosts,
		Headless:            true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		LoginHandler:        func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
		DynamicPartitioning: DisabledPartitionConfig(),
	}
	coord := NewCoordinator(cfg)
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)
	return coord
}

func cmdCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func opCaller() cmdsys.Caller { return cmdsys.NewOperatorIdentity("test-op") }

// ── 1. Local dispatch ─────────────────────────────────────────────────────────

func TestCmdsys_LocalDispatch(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})

	type echoArgs struct{ Msg string }
	type echoResult struct{ Echo string }

	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "echo",
		Route:       cmdsys.RouteLocal,
		Args:        echoArgs{},
		Result:      echoResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
			return echoResult{Echo: args.(echoArgs).Msg}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "test.echo", echoArgs{Msg: "hello"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected 1 OK target: %+v", res.PerTarget)
	}
	er, ok := res.PerTarget[0].Result.(echoResult)
	if !ok {
		t.Fatalf("result type: got %T", res.PerTarget[0].Result)
	}
	if er.Echo != "hello" {
		t.Errorf("Echo: got %q want hello", er.Echo)
	}
}

// ── 2. Remote dispatch via colocated short-circuit ───────────────────────────

func TestCmdsys_RemoteDispatch(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})

	var handlerRuns atomic.Int32

	type args struct {
		HostID string `cmd:"optional"`
	}
	type result struct{ Ran bool }

	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.remoteecho",
		Capability:  "test.remoteecho",
		Route:       cmdsys.RouteSpecificHost,
		Args:        args{},
		Result:      result{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			handlerRuns.Add(1)
			return result{Ran: true}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "test.remoteecho", args{HostID: "host-b"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.PerTarget) != 1 {
		t.Fatalf("expected 1 target, got %d", len(res.PerTarget))
	}
	tr := res.PerTarget[0]
	if !tr.OK {
		t.Errorf("target not OK: %s", tr.Error)
	}
	if tr.TargetID != "host-b" {
		t.Errorf("expected TargetID=host-b, got %q", tr.TargetID)
	}
	if handlerRuns.Load() == 0 {
		t.Error("handler never ran")
	}
}

// ── 3. PlayerOwner routing ───────────────────────────────────────────────────

func TestCmdsys_PlayerOwnerRouting(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})

	coord.notifySessionActive("alice", "host-a")

	type tpArgs struct {
		Username string
		X        float32 `cmd:"optional"`
		Y        float32 `cmd:"optional"`
	}
	type tpResult struct{ Teleported bool }

	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.tp",
		Capability:  "test.tp",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        tpArgs{},
		Result:      tpResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			return tpResult{Teleported: true}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	res, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "test.tp", tpArgs{Username: "alice", X: 100, Y: 200})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.PerTarget) != 1 || !res.PerTarget[0].OK {
		t.Fatalf("expected 1 OK target: %+v", res.PerTarget)
	}
	if res.PerTarget[0].TargetID != "host-a" {
		t.Errorf("expected target host-a, got %q", res.PerTarget[0].TargetID)
	}
}

// ── 4. Schema version mismatch ───────────────────────────────────────────────

// TestCmdsys_SchemaVersionMismatch builds a CommandRequest whose schema_version
// differs from the local command's ArgsSchemaHash, then calls executeCommandRequest
// directly — the same path the server-side handler uses — and verifies the
// "schema_version" error is returned without executing the handler.
func TestCmdsys_SchemaVersionMismatch(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a"})

	type fullArgs struct{ X, Y, Z float32 }
	type trimArgs struct{ X, Y float32 }
	type res struct{ OK bool }

	handlerCalled := false
	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.schemacmd",
		Capability:  "test.schemacmd",
		Route:       cmdsys.RouteLocal,
		Args:        fullArgs{},
		Result:      res{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			handlerCalled = true
			return res{OK: true}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	// Compute the trimArgs schema hash by registering it in a throwaway registry.
	trimReg := cmdsys.NewRegistry()
	_ = trimReg.Register(cmdsys.Command{
		Verb:        "test.schemacmd",
		Capability:  "test.schemacmd",
		Route:       cmdsys.RouteLocal,
		Args:        trimArgs{},
		Result:      res{},
		Handler:     func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) { return res{}, nil },
	})
	trimCmd, _ := trimReg.Lookup("test.schemacmd")

	// Craft a CommandRequest carrying the trimArgs schema hash (mismatch).
	req := &meshpb.CommandRequest{
		RequestId:         1,
		Verb:              "test.schemacmd",
		ArgsJson:          []byte(`{"X":1,"Y":2,"Z":3}`),
		DeadlineUnixNanos: time.Now().Add(5 * time.Second).UnixNano(),
		SchemaVersion:     trimCmd.ArgsSchemaHash, // differs from fullArgs hash
	}

	resp := executeCommandRequest(context.Background(), coord.dispatcher, "host-a", req)
	if resp.Ok {
		t.Fatal("expected schema mismatch rejection, got OK")
	}
	if resp.Error != "schema_version" {
		t.Errorf("expected error=schema_version, got %q", resp.Error)
	}
	if handlerCalled {
		t.Error("handler must not be called on schema mismatch")
	}
}

// ── 5. Cancel mid-flight ─────────────────────────────────────────────────────

func TestCmdsys_CancelMidFlight(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})

	blockCh := make(chan struct{})
	handlerStarted := make(chan struct{}, 1)

	type args struct {
		HostID string `cmd:"optional"`
	}

	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.block",
		Capability:  "test.block",
		Route:       cmdsys.RouteSpecificHost,
		Args:        args{},
		Result:      nil,
		Handler: func(ctx context.Context, _ *cmdsys.Env, _ any) (any, error) {
			select {
			case handlerStarted <- struct{}{}:
			default:
			}
			select {
			case <-blockCh:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	invokeDone := make(chan error, 1)
	go func() {
		_, err := coord.CmdDispatcher().Invoke(ctx, opCaller(), "test.block", args{HostID: "host-b"})
		invokeDone <- err
	}()

	// Wait for handler to start, then cancel.
	select {
	case <-handlerStarted:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("handler did not start within 3s")
	}
	cancel() // triggers context cancellation

	select {
	case err := <-invokeDone:
		if err == nil {
			t.Fatal("expected error after cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected ctx error, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Invoke did not return within 3s after cancel")
	}

	close(blockCh) // unblock any lingering handler goroutine
}

// ── 6. EntityOwner routing ───────────────────────────────────────────────────

// TestCmdsys_EntityOwnerFallback seeds the replicaNetIDs map on a cell that
// lives on host-b, then dispatches a RouteEntityOwner command with NetID=42
// and verifies it is routed to host-b.
func TestCmdsys_EntityOwnerFallback(t *testing.T) {
	coord := newCmdsysTestCoord(t, []string{"host-a", "host-b"})

	// Find a cell on host-b.
	var hostBCell *Cell
	coord.mu.RLock()
	for cellID, hostID := range coord.cellToHostMap {
		if hostID == "host-b" {
			hostBCell = coord.Cells[cellID]
			break
		}
	}
	coord.mu.RUnlock()
	if hostBCell == nil {
		t.Fatal("no cell found on host-b")
	}

	// Seed replicaNetIDs to simulate a live entity with netID=42 on host-b.
	hostBCell.Base.ReplicaNetIDs()[42] = hostBCell.Engine.ECS.NewEntity()

	// cmdsys schema supports int32 but not uint32; use int32 and cast.
	type cmdArgs struct{ NetID int32 }
	type cmdResult struct{ Found bool }

	if err := coord.RegisterCommand(cmdsys.Command{
		Verb:        "test.entitycmd",
		Capability:  "test.entitycmd",
		Route:       cmdsys.RouteEntityOwner,
		Args:        cmdArgs{},
		Result:      cmdResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			return cmdResult{Found: true}, nil
		},
	}); err != nil {
		t.Fatalf("RegisterCommand: %v", err)
	}

	result, err := coord.CmdDispatcher().Invoke(cmdCtx(t), opCaller(), "test.entitycmd", cmdArgs{NetID: 42})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(result.PerTarget) != 1 || !result.PerTarget[0].OK {
		t.Fatalf("expected 1 OK target: %+v", result.PerTarget)
	}
	if result.PerTarget[0].TargetID != "host-b" {
		t.Errorf("expected target host-b, got %q", result.PerTarget[0].TargetID)
	}
}
