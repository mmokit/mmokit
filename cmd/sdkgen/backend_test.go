package main

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestBackendFor(t *testing.T) {
	// ts → a working tsBackend, no error.
	b, err := backendFor("ts", backendOpts{CoreTS: "core.ts", InterpTS: "interp.ts"})
	if err != nil {
		t.Fatalf("backendFor(ts) error = %v, want nil", err)
	}
	if b.Lang() != "ts" {
		t.Fatalf("Lang() = %q, want ts", b.Lang())
	}

	// csharp → a working csharpBackend (the Plan 2 not-implemented error is gone).
	cs, err := backendFor("csharp", backendOpts{})
	if err != nil {
		t.Fatalf("backendFor(csharp) error = %v, want nil", err)
	}
	if cs.Lang() != "csharp" {
		t.Fatalf("Lang() = %q, want csharp", cs.Lang())
	}

	// unknown → generic unknown-lang error.
	if _, err := backendFor("rust", backendOpts{}); err == nil ||
		!strings.Contains(err.Error(), "unknown --lang") {
		t.Fatalf("backendFor(rust) error = %v, want 'unknown --lang'", err)
	}
}

func TestTSBackendCoreFiles(t *testing.T) {
	b := tsBackend{
		coreTS:         "a/delta-decoder-core.ts",
		interpTS:       "b/interpolation-core.ts",
		clockSyncTS:    "c/clock-sync.ts",
		interpBufferTS: "d/interpolation-buffer.ts",
		playbackTS:     "e/playback-controller.ts",
		predictionTS:   "f/prediction-buffer.ts",
	}
	core := b.CoreFiles()
	if len(core) != 6 {
		t.Fatalf("CoreFiles len = %d, want 6", len(core))
	}
	// Dst names are the _core/ basenames the SDK imports.
	wantDst := []string{"delta-decoder-core.ts", "interpolation-core.ts", "clock-sync.ts", "interpolation-buffer.ts", "playback-controller.ts", "prediction-buffer.ts"}
	wantSrc := []string{"a/delta-decoder-core.ts", "b/interpolation-core.ts", "c/clock-sync.ts", "d/interpolation-buffer.ts", "e/playback-controller.ts", "f/prediction-buffer.ts"}
	for i := range core {
		if core[i].Dst != wantDst[i] || core[i].Src != wantSrc[i] {
			t.Fatalf("CoreFiles[%d] = %+v, want Src=%q Dst=%q", i, core[i], wantSrc[i], wantDst[i])
		}
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
		if !slices.Contains(gotFull, name) {
			t.Fatalf("full schema missing %q; got %v", name, gotFull)
		}
	}

	// broadcasts.ts emits when ONLY ServerEventTypes is set (no BroadcastTypes).
	onlyEvents := ProtocolSchema{ServerEventTypes: []ServerEventTypeSchema{{}}}
	if !slices.Contains(keys(b.OutputFiles(onlyEvents)), "broadcasts.ts") {
		t.Fatalf("broadcasts.ts must emit when only ServerEventTypes is set")
	}
}
