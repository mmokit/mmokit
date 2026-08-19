package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The browser's reflect decoding, pinned to Go's bytes.
//
// docs/roadmap.md §7.5 records the asymmetry this closes: the Unity client's
// delta decoder is byte-pinned to Go and the browser's is not — the reverse of
// where the risk sits, since the browser is the reference game's only client.
// The delta half was closed in 357bd61c. This is the REFLECT half, which
// matters more: it is the client-ingress boundary, carrying every typed event,
// broadcast, client input and typed-op body.
//
// It has to be a generate-and-decode harness rather than a unit test, because
// there is no shared TypeScript reflect codec to unit-test. cmd/sdkgen inlines
// a per-type decode into each generated class, so pkg/quantize/ts has no
// equivalent of Go's ReflectMarshal or C#'s ReflectCodec. Same shape as
// TestCsharpSdk_Compiles next door, one step further: that one compiles the
// output, this one runs it against known bytes.
//
// The fixture mirrors the `reflect` section of
// csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json, which C# already
// asserts — so a divergence between the two clients shows up here.

// reflectGoldenHex and its decoded values, from the manifest's `reflect`
// section. Little-endian throughout (the snapshot codec is big-endian —
// different layers).
//
//	0000c03f          f32     1.5
//	efbeadde          u32     3735928559
//	0600 68c3a96c6c6f string  "héllo"      u16 length prefix, then UTF-8
//	01                bool    true
//	d6ffffffffffffff  i64     -42
//	0300 070000000800000009000000  slice<u32> [7,8,9]   u16 count, then items
const reflectGoldenHex = "0000c03fefbeadde060068c3a96c6c6f01d6ffffffffffffff0300070000000800000009000000"

// reflectGoldenSchema describes a type with the fixture's shape, as a server
// event so sdkgen emits a class with a static decode().
func reflectGoldenSchema() ProtocolSchema {
	return ProtocolSchema{
		Game: "reflectgolden",
		ServerEventTypes: []ServerEventTypeSchema{{
			Name:   "golden.ReflectSample",
			TypeID: 0x52454653,
			Fields: []BroadcastFieldSchema{
				{Name: "a", Encoding: "f32", Size: 4},
				{Name: "b", Encoding: "u32", Size: 4},
				{Name: "c", Encoding: "string"},
				{Name: "d", Encoding: "bool", Size: 1},
				{Name: "e", Encoding: "i64", Size: 8},
				{Name: "f", Encoding: "slice", Item: &BroadcastFieldSchema{Encoding: "u32", Size: 4}},
			},
		}},
	}
}

func TestTypeScriptReflectDecoder_MatchesGoldenBytes(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed; skipping the TypeScript reflect gate")
	}

	out := t.TempDir()
	b := tsBackend{}
	for name, gen := range b.OutputFiles(reflectGoldenSchema()) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(out, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), []byte(gen()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Assert the decoded VALUES, not just that it compiles. A decoder that
	// reads the right byte count into the wrong fields still compiles.
	spec := `
import { test, expect } from "bun:test";
import { ReflectSample } from "./broadcasts.js";

const hex = "` + reflectGoldenHex + `";
const bytes = new Uint8Array(hex.match(/../g)!.map((h) => parseInt(h, 16)));

test("generated reflect decoder reproduces Go's bytes", () => {
  const m = ReflectSample.decode(bytes);
  expect(m.a).toBeCloseTo(1.5, 6);
  expect(m.b).toBe(3735928559);
  expect(m.c).toBe("héllo");
  expect(m.d).toBe(true);
  expect(Number(m.e)).toBe(-42);
  expect(Array.from(m.f)).toEqual([7, 8, 9]);
});
`
	if err := os.WriteFile(filepath.Join(out, "reflect.test.ts"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bun, "test", "reflect.test.ts")
	cmd.Dir = out
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bun test failed — the generated TypeScript decoder disagrees with Go's bytes:\n%s", combined)
	}
	if !strings.Contains(string(combined), "1 pass") {
		t.Fatalf("expected the assertion to run:\n%s", combined)
	}
}

// Guards the fixture itself: if the schema shape stops matching the manifest's
// `reflect` section, the two clients are being pinned to different things.
func TestTypeScriptReflectGoldenMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json")
	if err != nil {
		t.Fatalf("read the cross-language manifest: %v", err)
	}
	var doc struct {
		Reflect struct {
			HexBytes string `json:"hexBytes"`
		} `json:"reflect"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if doc.Reflect.HexBytes != reflectGoldenHex {
		t.Errorf("this test's fixture has drifted from the manifest C# asserts against:\n manifest %s\n here     %s",
			doc.Reflect.HexBytes, reflectGoldenHex)
	}
}
