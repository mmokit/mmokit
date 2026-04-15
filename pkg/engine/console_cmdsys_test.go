package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
)

// newTestAdapter creates a cmdsysAdapter with no ExecOnLoop — handlers run
// synchronously on the calling goroutine. Suitable for unit tests.
func newTestAdapter() *cmdsysAdapter {
	return newCmdsysAdapter()
}

// dispatchSync invokes verb+rest through the dispatcher without ExecOnLoop.
func dispatchSync(a *cmdsysAdapter, raw string) string {
	return a.DispatchRaw(raw)
}

// ---------------------------------------------------------------------------
// 1. Typed cmdsys.Command: register and invoke via adapter
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_TypedCommand(t *testing.T) {
	type echoArgs struct {
		Message string
	}
	type echoResult struct {
		Echo string
	}

	a := newTestAdapter()
	err := a.registerTyped(cmdsys.Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "echo a message",
		Route:       cmdsys.RouteLocal,
		Args:        echoArgs{},
		Result:      echoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			ea := args.(echoArgs)
			return echoResult{Echo: ea.Message}, nil
		},
	}, "test.echo <message>", nil)
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}

	out := dispatchSync(a, "test.echo hello")
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// 2. Legacy Cmd shim: Register → adapter roundtrip
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_ShimRoundtrip(t *testing.T) {
	a := newTestAdapter()

	called := false
	var gotArgs []string
	err := a.registerShim("greet", "test", "say hello", "greet <name>", nil,
		func(args []string) {
			called = true
			gotArgs = args
		},
	)
	if err != nil {
		t.Fatalf("registerShim: %v", err)
	}

	// dispatch "greet world" — the shim splits on whitespace and calls fn(["world"])
	_ = dispatchSync(a, "greet world")
	if !called {
		t.Fatal("shim fn was not called")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "world" {
		t.Errorf("expected args [\"world\"], got %v", gotArgs)
	}
}

// ---------------------------------------------------------------------------
// 3. config set end-to-end via RegisterBuiltins + fake Configurable
// ---------------------------------------------------------------------------

type builtinTestConfig struct {
	Foo string
	Bar int
}

func TestBuiltins_ConfigSet(t *testing.T) {
	cfg := &builtinTestConfig{Foo: "initial", Bar: 42}
	rc := NewReflectConfig(cfg)

	var changedField string
	opts := BuiltinOpts{
		Config: rc,
		ConfigOnChanged: func(field string) {
			changedField = field
		},
	}

	a := newTestAdapter()
	// Wire opts into typed commands directly rather than going through Console
	// (which needs readline). We call the same helper the Console uses.
	c := &Console{
		adapter:     a,
		log:         logger.New(),
		commands:    make(map[string]*Command),
		groups:      make(map[string]*CommandGroup),
		completions: make(map[string][]string),
		builtinCats: make(map[string]bool),
	}
	c.RegisterBuiltins(opts)

	// Dispatch "config.set Foo newval" synchronously.
	out := a.DispatchRaw("config.set Foo newval")
	if !strings.Contains(out, "initial") || !strings.Contains(out, "newval") {
		t.Errorf("expected old->new in output, got: %q", out)
	}
	if cfg.Foo != "newval" {
		t.Errorf("expected Foo=\"newval\", got %q", cfg.Foo)
	}
	if changedField != "Foo" {
		t.Errorf("expected ConfigOnChanged(\"Foo\"), got %q", changedField)
	}
}

// ---------------------------------------------------------------------------
// 4. Help output includes every registered command grouped by capability namespace
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_HelpCoversAllRegistered(t *testing.T) {
	a := newTestAdapter()

	// Register a few verbs in different namespaces.
	verbs := []struct {
		verb string
		cat  string
	}{
		{"alpha.do", "alpha"},
		{"alpha.undo", "alpha"},
		{"beta.run", "beta"},
	}
	for _, v := range verbs {
		verb := v.verb
		_ = a.registerTyped(cmdsys.Command{
			Verb:        verb,
			Capability:  cmdsys.Capability(verb),
			Description: "desc for " + verb,
			Route:       cmdsys.RouteLocal,
			Args:        nil,
			Result:      nil,
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				return nil, nil
			},
		}, verb, nil)
	}

	help := a.buildHelpText(map[string]bool{})

	for _, v := range verbs {
		// Each group should appear in help (at least the namespace/group header).
		cat := v.cat
		if !strings.Contains(strings.ToLower(help), cat) {
			t.Errorf("help missing category %q\n---\n%s", cat, help)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. Console.Dispatcher() returns the same instance used by the REPL
// ---------------------------------------------------------------------------

func TestConsole_DispatcherAccessor(t *testing.T) {
	// We can't create a full Console (needs readline + tty), so test via adapter.
	a := newTestAdapter()

	// Register a command and verify Dispatcher routes to it.
	type pingArgs struct{ Msg string }
	type pingResult struct{ Reply string }
	_ = a.registerTyped(cmdsys.Command{
		Verb:        "ping.check",
		Capability:  "ping.check",
		Description: "ping",
		Route:       cmdsys.RouteLocal,
		Args:        pingArgs{},
		Result:      pingResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			pa := args.(pingArgs)
			return pingResult{Reply: "pong:" + pa.Msg}, nil
		},
	}, "ping.check <msg>", nil)

	// Invoke directly via Dispatcher (the same instance DispatchRaw uses).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := a.Dispatcher.Invoke(ctx, operatorCaller, "ping.check", "hello")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if len(res.PerTarget) == 0 || !res.PerTarget[0].OK {
		t.Fatalf("expected OK result, got %+v", res.PerTarget)
	}
	pr, ok := res.PerTarget[0].Result.(pingResult)
	if !ok {
		t.Fatalf("expected pingResult, got %T", res.PerTarget[0].Result)
	}
	if pr.Reply != "pong:hello" {
		t.Errorf("expected \"pong:hello\", got %q", pr.Reply)
	}
}

// ---------------------------------------------------------------------------
// 6. Group dispatch: "config list" routes to "config.list"
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_GroupDispatch(t *testing.T) {
	a := newTestAdapter()

	called := false
	_ = a.registerTyped(cmdsys.Command{
		Verb:        "mygroup.dosomething",
		Capability:  "mygroup.dosomething",
		Description: "do something",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      nil,
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			called = true
			return nil, nil
		},
	}, "mygroup dosomething", nil)

	// Dispatch "mygroup dosomething" — the adapter should rewrite to "mygroup.dosomething".
	_ = dispatchSync(a, "mygroup dosomething")
	if !called {
		t.Error("expected group dispatch to route 'mygroup dosomething' to 'mygroup.dosomething'")
	}
}

// ---------------------------------------------------------------------------
// 7. Unknown verb returns empty string (not an error message)
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_UnknownVerbSilent(t *testing.T) {
	a := newTestAdapter()
	out := dispatchSync(a, "definitely.unknown.verb foo bar")
	if out != "" {
		t.Errorf("expected empty output for unknown verb, got: %q", out)
	}
}
