package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// newTestAdapter creates a cmdsysAdapter with no ExecOnLoop — handlers run
// synchronously on the calling goroutine. Suitable for unit tests.
func newTestAdapter() *cmdsysAdapter {
	return newCmdsysAdapter()
}

// dispatchSync invokes verb+rest through the dispatcher without ExecOnLoop.
// Routes through Dispatch (not DispatchRaw) so secret-field handling and
// other Dispatch-only logic stays exercised in tests.
func dispatchSync(a *cmdsysAdapter, raw string) string {
	return a.Dispatch(raw)
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
		Usage:       "test.echo <message>",
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			ea := args.(echoArgs)
			return echoResult{Echo: ea.Message}, nil
		},
	})
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}

	out := dispatchSync(a, "test.echo hello")
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// 2. Help output includes every registered command grouped by capability namespace
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
			Usage:       verb,
			Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
				return nil, nil
			},
		})
	}

	help := a.buildHelpText(map[string]bool{})

	for _, v := range verbs {
		cat := v.cat
		if !strings.Contains(strings.ToLower(help), cat) {
			t.Errorf("help missing category %q\n---\n%s", cat, help)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Console.Dispatcher() returns the same instance used by the REPL
// ---------------------------------------------------------------------------

func TestConsole_DispatcherAccessor(t *testing.T) {
	a := newTestAdapter()

	type pingArgs struct{ Msg string }
	type pingResult struct{ Reply string }
	_ = a.registerTyped(cmdsys.Command{
		Verb:        "ping.check",
		Capability:  "ping.check",
		Description: "ping",
		Route:       cmdsys.RouteLocal,
		Args:        pingArgs{},
		Result:      pingResult{},
		Usage:       "ping.check <msg>",
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			pa := args.(pingArgs)
			return pingResult{Reply: "pong:" + pa.Msg}, nil
		},
	})

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
// 5. Group dispatch: "mygroup dosomething" routes to "mygroup.dosomething"
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
		Usage:       "mygroup dosomething",
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			called = true
			return nil, nil
		},
	})

	_ = dispatchSync(a, "mygroup dosomething")
	if !called {
		t.Error("expected group dispatch to route 'mygroup dosomething' to 'mygroup.dosomething'")
	}
}

// ---------------------------------------------------------------------------
// 6. Unknown verb returns empty string (not an error message)
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_UnknownVerbSilent(t *testing.T) {
	a := newTestAdapter()
	out := dispatchSync(a, "definitely.unknown.verb foo bar")
	if out != "" {
		t.Errorf("expected empty output for unknown verb, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// 7. registerGroupShim dispatches to sub-verbs
// ---------------------------------------------------------------------------

func TestCmdsysAdapter_GroupShim(t *testing.T) {
	a := newTestAdapter()

	called := false
	_ = a.registerTyped(cmdsys.Command{
		Verb:        "grp.sub",
		Capability:  "grp.sub",
		Description: "sub command",
		Route:       cmdsys.RouteLocal,
		Args:        nil,
		Result:      nil,
		Usage:       "grp sub",
		Handler: func(ctx context.Context, env *cmdsys.Env, args any) (any, error) {
			called = true
			return nil, nil
		},
	})
	_ = a.registerGroupShim("grp", "a group")

	// Dispatch "grp sub" — the group shim should re-dispatch to "grp.sub".
	_ = dispatchSync(a, "grp sub")
	if !called {
		t.Error("expected group shim to route 'grp sub' to 'grp.sub'")
	}
}

// ---------------------------------------------------------------------------
// 8. Help result renders text end-to-end
// ---------------------------------------------------------------------------

func TestConsole_HelpResult_RendersText(t *testing.T) {
	type echoArgs struct {
		Message string `cmd:"help=text to echo"`
	}
	a := newTestAdapter()
	err := a.registerTyped(cmdsys.Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "echo a message",
		Route:       cmdsys.RouteLocal,
		Args:        echoArgs{},
		Result:      nil,
		Usage:       "test.echo <message>",
		Examples:    []string{"test.echo hello"},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}
	out := a.Dispatch("test.echo --help")
	if !strings.Contains(out, "test echo — echo a message") {
		t.Errorf("expected help title in output. got:\n%s", out)
	}
	if !strings.Contains(out, "USAGE") {
		t.Errorf("expected USAGE section. got:\n%s", out)
	}
	if !strings.Contains(out, "EXAMPLES") {
		t.Errorf("expected EXAMPLES section. got:\n%s", out)
	}
	if strings.Contains(out, "{") {
		t.Errorf("output should not contain typed-struct braces. got:\n%s", out)
	}
}

func TestDispatch_SecretFieldPromptsAndPopulates(t *testing.T) {
	a := newTestAdapter()

	type args struct {
		Name     string `cmd:"help=user"`
		Password string `cmd:"secret,help=password"`
	}
	type result struct {
		GotPassword string `json:"gotPassword"`
	}

	captured := ""
	err := a.Registry.Register(cmdsys.Command{
		Verb:        "test.secret",
		Capability:  "test.secret",
		Description: "verifies secret prompt",
		Args:        args{},
		Result:      result{},
		Route:       cmdsys.RouteLocal,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(args)
			captured = a.Password
			return result{GotPassword: a.Password}, nil
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Pipe stdin so promptSecretFields' non-TTY fallback reads "s3cret".
	old := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	defer func() { os.Stdin = old }()
	go func() {
		_, _ = w.Write([]byte("s3cret\n"))
		_ = w.Close()
	}()

	out := dispatchSync(a, "test.secret alice")
	if captured != "s3cret" {
		t.Fatalf("handler captured password %q; want %q (console out: %s)", captured, "s3cret", out)
	}
}

func TestConsole_QuestionMarkSuffix_End2End(t *testing.T) {
	type splitArgs struct {
		CellID string `cmd:"help=target cell ID"`
	}
	a := newTestAdapter()
	err := a.registerTyped(cmdsys.Command{
		Verb:        "cell.split",
		Capability:  "cell.split",
		Description: "split a cell",
		Route:       cmdsys.RouteLocal,
		Args:        splitArgs{},
		Usage:       "cell split <cellID>",
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("registerTyped: %v", err)
	}
	out := a.Dispatch("cell split ?")
	if !strings.Contains(out, "cell split — split a cell") {
		t.Errorf("expected per-command help. got:\n%s", out)
	}
}
