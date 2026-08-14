package engine

import (
	"context"
	"slices"
	"sort"
	"testing"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

// newTestConsole builds a minimal *Console suitable for driving the
// completer in isolation — no readline, no game loop, no IO.
func newTestConsole(t *testing.T) *Console {
	t.Helper()
	return &Console{
		completions:          map[string][]string{},
		completionSources:    map[string]func() []string{},
		completionSourcesCtx: map[string]func([]string) []string{},
		adapter:              newCmdsysAdapter(),
	}
}

// completionsAt drives consoleCompleter.Do for a given input string and
// cursor position (defaulting to end of line). Returns the suggestion
// strings sorted for stable assertions.
func completionsAt(t *testing.T, c *Console, line string) []string {
	t.Helper()
	cc := &consoleCompleter{console: c}
	out, _ := cc.Do([]rune(line), len(line))
	got := make([]string, 0, len(out))
	for _, s := range out {
		got = append(got, string(s))
	}
	sort.Strings(got)
	return got
}

// TestCompletion_StaticSource confirms the existing static-list path
// still works alongside the new contextual one.
func TestCompletion_StaticSource(t *testing.T) {
	type args struct {
		Name string `cmd:"complete=names"`
	}
	c := newTestConsole(t)
	c.SetCompletions("names", []string{"alice", "bob", "carol"})
	if err := c.adapter.registerTyped(cmdsys.Command{
		Verb: "test.greet", Args: args{},
		Usage:   "test.greet <name>",
		Handler: func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}
	// filterMap appends a trailing space so readline advances the cursor
	// after completing. Result is "match-after-prefix + ' '".
	got := completionsAt(t, c, "test greet ")
	want := []string{"alice ", "bob ", "carol "}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestCompletion_ContextualSource confirms a SetCompletionSourceCtx
// provider receives the prior tokens. Used by `service call` to suggest
// request-message field names that depend on the typed op.
func TestCompletion_ContextualSource(t *testing.T) {
	type args struct {
		Kind string `cmd:"complete=kinds"`
		Op   string `cmd:"complete=ops"`
		Args string `cmd:"rest,optional,complete=fields"`
	}
	c := newTestConsole(t)
	c.SetCompletionSource("kinds", func() []string { return []string{"echo"} })
	c.SetCompletionSource("ops", func() []string { return []string{"echoPing", "echoPersist"} })

	// Contextual: returns different field names depending on which op was
	// previously typed.
	c.SetCompletionSourceCtx("fields", func(tokens []string) []string {
		if len(tokens) < 4 {
			return nil
		}
		switch tokens[3] {
		case "echoPing":
			return []string{"msg="}
		case "echoPersist":
			return []string{"key=", "value="}
		}
		return nil
	})

	if err := c.adapter.registerTyped(cmdsys.Command{
		Verb: "test.call", Args: args{},
		Usage:   "test.call <kind> <op> [args...]",
		Handler: func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}

	// After typing "test call echo echoPing " → context source should see
	// ["test", "call", "echo", "echoPing"] and return msg=. Suggestions
	// ending in '=' don't get a trailing space (partial assignment —
	// operator types the value next).
	got := completionsAt(t, c, "test call echo echoPing ")
	want := []string{"msg="}
	if !equalStringSlices(got, want) {
		t.Errorf("after echoPing: got %v, want %v", got, want)
	}

	// Different op → different fields.
	got = completionsAt(t, c, "test call echo echoPersist ")
	want = []string{"key=", "value="}
	if !equalStringSlices(got, want) {
		t.Errorf("after echoPersist: got %v, want %v", got, want)
	}

	// Same context, partial prefix on the args field — filterMap returns
	// the chars AFTER the typed prefix, so "k" + "ey=" completes to "key=".
	got = completionsAt(t, c, "test call echo echoPersist k")
	want = []string{"ey="}
	if !equalStringSlices(got, want) {
		t.Errorf("after echoPersist + k prefix: got %v, want %v", got, want)
	}
}

// TestCompletion_RestFieldClampsArgIndex confirms that completion
// continues to fire for tokens beyond the declared positional count
// when the last positional field is a Rest field. Without the clamp,
// 'service call kind op key1=v key2=' wouldn't get key2= suggestions.
func TestCompletion_RestFieldClampsArgIndex(t *testing.T) {
	type args struct {
		Kind string
		Op   string
		Args string `cmd:"rest,optional,complete=fields"`
	}
	c := newTestConsole(t)
	c.SetCompletionSource("fields", func() []string { return []string{"alpha=", "beta="} })

	if err := c.adapter.registerTyped(cmdsys.Command{
		Verb: "test.call", Args: args{},
		Usage:   "test.call <kind> <op> [args...]",
		Handler: func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil },
	}); err != nil {
		t.Fatal(err)
	}

	// First rest token (third positional, in-range): should complete.
	// '=' suffix suppresses the trailing space — partial assignment.
	got := completionsAt(t, c, "test call k1 op1 ")
	if !equalStringSlices(got, []string{"alpha=", "beta="}) {
		t.Errorf("first rest token: got %v", got)
	}

	// Second rest token (fourth positional, out-of-range without clamp):
	// should still complete because the last field is Rest=true.
	got = completionsAt(t, c, "test call k1 op1 alpha=v ")
	if !equalStringSlices(got, []string{"alpha=", "beta="}) {
		t.Errorf("second rest token: got %v", got)
	}
}

// TestCompletion_DeepNamespace confirms that namespace completion works at
// every depth — `auth `, `auth user `, `auth user lock `, `auth user lock
// alice ` all produce the right suggestions. Regression test for the bug
// where the completer only handled two-token namespaces.
func TestCompletion_DeepNamespace(t *testing.T) {
	type lockArgs struct {
		Username string `cmd:"complete=users"`
		Duration string
	}
	type emptyArgs struct{}

	c := newTestConsole(t)
	c.SetCompletions("users", []string{"alice", "bob"})

	noop := func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil }
	for _, v := range []string{"auth.user.info", "auth.user.unlock", "auth.session.list"} {
		if err := c.adapter.registerTyped(cmdsys.Command{
			Verb: v, Args: emptyArgs{}, Handler: noop,
			Usage: v,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.adapter.registerTyped(cmdsys.Command{
		Verb: "auth.user.lock", Args: lockArgs{}, Handler: noop,
		Usage: "auth.user.lock <user> <duration>",
	}); err != nil {
		t.Fatal(err)
	}

	// Depth 1: top-level → "auth"
	got := completionsAt(t, c, "")
	if !slices.Contains(got, "auth ") {
		t.Errorf("top-level: missing 'auth ' in %v", got)
	}

	// Depth 2: under "auth " → "user", "session"
	got = completionsAt(t, c, "auth ")
	want := []string{"session ", "user "}
	if !equalStringSlices(got, want) {
		t.Errorf("auth: got %v, want %v", got, want)
	}

	// Depth 3: under "auth user " → "info", "lock", "unlock"
	got = completionsAt(t, c, "auth user ")
	want = []string{"info ", "lock ", "unlock "}
	if !equalStringSlices(got, want) {
		t.Errorf("auth user: got %v, want %v", got, want)
	}

	// Depth 3, partial: "auth user l" → "lock"
	got = completionsAt(t, c, "auth user l")
	if !equalStringSlices(got, []string{"ock "}) {
		t.Errorf("auth user l: got %v, want [ock ]", got)
	}

	// Past the verb: "auth user lock " → first arg (Username) → users source.
	got = completionsAt(t, c, "auth user lock ")
	if !equalStringSlices(got, []string{"alice ", "bob "}) {
		t.Errorf("auth user lock: got %v, want [alice  bob ]", got)
	}

	// Past the verb with arg prefix: "auth user lock a" → match "alice".
	got = completionsAt(t, c, "auth user lock a")
	if !equalStringSlices(got, []string{"lice "}) {
		t.Errorf("auth user lock a: got %v, want [lice ]", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
