# Admin Operator Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators be created, listed, rotated, and deleted from both the server console (TTY-prompt for password) and the admin web UI (Users page), backed by the same `admin.operator.*` cmdsys verbs.

**Architecture:** The cmdsys layer gains a `Secret` field-schema flag (driven by `cmd:"secret"` tag). The console adapter intercepts secret fields and TTY-prompts (no echo) via `golang.org/x/term`; HTTP/admin-UI passes secrets in the JSON request body. Four new verbs (`admin.operator.create`/`delete`/`password`/`list`) live in `pkg/admin/operator_commands.go` and are auto-registered on `NewServer`. The admin UI grows a `/users` route that lists operators and triggers the same verbs via `/admin/api/commands/admin.operator.*` — `ArgsModal.svelte` learns to render `type="password"` for schema entries with `secret=true`. Guardrails (no self-delete, no delete-the-last-operator) live in the handler so both surfaces inherit them. The convention "admin features must be cmdsys verbs first, UI is a second surface" gets a permanent home in CLAUDE.md.

**Tech Stack:** Go 1.22+ (`pkg/cmdsys`, `pkg/admin`, `pkg/engine`), `golang.org/x/term` (re-added), Svelte 5 + Vite + Tailwind v4 (`web-admin/`), `bun` for the SPA build.

---

## File Structure

**Created:**
- `pkg/admin/operator_commands.go` — registers admin.operator.create/delete/password/list with the cmdsys registry. Args + Result structs + handlers + register helper.
- `pkg/admin/operator_commands_test.go` — unit tests against `persisttest.AdminOperatorRepoMock`.
- `web-admin/src/routes/users.svelte` — Users page (table + add modal + per-row drawer actions).
- `web-admin/src/components/UserDrawer.svelte` — per-row drawer with rotate-password / delete buttons.

**Modified:**
- `pkg/cmdsys/schema.go` — add `Secret bool` field to `FieldSchema`; populate from `cmd:"secret"` tag.
- `pkg/cmdsys/schema_test.go` — cover the new `Secret` tag.
- `pkg/cmdsys/help.go` — print secret fields with a `(secret, will prompt)` annotation in help output.
- `pkg/engine/console_cmdsys.go` — when invoking a verb whose Args has any `Secret=true` field, parse the non-secret prefix from the typed args, prompt each secret field via TTY (no echo), then re-invoke. Skip prompt when the command was invoked with `--json` (UI/scripted path).
- `pkg/admin/admin.go` — call `RegisterOperatorCommands(opts.Registry, opts.OperatorRepo)` from `NewServer`.
- `web-admin/src/components/ArgsModal.svelte` — render `<input type="password">` when `field.secret === true`.
- `web-admin/src/components/Sidebar.svelte` — add a "Users" entry under the Admin section.
- `web-admin/src/app.svelte` — register `/users` route alongside existing routes.
- `CLAUDE.md` — (a) update Command palette description to clarify it is search-only (no commands); (b) add the "admin features must be cmdsys verbs first" convention near the admin dashboard section; (c) document the four new verbs + Users page.
- `go.mod` / `go.sum` — `golang.org/x/term` re-appears (drop the previous tidy that removed it).

---

## Conventions baked into this plan

- **Verb names:** `admin.operator.create`, `admin.operator.delete`, `admin.operator.password`, `admin.operator.list`. Args/Result struct names follow Go conventions (`adminOperatorCreateArgs`, etc.) and are unexported.
- **Caller identity:** the admin HTTP path stamps `Caller.ID = <operator username>` (already done in [pkg/admin/middleware.go](pkg/admin/middleware.go)); the console path uses `operatorCaller` whose ID is fixed (`"console"`, see [pkg/engine/console_cmdsys.go:15](pkg/engine/console_cmdsys.go#L15)). Self-delete protection compares `caller.ID == target.Username` — for the console caller this never matches an operator row, so console retains the privilege to delete itself out (which is fine; the last-operator guard still applies).
- **Grants:** the four verbs share capability `admin.operator` — only operators with grant `admin.*` or `*.*` can call them.
- **Username lowercasing:** every handler `strings.ToLower(args.Username)` before touching the repo. Matches the lowercased-by-contract convention from `persist.AdminOperatorRepository`.
- **Secret field protocol:** struct tag `cmd:"secret"` means "do not accept this value from CLI tokens; prompt via TTY in the console path." HTTP/JSON request bodies still carry the field plainly — the UI sends it from a `<input type="password">` element.
- **Doc note location:** the "admin features must be cmdsys verbs first" rule belongs in the **Admin dashboard** paragraph of CLAUDE.md (the section that already describes how the admin server is wired). It is a project-wide convention, not a one-shot reminder.

---

## Phase A — Schema + console TTY prompt

### Task 1: Add `Secret` to `cmdsys.FieldSchema`

**Files:**
- Modify: `pkg/cmdsys/schema.go`
- Modify: `pkg/cmdsys/schema_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/cmdsys/schema_test.go`:

```go
func TestSchemaOf_SecretTagSetsFlag(t *testing.T) {
	type args struct {
		Username string `cmd:"help=name"`
		Password string `cmd:"secret,help=login password"`
	}
	s, err := SchemaOf(args{})
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	byName := map[string]FieldSchema{}
	for _, f := range s.Fields {
		byName[f.Name] = f
	}
	if byName["Username"].Secret {
		t.Errorf("Username.Secret = true; want false")
	}
	if !byName["Password"].Secret {
		t.Errorf("Password.Secret = false; want true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/cmdsys/ -run TestSchemaOf_SecretTagSetsFlag -v`
Expected: FAIL — `byName["Password"].Secret` is undefined or zero (no `Secret` field on `FieldSchema` yet).

- [ ] **Step 3: Add the field**

In `pkg/cmdsys/schema.go`, change `FieldSchema`:

```go
type FieldSchema struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Required  bool     `json:"required"`
	NamedOnly bool     `json:"named_only"`
	Default   string   `json:"default"`
	Enum      []string `json:"enum"`
	Help      string   `json:"help,omitempty"`
	Rest      bool     `json:"rest,omitempty"`
	Complete  string   `json:"complete,omitempty"`
	// Secret marks the field as a sensitive value that should not be
	// echoed or stored in shell history. The console adapter prompts
	// for these fields via TTY (no echo) instead of accepting them
	// from CLI tokens; the HTTP/admin-UI path passes them in the JSON
	// request body and surfaces a password-type form field via
	// ArgsModal.svelte. Mark with cmd:"secret".
	Secret bool `json:"secret,omitempty"`
}
```

In `schemaFields`, populate the flag. Find:

```go
		fs.Required = !containsFlag(tag, "optional")
		fs.NamedOnly = containsFlag(tag, "named-only")
		fs.Rest = containsFlag(tag, "rest")
```

Add after it:

```go
		fs.Secret = containsFlag(tag, "secret")
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/cmdsys/ -run TestSchemaOf_SecretTagSetsFlag -v`
Expected: PASS.

- [ ] **Step 5: Run the full cmdsys suite — no regressions**

Run: `go test ./pkg/cmdsys/... -count=1`
Expected: all PASS. The schema-hash tests must still pass because `Secret` is excluded from `encodeFields` (which only writes `Name:Kind,`).

- [ ] **Step 6: Commit**

```bash
git add pkg/cmdsys/schema.go pkg/cmdsys/schema_test.go
git commit -m "cmdsys: add Secret flag to FieldSchema (cmd:\"secret\" tag)"
```

---

### Task 2: Help text annotates secret fields

**Files:**
- Modify: `pkg/cmdsys/help.go`
- Modify: `pkg/cmdsys/help_test.go`

Help output today renders each field's name + kind + help text. Secret fields should pick up a `(secret, prompted)` annotation so `?` help in the console makes clear the user can't pass the value on the command line.

- [ ] **Step 1: Write the failing test**

Append to `pkg/cmdsys/help_test.go`:

```go
func TestRenderFieldLine_SecretShowsAnnotation(t *testing.T) {
	var b strings.Builder
	renderFieldLine(&b, FieldSchema{
		Name: "Password",
		Kind: "string",
		Help: "operator password",
		Secret: true,
		Required: true,
	}, false)
	out := b.String()
	if !strings.Contains(out, "(secret, prompted)") {
		t.Errorf("help output missing '(secret, prompted)' annotation: %q", out)
	}
}
```

Add `"strings"` to the test file's imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/cmdsys/ -run TestRenderFieldLine_SecretShowsAnnotation -v`
Expected: FAIL (no "(secret, prompted)" in output).

- [ ] **Step 3: Add the annotation**

Open `pkg/cmdsys/help.go` and find `renderFieldLine`. Add the annotation in a place that's clearly visible — typically right after the kind. Concrete patch: at the end of `renderFieldLine` (just before the function closes), insert:

```go
	if f.Secret {
		b.WriteString(" (secret, prompted)")
	}
```

If the function ends with a newline write, insert BEFORE the newline. Read the existing function to find the right insertion point and keep the per-line shape intact.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/cmdsys/ -run TestRenderFieldLine_SecretShowsAnnotation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/cmdsys/help.go pkg/cmdsys/help_test.go
git commit -m "cmdsys: render '(secret, prompted)' annotation in help output"
```

---

### Task 3: Console TTY prompt for secret fields

**Files:**
- Modify: `pkg/engine/console_cmdsys.go`
- Modify: `go.mod` / `go.sum` (re-add `golang.org/x/term`)

The console adapter currently passes a raw arg string to `Dispatcher.Invoke`. When the looked-up command has any secret fields, we instead:
1. Parse the typed args from the non-secret tokens
2. Prompt for each secret field via TTY (no echo)
3. Marshal the resulting args struct and invoke with the populated args

The cmdsys Dispatcher already accepts both a raw string and a typed struct (see `cmdsys.NewArgs` + the typed Invoke path used by HTTP); we route through the typed path for secret-bearing verbs.

- [ ] **Step 1: Re-add the `golang.org/x/term` dependency**

Run: `go get golang.org/x/term@latest`
Expected: module added back to `go.mod`/`go.sum`.

- [ ] **Step 2: Add the TTY prompt helper**

Open `pkg/engine/console_cmdsys.go`. At the bottom of the file, add:

```go
// promptSecretFields walks the schema for any Secret fields and reads
// their values from the TTY without echo. The returned map is keyed by
// FieldSchema.Name. Returns nil + nil error if no secret fields exist.
//
// Bypasses readline directly via golang.org/x/term — readline's prompt
// is paused while we read, then the next Readline() iteration redraws.
// Non-TTY callers (tests, piped stdin) fall through to a plain
// bufio.Scanner read; this matches the legacy promptAndPrintAdminHash
// behavior from the removed --admin-hash-password flag.
func promptSecretFields(out io.Writer, schema cmdsys.Schema) (map[string]string, error) {
	var secrets []cmdsys.FieldSchema
	for _, f := range schema.Fields {
		if f.Secret {
			secrets = append(secrets, f)
		}
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(secrets))
	for _, f := range secrets {
		fmt.Fprintf(out, "%s: ", f.Name)
		var pwd []byte
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(out)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", f.Name, err)
			}
			pwd = b
		} else {
			s := bufio.NewScanner(os.Stdin)
			if !s.Scan() {
				return nil, fmt.Errorf("read %s: no input", f.Name)
			}
			pwd = []byte(strings.TrimSpace(s.Text()))
		}
		if len(pwd) == 0 {
			return nil, fmt.Errorf("%s required", f.Name)
		}
		result[f.Name] = string(pwd)
	}
	return result, nil
}
```

Add imports at the top of the file (only the missing ones):

```go
	"bufio"
	"io"
	"os"

	"golang.org/x/term"
```

- [ ] **Step 3: Hook the prompt into `Dispatch`**

Find the existing `Dispatch` method body (around line 130). Today it builds `argsRest` and calls `Dispatcher.Invoke(ctx, operatorCaller, verb, argsRest)`. Replace the dispatch call with:

```go
	// If the resolved verb has any secret fields, parse the non-secret
	// prefix into a typed args struct, prompt for the secret values via
	// TTY, fill them in, and invoke with the typed args. Otherwise fall
	// back to the legacy string-args path.
	cmd, found := a.Registry.Lookup(verb)
	if !found {
		// Fall through to the dispatcher so it produces ErrUnknownVerb.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := a.Dispatcher.Invoke(ctx, operatorCaller, verb, argsRest)
		if err != nil {
			if err == cmdsys.ErrUnknownVerb {
				return ""
			}
			return fmt.Sprintf("  error: %v\n", err)
		}
		return renderDispatchResult(res, asJSON)
	}
	schema, _ := cmdsys.SchemaOf(cmd.Args)
	hasSecret := false
	for _, f := range schema.Fields {
		if f.Secret {
			hasSecret = true
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res cmdsys.Result
	var err error
	if !hasSecret {
		res, err = a.Dispatcher.Invoke(ctx, operatorCaller, verb, argsRest)
	} else {
		// Parse the non-secret tokens into a typed args value.
		typed, perr := cmdsys.NewArgs(cmd)
		if perr != nil {
			return fmt.Sprintf("  error: args schema: %v\n", perr)
		}
		if typed != nil {
			if perr := cmdsys.ParseInto(cmd, argsRest, typed); perr != nil {
				return fmt.Sprintf("  error: parse args: %v\n", perr)
			}
		}
		secrets, perr := promptSecretFields(a.out(), schema)
		if perr != nil {
			return fmt.Sprintf("  error: %v\n", perr)
		}
		if err := setStringFields(typed, secrets); err != nil {
			return fmt.Sprintf("  error: %v\n", err)
		}
		res, err = a.Dispatcher.Invoke(ctx, operatorCaller, verb, typed)
	}
	if err != nil {
		if err == cmdsys.ErrUnknownVerb {
			return ""
		}
		return fmt.Sprintf("  error: %v\n", err)
	}
	return renderDispatchResult(res, asJSON)
```

This requires three new pieces, all in `pkg/engine/console_cmdsys.go`:

1. `a.out()` — returns the io.Writer to draw the prompt onto. Add this method:

```go
// out returns the console stdout writer so prompts draw through readline
// safely. Tests can override by providing a wrapper.
func (a *cmdsysAdapter) out() io.Writer {
	if a.stdout != nil {
		return a.stdout
	}
	return os.Stdout
}
```

And add `stdout io.Writer` field to the `cmdsysAdapter` struct. The console wires it via:

```go
func newConsoleWith(gameLog *logger.Logger, adapter *cmdsysAdapter) *Console {
	// ... existing setup ...
	c := &Console{ ... }
	// After rl is created:
	adapter.stdout = rl.Stdout()
```

(Edit the existing `newConsoleWith` constructor to wire `adapter.stdout` from `rl.Stdout()` right after the `rl` is built.)

2. `setStringFields(typed any, values map[string]string) error` — reflect to set string fields on the args struct by name. Add as a free function at the bottom of the file:

```go
// setStringFields uses reflection to assign values into the named
// string fields of typed (a pointer to a struct). Returns an error
// if a name is unknown or the field isn't a string. Used by the
// secret-prompt path to fill prompted values into the args struct.
func setStringFields(typed any, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	rv := reflect.ValueOf(typed)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("setStringFields: expected pointer to struct, got %T", typed)
	}
	rs := rv.Elem()
	for name, val := range values {
		f := rs.FieldByName(name)
		if !f.IsValid() {
			return fmt.Errorf("setStringFields: unknown field %q", name)
		}
		if f.Kind() != reflect.String {
			return fmt.Errorf("setStringFields: field %q is not a string", name)
		}
		f.SetString(val)
	}
	return nil
}
```

Add `"reflect"` to the imports if missing.

3. `cmdsys.ParseInto` — this needs to exist. Check `pkg/cmdsys/parser.go` for an existing function that parses a raw string into a typed pointer. If `ParseInto` doesn't exist by that name, look for the equivalent (the HTTP path does its own JSON decode, but the dispatcher's typed-string Invoke path parses internally). If no public parser exposed, add one:

In `pkg/cmdsys/parser.go`, near the existing parsing helpers, add:

```go
// ParseInto parses raw arg tokens into the typed args pointer. Used by
// callers that want to fill some fields programmatically (e.g. the
// console's secret-prompt path) and need the rest parsed from a raw
// string. Empty raw is a no-op (returns nil).
func ParseInto(cmd Command, raw string, args any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := ParseArgs(cmd, raw)
	if err != nil {
		return err
	}
	// Copy fields from parsed into args via reflection. Both are
	// pointers to the same struct type.
	srcV := reflect.ValueOf(parsed).Elem()
	dstV := reflect.ValueOf(args).Elem()
	for i := 0; i < srcV.NumField(); i++ {
		dstV.Field(i).Set(srcV.Field(i))
	}
	return nil
}
```

(`ParseArgs` is the existing parser used by the dispatcher — verify the actual name by grepping `pkg/cmdsys/parser.go`. If the exposed name differs, update the call.)

Add `"reflect"` and `"strings"` to imports if missing.

- [ ] **Step 4: Smoke-build**

Run: `go vet ./pkg/cmdsys/... ./pkg/engine/...`
Expected: no output.

- [ ] **Step 5: Write a unit test for the console secret-prompt path**

Append to `pkg/engine/console_cmdsys_test.go`:

```go
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
		Args:        &args{},
		Result:      &result{},
		Route:       cmdsys.RouteLocal,
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			a := raw.(*args)
			captured = a.Password
			return &result{GotPassword: a.Password}, nil
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
```

- [ ] **Step 6: Run the new test**

Run: `go test ./pkg/engine/ -run TestDispatch_SecretFieldPromptsAndPopulates -v`
Expected: PASS.

- [ ] **Step 7: Run engine + cmdsys suites**

Run: `go test ./pkg/cmdsys/... ./pkg/engine/... -count=1`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/engine/console_cmdsys.go pkg/engine/console_cmdsys_test.go pkg/engine/console.go pkg/cmdsys/parser.go go.mod go.sum
git commit -m "engine/console: TTY-prompt secret fields via term.ReadPassword"
```

(Only include `pkg/engine/console.go` if you ended up editing `newConsoleWith` to wire `adapter.stdout`. Only include `pkg/cmdsys/parser.go` if you added the new `ParseInto` helper.)

---

## Phase B — Admin operator verbs (backend)

### Task 4: Args + Result structs + handler skeletons

**Files:**
- Create: `pkg/admin/operator_commands.go`

This task lays down the verb shapes with empty/stub handlers so Task 5 can fill in behavior with TDD.

- [ ] **Step 1: Write the skeleton**

Create `pkg/admin/operator_commands.go`:

```go
package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

// adminOperatorCreateArgs creates a new admin operator. Password is a
// secret field: the console prompts via TTY (no echo); the admin UI
// sends it from a password form field over JSON.
type adminOperatorCreateArgs struct {
	Username string   `cmd:"help=operator username (lowercased)"`
	Password string   `cmd:"secret,help=initial password"`
	Grants   []string `cmd:"optional,help=grant patterns; default *.*"`
}

type adminOperatorCreateResult struct {
	Username string   `json:"username"`
	Grants   []string `json:"grants"`
}

// adminOperatorDeleteArgs removes an operator. Two guardrails apply
// (see handler): cannot delete the last operator, and the HTTP caller
// cannot delete itself.
type adminOperatorDeleteArgs struct {
	Username string `cmd:"help=operator username"`
}

type adminOperatorDeleteResult struct {
	Username string `json:"username"`
}

// adminOperatorPasswordArgs rotates the password hash for an existing
// operator. The new password is a secret field.
type adminOperatorPasswordArgs struct {
	Username string `cmd:"help=operator username"`
	Password string `cmd:"secret,help=new password"`
}

type adminOperatorPasswordResult struct {
	Username string `json:"username"`
}

// adminOperatorListArgs takes no input.
type adminOperatorListArgs struct{}

// adminOperatorListResult holds the table the UI renders. Field tags
// match what /admin/api/commands/admin.operator.list will JSON-encode.
type adminOperatorListResult struct {
	Operators []adminOperatorListRow `json:"operators"`
}

type adminOperatorListRow struct {
	Username  string   `json:"username"`
	Grants    []string `json:"grants"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// RegisterOperatorCommands registers the four admin.operator.* verbs.
// Called by NewServer when OperatorRepo is non-nil. Returns an error
// if any verb already exists in the registry.
func RegisterOperatorCommands(reg *cmdsys.Registry, repo persist.AdminOperatorRepository) error {
	if reg == nil || repo == nil {
		return errors.New("admin: RegisterOperatorCommands requires non-nil registry and repo")
	}
	cmds := []cmdsys.Command{
		{
			Verb:        "admin.operator.create",
			Capability:  "admin.operator",
			Description: "create a new admin operator",
			Args:        &adminOperatorCreateArgs{},
			Result:      &adminOperatorCreateResult{},
			Route:       cmdsys.RouteLocal,
			Handler:     makeAdminOperatorCreate(repo),
		},
		{
			Verb:        "admin.operator.delete",
			Capability:  "admin.operator",
			Description: "delete an admin operator (cannot delete last or self)",
			Args:        &adminOperatorDeleteArgs{},
			Result:      &adminOperatorDeleteResult{},
			Route:       cmdsys.RouteLocal,
			Handler:     makeAdminOperatorDelete(repo),
		},
		{
			Verb:        "admin.operator.password",
			Capability:  "admin.operator",
			Description: "rotate an admin operator's password",
			Args:        &adminOperatorPasswordArgs{},
			Result:      &adminOperatorPasswordResult{},
			Route:       cmdsys.RouteLocal,
			Handler:     makeAdminOperatorPassword(repo),
		},
		{
			Verb:        "admin.operator.list",
			Capability:  "admin.operator",
			Description: "list all admin operators",
			Args:        &adminOperatorListArgs{},
			Result:      &adminOperatorListResult{},
			Route:       cmdsys.RouteLocal,
			Handler:     makeAdminOperatorList(repo),
		},
	}
	for _, c := range cmds {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("register %s: %w", c.Verb, err)
		}
	}
	return nil
}

func makeAdminOperatorCreate(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		return nil, errors.New("not yet implemented")
	}
}

func makeAdminOperatorDelete(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		return nil, errors.New("not yet implemented")
	}
}

func makeAdminOperatorPassword(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		return nil, errors.New("not yet implemented")
	}
}

func makeAdminOperatorList(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		return nil, errors.New("not yet implemented")
	}
}

// suppress unused-import errors before the handler bodies land
var _ = strings.ToLower
var _ = auth.HashPassword
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/admin/...`
Expected: no output. (The unused-import sinks at the bottom satisfy the `strings` and `auth` imports until Task 5 fills the handlers.)

- [ ] **Step 3: Commit**

```bash
git add pkg/admin/operator_commands.go
git commit -m "admin: scaffold admin.operator.* command shapes"
```

---

### Task 5: Implement create + list + password handlers (TDD)

**Files:**
- Modify: `pkg/admin/operator_commands.go`
- Create: `pkg/admin/operator_commands_test.go`

- [ ] **Step 1: Write failing tests for `create`**

Create `pkg/admin/operator_commands_test.go`:

```go
package admin

import (
	"context"
	"errors"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/persist"
	"github.com/zenion/mmoserver/pkg/persist/persisttest"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

func adminOpEnv(callerID string) (*cmdsys.Env, context.Context) {
	return &cmdsys.Env{Caller: cmdsys.Caller{ID: callerID, Source: cmdsys.SourceAdminHTTP}}, context.Background()
}

func TestAdminOperatorCreate_Success(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	h := makeAdminOperatorCreate(repo)
	env, ctx := adminOpEnv("admin")

	res, err := h(ctx, env, &adminOperatorCreateArgs{
		Username: "Alice",
		Password: "s3cret-pass",
		Grants:   []string{"cell.*"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := res.(*adminOperatorCreateResult)
	if r.Username != "alice" {
		t.Errorf("Username = %q, want lowercase 'alice'", r.Username)
	}
	if len(r.Grants) != 1 || r.Grants[0] != "cell.*" {
		t.Errorf("Grants = %v, want [cell.*]", r.Grants)
	}
	got, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	ok, verr := auth.VerifyPassword("s3cret-pass", got.PasswordHash)
	if verr != nil || !ok {
		t.Errorf("password hash does not verify against 's3cret-pass'")
	}
}

func TestAdminOperatorCreate_DefaultsGrantsToWildcard(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	h := makeAdminOperatorCreate(repo)
	env, ctx := adminOpEnv("admin")
	res, err := h(ctx, env, &adminOperatorCreateArgs{Username: "bob", Password: "pw"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	r := res.(*adminOperatorCreateResult)
	if len(r.Grants) != 1 || r.Grants[0] != "*.*" {
		t.Errorf("default Grants = %v, want [*.*]", r.Grants)
	}
}

func TestAdminOperatorCreate_DuplicateRejected(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}
	h := makeAdminOperatorCreate(repo)
	env, ctx := adminOpEnv("admin")
	if _, err := h(ctx, env, &adminOperatorCreateArgs{Username: "alice", Password: "pw"}); err == nil {
		t.Fatal("expected duplicate-create error, got nil")
	}
}

func TestAdminOperatorCreate_RejectsEmptyPassword(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	h := makeAdminOperatorCreate(repo)
	env, ctx := adminOpEnv("admin")
	if _, err := h(ctx, env, &adminOperatorCreateArgs{Username: "alice", Password: ""}); err == nil {
		t.Fatal("expected empty-password error, got nil")
	}
}

func TestAdminOperatorList_ReturnsSorted(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	for _, name := range []string{"zoe", "alice", "bob"} {
		_ = repo.Create(context.Background(), &persist.AdminOperator{Username: name, PasswordHash: "x", Grants: []string{"*.*"}})
	}
	h := makeAdminOperatorList(repo)
	env, ctx := adminOpEnv("admin")
	res, err := h(ctx, env, &adminOperatorListArgs{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	r := res.(*adminOperatorListResult)
	if len(r.Operators) != 3 {
		t.Fatalf("len = %d, want 3", len(r.Operators))
	}
	want := []string{"alice", "bob", "zoe"}
	for i, w := range want {
		if r.Operators[i].Username != w {
			t.Errorf("[%d] = %q, want %q", i, r.Operators[i].Username, w)
		}
	}
}

func TestAdminOperatorPassword_Rotates(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "old"})
	h := makeAdminOperatorPassword(repo)
	env, ctx := adminOpEnv("admin")
	if _, err := h(ctx, env, &adminOperatorPasswordArgs{Username: "Alice", Password: "newpass"}); err != nil {
		t.Fatalf("Password: %v", err)
	}
	got, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	ok, _ := auth.VerifyPassword("newpass", got.PasswordHash)
	if !ok {
		t.Errorf("new password does not verify")
	}
}

func TestAdminOperatorPassword_UnknownUser(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	h := makeAdminOperatorPassword(repo)
	env, ctx := adminOpEnv("admin")
	_, err := h(ctx, env, &adminOperatorPasswordArgs{Username: "ghost", Password: "pw"})
	if !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests — confirm they fail**

Run: `go test ./pkg/admin/ -run TestAdminOperator -v`
Expected: every test FAILS with "not yet implemented" (stub error).

- [ ] **Step 3: Implement the handlers**

Replace the four `makeAdminOperator*` stubs in `pkg/admin/operator_commands.go`:

```go
func makeAdminOperatorCreate(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		args := rawArgs.(*adminOperatorCreateArgs)
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, errors.New("username required")
		}
		if args.Password == "" {
			return nil, errors.New("password required")
		}
		hash, err := auth.HashPassword(args.Password, auth.DefaultArgonParams())
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		grants := args.Grants
		if len(grants) == 0 {
			grants = []string{"*.*"}
		}
		if err := repo.Create(ctx, &persist.AdminOperator{
			Username:     username,
			PasswordHash: hash,
			Grants:       grants,
		}); err != nil {
			return nil, err
		}
		return &adminOperatorCreateResult{Username: username, Grants: grants}, nil
	}
}

func makeAdminOperatorList(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		ops, err := repo.List(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]adminOperatorListRow, len(ops))
		for i, op := range ops {
			rows[i] = adminOperatorListRow{
				Username:  op.Username,
				Grants:    op.Grants,
				CreatedAt: op.CreatedAt.UTC().Format(time.RFC3339),
				UpdatedAt: op.UpdatedAt.UTC().Format(time.RFC3339),
			}
		}
		return &adminOperatorListResult{Operators: rows}, nil
	}
}

func makeAdminOperatorPassword(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		args := rawArgs.(*adminOperatorPasswordArgs)
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, errors.New("username required")
		}
		if args.Password == "" {
			return nil, errors.New("password required")
		}
		hash, err := auth.HashPassword(args.Password, auth.DefaultArgonParams())
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		if err := repo.UpdatePasswordHash(ctx, username, hash); err != nil {
			return nil, err
		}
		return &adminOperatorPasswordResult{Username: username}, nil
	}
}
```

Keep the delete handler stubbed for now (Task 6 covers it).

Add `"time"` to the imports. Delete the temporary `var _ = strings.ToLower` / `var _ = auth.HashPassword` sinks at the bottom of the file — they're no longer needed.

- [ ] **Step 4: Run tests — confirm everything except delete passes**

Run: `go test ./pkg/admin/ -run TestAdminOperator -v`
Expected: All `TestAdminOperatorCreate_*`, `TestAdminOperatorList_*`, `TestAdminOperatorPassword_*` PASS. (Any delete-related tests are still stubbed — Task 6 covers them.)

- [ ] **Step 5: Commit**

```bash
git add pkg/admin/operator_commands.go pkg/admin/operator_commands_test.go
git commit -m "admin: implement operator create/list/password handlers"
```

---

### Task 6: Implement delete handler with guardrails

**Files:**
- Modify: `pkg/admin/operator_commands.go`
- Modify: `pkg/admin/operator_commands_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/admin/operator_commands_test.go`:

```go
func TestAdminOperatorDelete_Success(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"})
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "bob", PasswordHash: "x"})
	h := makeAdminOperatorDelete(repo)
	env, ctx := adminOpEnv("admin")
	if _, err := h(ctx, env, &adminOperatorDeleteArgs{Username: "Bob"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByUsername(ctx, "bob"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestAdminOperatorDelete_RejectsLastOperator(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"})
	h := makeAdminOperatorDelete(repo)
	env, ctx := adminOpEnv("admin")
	_, err := h(ctx, env, &adminOperatorDeleteArgs{Username: "alice"})
	if err == nil {
		t.Fatal("expected error deleting last operator, got nil")
	}
	if got, _ := repo.GetByUsername(ctx, "alice"); got == nil {
		t.Error("operator was deleted despite last-operator guard")
	}
}

func TestAdminOperatorDelete_RejectsSelf(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"})
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "bob", PasswordHash: "x"})
	h := makeAdminOperatorDelete(repo)
	// Caller is alice; deleting self should fail even though count > 1.
	env, ctx := adminOpEnv("alice")
	_, err := h(ctx, env, &adminOperatorDeleteArgs{Username: "Alice"})
	if err == nil {
		t.Fatal("expected error deleting self, got nil")
	}
}

func TestAdminOperatorDelete_ConsoleCanDeleteAnyone(t *testing.T) {
	// Console caller has ID "console", which won't match an operator
	// username — so self-delete guard never fires. Last-operator guard
	// still applies.
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"})
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "bob", PasswordHash: "x"})
	h := makeAdminOperatorDelete(repo)
	env := &cmdsys.Env{Caller: cmdsys.Caller{ID: "console", Source: cmdsys.SourceConsole}}
	if _, err := h(context.Background(), env, &adminOperatorDeleteArgs{Username: "alice"}); err != nil {
		t.Fatalf("console-driven delete failed: %v", err)
	}
}

func TestAdminOperatorDelete_UnknownUser(t *testing.T) {
	repo := persisttest.NewAdminOperatorRepoMock()
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "alice", PasswordHash: "x"})
	_ = repo.Create(context.Background(), &persist.AdminOperator{Username: "bob", PasswordHash: "x"})
	h := makeAdminOperatorDelete(repo)
	env, ctx := adminOpEnv("admin")
	_, err := h(ctx, env, &adminOperatorDeleteArgs{Username: "ghost"})
	if !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("expected ErrNotFound for unknown user, got %v", err)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run: `go test ./pkg/admin/ -run TestAdminOperatorDelete -v`
Expected: every TestAdminOperatorDelete_* FAILS with "not yet implemented".

- [ ] **Step 3: Implement the delete handler**

Replace `makeAdminOperatorDelete` in `pkg/admin/operator_commands.go`:

```go
func makeAdminOperatorDelete(repo persist.AdminOperatorRepository) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, rawArgs any) (any, error) {
		args := rawArgs.(*adminOperatorDeleteArgs)
		username := strings.ToLower(strings.TrimSpace(args.Username))
		if username == "" {
			return nil, errors.New("username required")
		}
		// Self-delete guard: HTTP caller ID is the operator username.
		// Console caller ID is "console" which won't collide unless
		// someone creates an operator named "console".
		if env != nil && strings.ToLower(env.Caller.ID) == username {
			return nil, errors.New("cannot delete yourself")
		}
		// Confirm the user exists before checking the last-op guard,
		// so unknown-user returns ErrNotFound (not a misleading
		// last-operator error).
		if _, err := repo.GetByUsername(ctx, username); err != nil {
			return nil, err
		}
		n, err := repo.Count(ctx)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, errors.New("cannot delete the last admin operator")
		}
		if err := repo.Delete(ctx, username); err != nil {
			return nil, err
		}
		return &adminOperatorDeleteResult{Username: username}, nil
	}
}
```

- [ ] **Step 4: Run tests — confirm passing**

Run: `go test ./pkg/admin/ -run TestAdminOperatorDelete -v`
Expected: every test PASSES.

- [ ] **Step 5: Run the full admin suite**

Run: `go test ./pkg/admin/... -count=1`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/admin/operator_commands.go pkg/admin/operator_commands_test.go
git commit -m "admin: operator delete handler with last-op + self-delete guards"
```

---

### Task 7: Wire `RegisterOperatorCommands` into `NewServer`

**Files:**
- Modify: `pkg/admin/admin.go`
- Modify: `pkg/admin/api_auth_test.go` (test util might need touching if NewServer signature shifts; unlikely)

- [ ] **Step 1: Add registration call**

In `pkg/admin/admin.go`'s `NewServer`, just after the `seedDefaultOperator` call (and before `return s`), add:

```go
	if opts.Registry != nil && opts.OperatorRepo != nil {
		if err := RegisterOperatorCommands(opts.Registry, opts.OperatorRepo); err != nil {
			if opts.Logger != nil {
				opts.Logger.Log("admin", "register operator commands: %v", err)
			}
		}
	}
```

- [ ] **Step 2: Add an end-to-end test that round-trips a verb via the registry**

Append to `pkg/admin/api_auth_test.go`:

```go
func TestNewServer_RegistersOperatorCommands(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()

	reg := cmdsys.NewRegistry()
	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		Registry:     reg,
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	for _, verb := range []string{
		"admin.operator.create",
		"admin.operator.delete",
		"admin.operator.password",
		"admin.operator.list",
	} {
		if _, ok := reg.Lookup(verb); !ok {
			t.Errorf("verb %q not registered", verb)
		}
	}
}
```

Add `"github.com/zenion/mmoserver/pkg/cmdsys"` to imports if not present.

- [ ] **Step 3: Run admin tests**

Run: `go test ./pkg/admin/... -count=1`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/admin/admin.go pkg/admin/api_auth_test.go
git commit -m "admin: register admin.operator.* verbs on NewServer"
```

---

## Phase C — Admin UI

### Task 8: Render password inputs in `ArgsModal.svelte`

**Files:**
- Modify: `web-admin/src/components/ArgsModal.svelte`

The existing `ArgsModal.svelte` reads `argsSchema.fields` from `/admin/api/commands/<verb>` and renders one input per field. Today it picks input type from `field.kind` (string, int, bool, etc.). We add a tiny check: when `field.secret === true`, render `type="password"` regardless of kind.

- [ ] **Step 1: Inspect the current input-renderer**

Run: `grep -n "type=\|input" web-admin/src/components/ArgsModal.svelte | head -30`

Locate the existing `<input>` element (most likely a single template with bindings against `field.kind`). Identify the place where the input's `type` attribute is computed.

- [ ] **Step 2: Add the secret check**

Find the input element and change its `type` attribute to give secret-fields priority. Concretely, if there's currently something like:

```svelte
<input type={inputTypeFor(field.kind)} ... />
```

change it to:

```svelte
<input type={field.secret ? "password" : inputTypeFor(field.kind)} ... />
```

If the template uses a direct conditional (e.g. `{#if field.kind === "bool"}<input type="checkbox" />{:else}<input type="text" />{/if}`), insert a `{:else if field.secret}<input type="password" bind:value={values[field.name]} />` branch BEFORE the catch-all string branch.

If the existing code calls a helper `inputTypeFor(kind)`, update the helper to take the full field object instead and check `field.secret` first.

- [ ] **Step 3: Test that the schema annotation actually flows through**

Add a unit test for ArgsModal if the file has a sibling `*.test.ts`. Otherwise, smoke-test via the running dev server in Task 9 (the Users page exercises this path).

- [ ] **Step 4: Build the SPA to confirm no TS errors**

Run: `cd web-admin && bun run build`
Expected: build succeeds; output goes into `pkg/admin/static/dist/`.

- [ ] **Step 5: Commit**

```bash
git add web-admin/src/components/ArgsModal.svelte pkg/admin/static/dist/
git commit -m "web-admin: render password input for secret schema fields"
```

(The `pkg/admin/static/dist/` directory is the embedded SPA the Go binary serves. Committing the build output keeps the binary self-contained — same pattern as existing admin work.)

---

### Task 9: Users page route + sidebar entry

**Files:**
- Create: `web-admin/src/routes/users.svelte`
- Create: `web-admin/src/components/UserDrawer.svelte`
- Modify: `web-admin/src/components/Sidebar.svelte`
- Modify: `web-admin/src/app.svelte`

The page follows the pattern of the existing `/players` route: `DataTable` of operators + per-row drawer + an "Add operator" button at the top that opens `ArgsModal` for `admin.operator.create`.

- [ ] **Step 1: Read sibling routes to learn the pattern**

```bash
ls web-admin/src/routes/
```

The relevant files to skim: `players.svelte` (closest analogue: table + drawer + ops), `audit.svelte` (read-only list), and `logs.svelte`. Read `players.svelte` carefully to internalize the `DataTable` + `PlayerDrawer` pattern.

- [ ] **Step 2: Write the route file**

Create `web-admin/src/routes/users.svelte`:

```svelte
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import DataTable from "../components/DataTable.svelte";
  import ArgsModal from "../components/ArgsModal.svelte";
  import UserDrawer from "../components/UserDrawer.svelte";
  import { invokeCommand, fetchCommandSchema } from "../lib/api";

  type Operator = {
    username: string;
    grants: string[];
    createdAt: string;
    updatedAt: string;
  };

  let operators: Operator[] = $state([]);
  let loadError = $state<string | null>(null);
  let loading = $state(true);
  let selected = $state<Operator | null>(null);
  let createOpen = $state(false);
  let createSchema = $state<any>(null);

  async function reload() {
    loading = true;
    loadError = null;
    try {
      const res = await invokeCommand("admin.operator.list", {});
      operators = (res?.operators ?? []) as Operator[];
    } catch (err: any) {
      loadError = err?.message ?? String(err);
    } finally {
      loading = false;
    }
  }

  async function openCreate() {
    if (!createSchema) {
      createSchema = await fetchCommandSchema("admin.operator.create");
    }
    createOpen = true;
  }

  async function onCreated() {
    createOpen = false;
    await reload();
  }

  async function onMutated() {
    selected = null;
    await reload();
  }

  let intervalId: ReturnType<typeof setInterval> | undefined;
  onMount(() => {
    reload();
    intervalId = setInterval(reload, 10_000);
  });
  onDestroy(() => clearInterval(intervalId));

  const columns = [
    { key: "username", label: "Username", sortable: true },
    {
      key: "grants",
      label: "Grants",
      sortable: false,
      render: (op: Operator) => op.grants.join(", "),
    },
    { key: "createdAt", label: "Created", sortable: true },
    { key: "updatedAt", label: "Updated", sortable: true },
  ];
</script>

<div class="p-6 space-y-4">
  <div class="flex items-center justify-between">
    <h1 class="text-xl font-semibold">Admin Users</h1>
    <button
      class="px-3 py-1.5 bg-blue-600 hover:bg-blue-700 rounded text-sm"
      onclick={openCreate}
    >
      Add operator
    </button>
  </div>

  {#if loadError}
    <div class="text-red-400">{loadError}</div>
  {:else if loading && operators.length === 0}
    <div class="text-[var(--text-dim)]">Loading…</div>
  {:else}
    <DataTable
      rows={operators}
      {columns}
      rowKey={(op: Operator) => op.username}
      onRowClick={(op: Operator) => (selected = op)}
    />
  {/if}
</div>

{#if createOpen && createSchema}
  <ArgsModal
    verb="admin.operator.create"
    schema={createSchema}
    onClose={() => (createOpen = false)}
    onSubmitted={onCreated}
  />
{/if}

{#if selected}
  <UserDrawer
    operator={selected}
    onClose={() => (selected = null)}
    onMutated={onMutated}
  />
{/if}
```

If `fetchCommandSchema` / `invokeCommand` don't yet exist in `web-admin/src/lib/api.ts`, check what helpers ARE there (`grep -n "export" web-admin/src/lib/api.ts`) and use the existing names. The two operations required: GET `/admin/api/commands/admin.operator.list` (returns schema) and POST `/admin/api/commands/admin.operator.list` (invokes). If only one helper exists, use it for both.

- [ ] **Step 3: Write the drawer component**

Create `web-admin/src/components/UserDrawer.svelte`:

```svelte
<script lang="ts">
  import ArgsModal from "./ArgsModal.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import { invokeCommand, fetchCommandSchema } from "../lib/api";

  let { operator, onClose, onMutated } = $props<{
    operator: { username: string; grants: string[]; createdAt: string; updatedAt: string };
    onClose: () => void;
    onMutated: () => void;
  }>();

  let confirmDeleteOpen = $state(false);
  let rotateOpen = $state(false);
  let rotateSchema = $state<any>(null);
  let actionError = $state<string | null>(null);

  async function openRotate() {
    if (!rotateSchema) {
      rotateSchema = await fetchCommandSchema("admin.operator.password");
    }
    rotateOpen = true;
  }

  async function doDelete() {
    actionError = null;
    try {
      await invokeCommand("admin.operator.delete", { Username: operator.username });
      confirmDeleteOpen = false;
      onMutated();
    } catch (err: any) {
      actionError = err?.message ?? String(err);
    }
  }
</script>

<div class="fixed inset-y-0 right-0 w-96 bg-[var(--bg-panel)] border-l border-[var(--border)] shadow-xl p-6 z-30 overflow-y-auto">
  <div class="flex items-center justify-between mb-4">
    <h2 class="text-lg font-semibold">{operator.username}</h2>
    <button class="text-[var(--text-dim)]" onclick={onClose}>✕</button>
  </div>

  <dl class="space-y-2 text-sm">
    <div class="flex justify-between"><dt class="text-[var(--text-dim)]">Grants</dt><dd>{operator.grants.join(", ")}</dd></div>
    <div class="flex justify-between"><dt class="text-[var(--text-dim)]">Created</dt><dd>{operator.createdAt}</dd></div>
    <div class="flex justify-between"><dt class="text-[var(--text-dim)]">Updated</dt><dd>{operator.updatedAt}</dd></div>
  </dl>

  {#if actionError}
    <div class="mt-4 text-red-400 text-sm">{actionError}</div>
  {/if}

  <div class="mt-6 flex gap-2">
    <button class="px-3 py-1.5 bg-blue-600 hover:bg-blue-700 rounded text-sm" onclick={openRotate}>
      Rotate password
    </button>
    <button class="px-3 py-1.5 bg-red-700 hover:bg-red-800 rounded text-sm" onclick={() => (confirmDeleteOpen = true)}>
      Delete
    </button>
  </div>
</div>

{#if rotateOpen && rotateSchema}
  <ArgsModal
    verb="admin.operator.password"
    schema={rotateSchema}
    prefilled={{ Username: operator.username }}
    onClose={() => (rotateOpen = false)}
    onSubmitted={() => { rotateOpen = false; onMutated(); }}
  />
{/if}

{#if confirmDeleteOpen}
  <ConfirmDialog
    title="Delete operator {operator.username}?"
    message="This cannot be undone."
    confirmLabel="Delete"
    onConfirm={doDelete}
    onCancel={() => (confirmDeleteOpen = false)}
  />
{/if}
```

If `ArgsModal`'s `prefilled` prop doesn't exist, fall back to two routes: (a) extend `ArgsModal` to accept a `prefilled?: Record<string, any>` prop that pre-populates inputs and hides fields whose name is in the prefilled map; OR (b) call the verb directly without a modal, prompting for password via a simpler purpose-built modal. Pick whichever requires less code based on what's already there.

If `ConfirmDialog` doesn't exist, look for the pattern used by PlayerDrawer for destructive actions and follow it.

- [ ] **Step 4: Add sidebar entry**

In `web-admin/src/components/Sidebar.svelte`, find the existing route list (the array of `{ href, label, icon }` entries) and add a "Users" entry under the Admin group. The icon comes from `@lucide/svelte` — `Users` or `UserCog` work. Match the existing import/usage pattern.

- [ ] **Step 5: Register the route**

In `web-admin/src/app.svelte` (or wherever the router lives), add `/users` → `users.svelte`. Match the surrounding routes' registration pattern.

- [ ] **Step 6: Build the SPA**

Run: `cd web-admin && bun run build`
Expected: build succeeds. Confirm `pkg/admin/static/dist/index.html` references the new bundle.

- [ ] **Step 7: Manual smoke (skip in autonomous execution; user should run)**

Run: `just dev` from `examples/4node-basic/`. Open `http://localhost:9101/admin`, log in, click "Users" in the sidebar. The table should show `admin` (the seeded default). Try the "Add operator" button.

- [ ] **Step 8: Commit**

```bash
git add web-admin/src/routes/users.svelte web-admin/src/components/UserDrawer.svelte web-admin/src/components/Sidebar.svelte web-admin/src/app.svelte pkg/admin/static/dist/
git commit -m "web-admin: Users page for admin operator management"
```

---

## Phase D — Documentation

### Task 10: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

Three updates, all in or near the Admin dashboard paragraph (currently around line 203):

1. Update the Command palette description (currently line 211) to clarify it's search-only.
2. Add a project convention: "admin features must be cmdsys verbs first, UI is a second surface."
3. Document the new `admin.operator.*` verbs and the Users page.

- [ ] **Step 1: Fix the palette description**

In `CLAUDE.md`, find the existing line that begins "The Command palette (`⌘K`, `CommandPalette.svelte`)..." and update it to explicitly call out that the palette is for finding/navigating to entities (cells / nodes / players), not for running commands. Suggested replacement:

```
The Command palette (`⌘K`, `CommandPalette.svelte`) is a VS-Code-style entity *finder* over cells / nodes / players — search and navigation only, no command invocation. It reads `cellsStore` / `hostsStore` / `gatewaysStore` / `playersStore` directly (no API call), fuzzy-matches the typed query, and on Enter writes a `pendingNav` signal + navigates to the appropriate route — `/cluster` for cells, `/nodes` for hosts/gateways, `/players` for players. Routes consume `pendingNav` in a `$effect` to open the right detail surface. Commands run from the per-page UI surfaces (e.g. PlayerDrawer buttons, PanelHost toolbars, the Users page).
```

- [ ] **Step 2: Add the convention paragraph**

Immediately after the existing Admin dashboard paragraph (around line 203), insert a new paragraph:

```
**Admin feature convention:** Every operator-facing admin action MUST be implemented as a cmdsys verb first. The console gets the verb for free (registered against `Process.CmdRegistry()`), the admin UI calls it via `POST /admin/api/commands/<verb>`, and an audit-log entry is recorded automatically. Don't reach for ad-hoc HTTP routes or per-page handlers — they bypass RBAC, the audit log, and the dual-surface (console + UI) requirement. The four `admin.operator.*` verbs (`create`, `delete`, `password`, `list`) are the worked example: backed by `persist.AdminOperatorRepository`, registered in `pkg/admin/operator_commands.go`, and surfaced as the `/users` route in the admin UI.
```

- [ ] **Step 3: Update the seed banner / operator-management mention**

In the same Admin dashboard paragraph, find the sentence that talks about the seeded `admin`/`admin` operator and "Rotation/management is a future admin-UI feature — there is no CLI for it." Update to:

```
Operators live in the `admin_operators` Postgres table managed via `persist.AdminOperatorRepository`; `admin.NewServer` seeds a default `admin`/`admin` operator with `*.*` grants on first run when the table is empty (the credentials are logged with a "CHANGE IN PRODUCTION" banner). Operator management is exposed via the `admin.operator.*` cmdsys verbs (`create`, `delete`, `password`, `list`) — runnable from the server console (passwords prompted via TTY, no echo) and from the admin UI's `/users` page.
```

- [ ] **Step 4: Document the new Secret schema flag**

CLAUDE.md mentions `pkg/cmdsys/schema.go::FieldSchema` in the context of `ArgsModal.svelte` (around line 209). Append a sentence to that paragraph noting the new flag:

```
FieldSchema now also carries a `Secret bool` field (set by `cmd:"secret"`); the console TTY-prompts for these fields (no echo) and ArgsModal renders them as `<input type="password">`.
```

- [ ] **Step 5: Verify the edits read cleanly**

Read the affected section (roughly CLAUDE.md lines 195–215) end-to-end. Make sure the narrative flows: dashboard wiring → operators/users → conventions → SPA structure → palette.

- [ ] **Step 6: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: cmdsys-first admin convention + Users page + ⌘K palette is search-only"
```

---

## Phase E — End-to-end verification

### Task 11: Full test sweep

- [ ] **Step 1: Go suite**

Run: `go test ./... -count=1`
Expected: all PASS.

- [ ] **Step 2: Pgtest suite**

Run: `just test-pg`
Expected: all PASS. (The `admin_operators` repo coverage from the previous plan still applies; this plan touches only handler code, not the schema.)

- [ ] **Step 3: SPA tests**

Run: `cd web-admin && bun test`
Expected: all PASS. (If only a handful of `*.test.ts` files exist, they should still be unaffected.)

- [ ] **Step 4: Vet**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 5: Manual smoke (user-run, not autonomous)**

Console:

```
> admin operator create alice
password:         (typed, no echo)
{ "Username": "alice", "Grants": ["*.*"] }
> admin operator list
{ "operators": [ {"username":"admin",...}, {"username":"alice",...} ] }
> admin operator password alice
password:
{ "Username": "alice" }
> admin operator delete admin    (should fail — "cannot delete yourself" only applies to HTTP; console can do this)
{ "Username": "admin" }
> admin operator delete alice    (should fail — "cannot delete the last admin operator")
  error: cannot delete the last admin operator
```

Admin UI:

1. Log in as `admin`/`admin` at `/admin`.
2. Click Users in the sidebar; confirm a single-row table.
3. Add operator → username + password fields render correctly (password is masked).
4. Click a row → drawer; rotate password / delete buttons work.
5. Try to delete your own row from the UI → expect a clear error (self-delete guard).

(The plan does not run the dev server itself; the user smoke-tests after the implementer reports all tasks done.)

---

## Self-Review

**1. Spec coverage:**
- ✅ Cmdsys verbs for admin operator mgmt → Tasks 4-6 (create/delete/password/list) and Task 7 (auto-registration on NewServer)
- ✅ TTY prompt for passwords in the console → Tasks 1 (schema flag), 2 (help text), 3 (console adapter)
- ✅ Admin UI Users page → Task 9; password fields in modals → Task 8
- ✅ Self-delete guardrail → Task 6 (handler) + Task 6 test
- ✅ Last-operator guardrail → Task 6 (handler) + Task 6 test
- ✅ Doc note: "admin features must be cmdsys verbs first" → Task 10 Step 2
- ✅ Doc fix: ⌘K palette is search-only → Task 10 Step 1
- ✅ Project memory respected: `cmd:"secret"` follows the existing tag convention; secret args remain positional in arg-order (just prompted), matching the [Command arg style](memory/feedback_command_arg_style.md) preference

**2. Placeholder scan:**
- One soft spot: Task 9 Step 2 says "If `fetchCommandSchema` / `invokeCommand` don't yet exist… check what helpers ARE there." This is a real fallback, not a placeholder — the plan expects the implementer to run a grep and adapt. Same for ConfirmDialog / ArgsModal `prefilled` prop. These are honest "match the existing conventions" branches; not "TODO: fill in later."
- No "TBD", "implement later", "similar to Task N" found.

**3. Type consistency:**
- `adminOperatorCreateArgs` fields (Username, Password, Grants) match between Task 4 (definition), Task 5 (handler), Task 9 (UI uses schema introspection — no hardcoded names).
- `adminOperatorListRow` fields (Username, Grants, CreatedAt, UpdatedAt) match Task 4 ← Task 5 ← Task 9 (UI's `columns` array uses the same camelCase keys via the JSON tags).
- `Secret` flag name is consistent across Task 1 (schema), Task 2 (help), Task 3 (console), Task 8 (UI).
- `RegisterOperatorCommands(reg, repo)` signature matches Task 4 (definition) ↔ Task 7 (call site).

---

**Plan complete.** 11 tasks across 5 phases delivering: schema-level `Secret` field, TTY-prompt support in the console, four `admin.operator.*` cmdsys verbs (with last-operator + self-delete guards), an admin UI Users page, and three CLAUDE.md updates (palette fix + cmdsys-first convention + new feature docs).
