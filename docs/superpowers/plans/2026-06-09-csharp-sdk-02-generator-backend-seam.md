# C# SDK — Plan 2: Generator Backend Seam (`--lang` flag) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `cmd/sdkgen` behind a language-`Backend` seam and add a `--lang` flag, with the TypeScript output kept byte-identical, so a C# backend can be slotted in later (Plan 4).

**Architecture:** Introduce a `Backend` interface (`Lang`, `CoreFiles`, `OutputFiles`). `tsBackend` implements it by delegating content generation to the existing `*Generator` (whose methods read only `schema`, never `outDir`). `main.go` becomes language-agnostic: it decodes the schema, selects a backend via `backendFor(lang)`, copies the backend's core files, and writes the backend's output files. `--lang` defaults to `ts`; `--lang=csharp` returns a clear "not yet implemented" error (the real C# backend lands in Plan 4); any other value errors as "unknown". Byte-identical TS output is proven by regenerating the committed SDKs and asserting `git diff --exit-code` is clean.

**Tech Stack:** Go, `cmd/sdkgen`, `go test`, `just`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §A.

**Prerequisite:** Plan 1 (UDP op-channel) is merged. This plan is independent of Plan 1 but is sequenced after it.

---

## Background facts (verified in the current code)

- `cmd/sdkgen/main.go` currently: decodes `ProtocolSchema`, constructs `&Generator{schema, outDir}`, `MkdirAll` out + `_core`, copies two TS core files (`delta-decoder-core.ts`, `interpolation-core.ts`) via `copyFile`, builds a `map[string]func() string` of output files (unconditional: `transport.ts`, `entities.ts`, `delta-decoder.ts`, `client.ts`, `index.ts`; conditional: `broadcasts.ts` when `BroadcastTypes` OR `ServerEventTypes` non-empty, `inputs.ts` when `ClientInputTypes` non-empty, `operations.ts` when `Operations` non-empty, `entityType.ts` when `Entities` non-empty), then writes each.
- `Generator` (`cmd/sdkgen/generate.go:10-13`) has fields `schema ProtocolSchema` and `outDir string`. **`outDir` is set in `main.go` but never read by any `Generator` method** (verified: no `g.outDir` reference outside the struct decl). It is dead and will be dropped.
- All content-generating methods are `func (g *Generator) genXxx() string` reading only `g.schema`: `genTransport`, `genEntities`, `genDeltaDecoder`, `genClient`, `genIndex` (generate.go), `genBroadcasts` (broadcasts.go), `genInputs` (inputs.go), `genOperations` (operations.go), `genEntityType` (entitytype.go). Helpers `copyFile` and `titleCase` live in `main.go`.

---

## File Structure

- **Create:** `cmd/sdkgen/backend.go` — the `Backend` interface, `CoreFile` struct, and `backendFor(lang, coreTS, interpTS)` selector.
- **Create:** `cmd/sdkgen/backend_ts.go` — `tsBackend`, delegating to `*Generator`; owns the file-selection logic moved out of `main.go`.
- **Create:** `cmd/sdkgen/backend_test.go` — hermetic unit tests for `backendFor` routing, `tsBackend.OutputFiles` selection, and `CoreFiles`.
- **Modify:** `cmd/sdkgen/main.go` — add `--lang` flag, select backend, drive core-copy + output-write through the backend; update the package doc comment.
- **Modify:** `cmd/sdkgen/generate.go` — drop the dead `outDir` field from `Generator`; update its doc comment.

---

### Task 1: Introduce the `Backend` seam and `--lang` flag

**Files:**
- Create: `cmd/sdkgen/backend.go`
- Create: `cmd/sdkgen/backend_ts.go`
- Modify: `cmd/sdkgen/main.go`
- Modify: `cmd/sdkgen/generate.go`

- [ ] **Step 1: Create `cmd/sdkgen/backend.go`**

```go
package main

import "fmt"

// CoreFile is a runtime file copied verbatim into <out>/_core/.
type CoreFile struct {
	Src string // path on disk to copy from
	Dst string // filename under _core/
}

// Backend is a language-specific SDK emitter. main.go is language-agnostic:
// it decodes the schema, asks the backend for its core files and its
// schema-filtered output file set, and writes them.
type Backend interface {
	// Lang is the --lang token this backend handles ("ts", "csharp").
	Lang() string
	// CoreFiles lists runtime files to copy verbatim into <out>/_core/.
	CoreFiles() []CoreFile
	// OutputFiles returns filename -> content-generator for the given
	// schema, already filtered to what the schema needs (e.g. no
	// broadcasts file when the schema declares none).
	OutputFiles(schema ProtocolSchema) map[string]func() string
}

// backendFor selects a backend by --lang token. The C# backend is added in
// a later plan; until then it returns a clear not-implemented error so the
// flag is wired and the message is specific.
func backendFor(lang, coreTS, interpTS string) (Backend, error) {
	switch lang {
	case "ts":
		return tsBackend{coreTS: coreTS, interpTS: interpTS}, nil
	case "csharp":
		return nil, fmt.Errorf("--lang=csharp: C# backend not yet implemented (see C# SDK Plan 4)")
	default:
		return nil, fmt.Errorf("unknown --lang %q (want: ts, csharp)", lang)
	}
}
```

- [ ] **Step 2: Create `cmd/sdkgen/backend_ts.go`**

```go
package main

// tsBackend emits the TypeScript SDK. It delegates content generation to
// the existing *Generator; the file-selection logic here mirrors exactly
// what main.go did before the backend seam was introduced.
type tsBackend struct {
	coreTS   string // path to delta-decoder-core.ts
	interpTS string // path to interpolation-core.ts
}

func (b tsBackend) Lang() string { return "ts" }

func (b tsBackend) CoreFiles() []CoreFile {
	return []CoreFile{
		{Src: b.coreTS, Dst: "delta-decoder-core.ts"},
		{Src: b.interpTS, Dst: "interpolation-core.ts"},
	}
}

func (b tsBackend) OutputFiles(schema ProtocolSchema) map[string]func() string {
	g := &Generator{schema: schema}
	files := map[string]func() string{
		"transport.ts":     g.genTransport,
		"entities.ts":      g.genEntities,
		"delta-decoder.ts": g.genDeltaDecoder,
		"client.ts":        g.genClient,
		"index.ts":         g.genIndex,
	}
	// broadcasts.ts holds both HandleAll[T] broadcast classes AND
	// RegisterEvent[T] typed-server-event classes — same wire layout, same
	// dispatcher path. Emit when either registry is non-empty.
	if len(schema.BroadcastTypes) > 0 || len(schema.ServerEventTypes) > 0 {
		files["broadcasts.ts"] = g.genBroadcasts
	}
	// inputs.ts mirrors broadcasts.ts but for client-input messages.
	if len(schema.ClientInputTypes) > 0 {
		files["inputs.ts"] = g.genInputs
	}
	// operations.ts holds RegisterOp[Req, Res] Request + Response classes.
	if len(schema.Operations) > 0 {
		files["operations.ts"] = g.genOperations
	}
	// entityType.ts holds the EntityType const block (one entry per kind).
	if len(schema.Entities) > 0 {
		files["entityType.ts"] = g.genEntityType
	}
	return files
}
```

- [ ] **Step 3: Drop the dead `outDir` field from `Generator`**

In `cmd/sdkgen/generate.go`, replace:

```go
// Generator produces TypeScript SDK files from a protocol schema.
type Generator struct {
	schema ProtocolSchema
	outDir string
}
```

with:

```go
// Generator produces TypeScript SDK file contents from a protocol schema.
// It is the content engine behind tsBackend; all genXxx methods read only
// schema.
type Generator struct {
	schema ProtocolSchema
}
```

- [ ] **Step 4: Refactor `main.go` to drive everything through the backend**

In `cmd/sdkgen/main.go`, update the package doc comment's usage block (top of file) to mention `--lang`:

Replace the comment lines:
```go
//	go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen --out examples/4node-basic/web/sdk
//	# or:
//	go run ./cmd/sdkgen --schema protocol-schema.json --out examples/4node-basic/web/sdk
```
with:
```go
//	go run ./examples/4node-basic --dump-schema | go run ./cmd/sdkgen --out examples/4node-basic/web/sdk
//	# or:
//	go run ./cmd/sdkgen --schema protocol-schema.json --out examples/4node-basic/web/sdk
//	# select the target language (default ts):
//	go run ./cmd/sdkgen --lang=ts --schema protocol-schema.json --out sdk
```

Replace the entire `main()` body (from `func main() {` through its closing brace) with:

```go
func main() {
	schemaFile := flag.String("schema", "", "Path to protocol-schema.json (reads stdin if empty)")
	outDir := flag.String("out", "sdk", "Output directory for generated SDK")
	lang := flag.String("lang", "ts", "Target language for the generated SDK (ts, csharp)")
	coreTS := flag.String("core", "pkg/quantize/ts/delta-decoder-core.ts", "Path to delta-decoder-core.ts to copy")
	interpTS := flag.String("interp", "pkg/quantize/ts/interpolation-core.ts", "Path to interpolation-core.ts to copy")
	flag.Parse()

	// Read schema.
	var r io.Reader = os.Stdin
	if *schemaFile != "" {
		f, err := os.Open(*schemaFile)
		if err != nil {
			log.Fatalf("open schema: %v", err)
		}
		defer f.Close()
		r = f
	}

	var schema ProtocolSchema
	if err := json.NewDecoder(r).Decode(&schema); err != nil {
		log.Fatalf("decode schema: %v", err)
	}

	// Select the language backend.
	backend, err := backendFor(*lang, *coreTS, *interpTS)
	if err != nil {
		log.Fatalf("sdkgen: %v", err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(*outDir, "_core"), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	// Copy core runtime files.
	for _, cf := range backend.CoreFiles() {
		if err := copyFile(cf.Src, filepath.Join(*outDir, "_core", cf.Dst)); err != nil {
			log.Fatalf("copy core %s: %v", cf.Dst, err)
		}
	}

	// Generate each output file.
	for name, fn := range backend.OutputFiles(schema) {
		path := filepath.Join(*outDir, name)
		if err := os.WriteFile(path, []byte(fn()), 0o644); err != nil {
			log.Fatalf("write %s: %v", name, err)
		}
		fmt.Printf("  %s\n", path)
	}
}
```

Leave the `copyFile` and `titleCase` helpers in `main.go` unchanged. (The `import` block in `main.go` is unchanged — it still uses `encoding/json`, `flag`, `fmt`, `io`, `log`, `os`, `path/filepath`, `strings`.)

- [ ] **Step 5: Verify it compiles and the TS recipe still runs**

Run: `go vet ./cmd/sdkgen/...`
Expected: no output (clean).

Run: `go run ./cmd/sdkgen --lang=csharp --schema /dev/null 2>&1 || true`
Expected: prints `sdkgen: --lang=csharp: C# backend not yet implemented (see C# SDK Plan 4)` and exits non-zero. (The `/dev/null` decode actually fails first with EOF — to isolate the lang check, instead run the unit tests in Task 2; this manual check is optional.)

- [ ] **Step 6: Commit**

```bash
git add cmd/sdkgen/backend.go cmd/sdkgen/backend_ts.go cmd/sdkgen/main.go cmd/sdkgen/generate.go
git commit -m "refactor(sdkgen): introduce language Backend seam + --lang flag

cmd/sdkgen is now backend-agnostic: a Backend interface owns core files +
schema-filtered output files. tsBackend delegates to the existing
Generator (TS output unchanged). --lang defaults to ts; --lang=csharp
returns a not-yet-implemented error (C# backend lands in a later plan).
Dropped the dead Generator.outDir field.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Hermetic unit tests for backend selection + file filtering

**Files:**
- Create: `cmd/sdkgen/backend_test.go`

- [ ] **Step 1: Write the tests**

Create `cmd/sdkgen/backend_test.go`:

```go
package main

import (
	"sort"
	"strings"
	"testing"
)

func TestBackendFor(t *testing.T) {
	// ts → a working tsBackend, no error.
	b, err := backendFor("ts", "core.ts", "interp.ts")
	if err != nil {
		t.Fatalf("backendFor(ts) error = %v, want nil", err)
	}
	if b.Lang() != "ts" {
		t.Fatalf("Lang() = %q, want ts", b.Lang())
	}

	// csharp → specific not-implemented error (flag wired, message clear).
	if _, err := backendFor("csharp", "", ""); err == nil ||
		!strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("backendFor(csharp) error = %v, want 'not yet implemented'", err)
	}

	// unknown → generic unknown-lang error.
	if _, err := backendFor("rust", "", ""); err == nil ||
		!strings.Contains(err.Error(), "unknown --lang") {
		t.Fatalf("backendFor(rust) error = %v, want 'unknown --lang'", err)
	}
}

func TestTSBackendCoreFiles(t *testing.T) {
	b := tsBackend{coreTS: "a/delta-decoder-core.ts", interpTS: "b/interpolation-core.ts"}
	core := b.CoreFiles()
	if len(core) != 2 {
		t.Fatalf("CoreFiles len = %d, want 2", len(core))
	}
	// Dst names are the _core/ basenames the SDK imports.
	if core[0].Dst != "delta-decoder-core.ts" || core[1].Dst != "interpolation-core.ts" {
		t.Fatalf("CoreFiles Dst = %q,%q", core[0].Dst, core[1].Dst)
	}
	if core[0].Src != "a/delta-decoder-core.ts" || core[1].Src != "b/interpolation-core.ts" {
		t.Fatalf("CoreFiles Src not threaded from struct: %q,%q", core[0].Src, core[1].Src)
	}
}

func keys(m map[string]func() string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTSBackendOutputFilesSelection(t *testing.T) {
	b := tsBackend{}

	// Empty schema → only the five unconditional files.
	got := keys(b.OutputFiles(ProtocolSchema{}))
	want := []string{"client.ts", "delta-decoder.ts", "entities.ts", "index.ts", "transport.ts"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("empty schema files = %v, want %v", got, want)
	}

	// Each registry toggles its file on.
	full := ProtocolSchema{
		Entities:         []EntitySchema{{}},
		BroadcastTypes:   []BroadcastTypeSchema{{}},
		ClientInputTypes: []ClientInputTypeSchema{{}},
		ServerEventTypes: []ServerEventTypeSchema{{}},
		Operations:       []OperationSchema{{}},
	}
	gotFull := keys(b.OutputFiles(full))
	for _, name := range []string{"broadcasts.ts", "inputs.ts", "operations.ts", "entityType.ts"} {
		if !contains(gotFull, name) {
			t.Fatalf("full schema missing %q; got %v", name, gotFull)
		}
	}

	// broadcasts.ts emits when ONLY ServerEventTypes is set (no BroadcastTypes).
	onlyEvents := ProtocolSchema{ServerEventTypes: []ServerEventTypeSchema{{}}}
	if !contains(keys(b.OutputFiles(onlyEvents)), "broadcasts.ts") {
		t.Fatalf("broadcasts.ts must emit when only ServerEventTypes is set")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./cmd/sdkgen/ -run 'TestBackend|TestTSBackend' -v`
Expected: PASS — all four tests green. (These pin both the selection logic and the not-implemented/unknown error messages.)

- [ ] **Step 3: Commit**

```bash
git add cmd/sdkgen/backend_test.go
git commit -m "test(sdkgen): cover backend selection + TS output-file filtering

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Prove TS output is byte-identical (regenerate committed SDKs)

**Files:** none modified — verification only. The committed SDK files (`web-pixi/sdk/`, `examples/4node-basic/web/sdk/`) ARE the golden output; regenerating them and diffing proves the refactor changed nothing.

- [ ] **Step 1: Ensure the dev database is up (schema dump needs Postgres)**

Run: `just db-up`
Expected: Postgres 17 container running. (Skip if already up.)

- [ ] **Step 2: Regenerate the space-game SDK and assert no diff**

Run:
```bash
just space-sdk
git diff --exit-code -- web-pixi/sdk
```
Expected: `just space-sdk` prints the written file paths; `git diff --exit-code` produces **no output and exits 0** (byte-identical — the refactor did not change any generated TS).

- [ ] **Step 3: Regenerate the 4node-basic SDK and assert no diff**

Run:
```bash
just client-sdk examples/4node-basic
git diff --exit-code -- examples/4node-basic/web/sdk
```
Expected: no output, exit 0.

- [ ] **Step 4: Vet the package**

Run: `go vet ./cmd/sdkgen/...`
Expected: no output (clean).

No commit — verification only. If `git diff --exit-code` shows ANY change, the refactor altered TS output: stop and reconcile `tsBackend.OutputFiles` / `main.go` against the original `main.go` logic before proceeding.

**If Postgres/Docker is unavailable in this environment:** `just db-up` and the regen steps cannot run. In that case, report `DONE_WITH_CONCERNS`, note the byte-identical regen check was deferred, and rely on the Task 2 unit tests plus a manual reading that `tsBackend.OutputFiles` reproduces the original `main.go` file map exactly. The user can run Task 3 with the DB up.

---

## Self-Review

- **Spec coverage (§A):** `Backend` interface with `Lang`/`CoreFiles`/`OutputFiles` (Task 1 backend.go); `tsBackend` wraps existing `Generator`, TS unchanged (Task 1 backend_ts.go + Task 3 byte-identical proof); `--lang` defaults to `ts`, `csharp` reserved with a clear error (Task 1 + Task 2); conditional file emission stays schema-driven and shared in `tsBackend.OutputFiles` (mirrors original `main.go`). ✅
- **Placeholder scan:** No TBD/TODO; every code step shows complete code. The only generated artifacts (the regenerated SDKs) are existing committed files re-emitted and diffed, not hand-written. ✅
- **Type consistency:** `Backend` methods (`Lang() string`, `CoreFiles() []CoreFile`, `OutputFiles(ProtocolSchema) map[string]func() string`) match `tsBackend`'s methods and `backendFor`'s return type exactly. `CoreFile{Src, Dst}` fields match their use in both `tsBackend.CoreFiles` and `main.go`'s copy loop. The dropped `Generator.outDir` is verified unused (background facts). Schema field names (`Entities`, `BroadcastTypes`, `ClientInputTypes`, `ServerEventTypes`, `Operations`) match `cmd/sdkgen/schema.go`. ✅
