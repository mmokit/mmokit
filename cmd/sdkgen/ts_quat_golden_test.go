package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The 3D entity snapshot, decoded by the REAL generated TypeScript SDK.
//
// docs/roadmap.md §7.5.6 names this as the gap phase 2 left: "cube3d.json is
// the only pin on the 3D layout until a generated SDK cross-checks it". The
// unit tests in pkg/quantize/ts prove unQuat decodes the golden vectors, and
// cmd/sdkgen's unit tests prove the generator emits a qquat line — but nothing
// proved the emitted line consumes the right number of bytes at the right
// offset inside a real entity layout. An off-by-one there compiles, decodes,
// and silently shifts every field after the quaternion.
//
// Same shape as TestTypeScriptReflectDecoder_MatchesGoldenBytes next door:
// generate into a temp dir, run the output under bun, assert the VALUES.
func TestTypeScriptQuatDecoder_MatchesServerBytes(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed; skipping the TypeScript 3D decode gate")
	}

	// A 3D engine layout with a field AFTER the quaternion, deliberately: a
	// quaternion that consumed the wrong width would still decode plausibly
	// on its own, and only a trailing field exposes the misaligned cursor.
	schema := ProtocolSchema{
		Game:      "cube3d",
		Dimension: "3d",
		Entities: []EntitySchema{{
			Kind:   1,
			Name:   "Cube",
			Layout: []int{4, 4, 4, 7, 2},
			Bindings: []BindingSchema{
				{Type: "viewer_relative_pos3", Fields: []BindingSchemaField{
					{Name: "worldX", Encoding: "f32", Size: 4},
					{Name: "worldY", Encoding: "f32", Size: 4},
					{Name: "worldZ", Encoding: "f32", Size: 4},
				}},
				{Type: "q_quat", Fields: []BindingSchemaField{
					{Name: "rot", Encoding: "qquat", Size: 7},
				}},
				{Type: "component", Fields: []BindingSchemaField{
					{Name: "trailer", Encoding: "qvel", Size: 2, Scale: 100},
				}},
			},
		}},
	}

	out := t.TempDir()
	const tsCore = "../../pkg/quantize/ts/"
	b := tsBackend{
		coreTS:         tsCore + "delta-decoder-core.ts",
		interpTS:       tsCore + "interpolation-core.ts",
		clockSyncTS:    tsCore + "clock-sync.ts",
		interpBufferTS: tsCore + "interpolation-buffer.ts",
		playbackTS:     tsCore + "playback-controller.ts",
		predictionTS:   tsCore + "prediction-buffer.ts",
		reconGateTS:    tsCore + "reconciliation-gate.ts",
	}
	for name, gen := range b.OutputFiles(schema) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(out, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, name), []byte(gen()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, cf := range b.CoreFiles() {
		src, err := os.ReadFile(cf.Src)
		if err != nil {
			t.Fatalf("read core %s: %v", cf.Src, err)
		}
		dst := filepath.Join(out, "_core", cf.Dst)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, src, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A complete replication frame carrying one full entity, assembled the
	// way the server does — header, then a full entry whose snapshot is the
	// 3D layout above. Going through the real exported decoder rather than an
	// internal helper is the point: it is the path a browser actually takes.
	q := firstQuatGolden(t)
	snapHex := "3fc00000" + "c0200000" + "40600000" + q.Hex + "1f40"
	frameHex := buildFullFrameHex(snapHex)

	spec := fmt.Sprintf(`
import { test, expect } from "bun:test";
import { Cube3dDeltaDecoder } from "./delta-decoder.js";

const hex = %q;
const bytes = new Uint8Array(hex.match(/../g)!.map((h) => parseInt(h, 16)));

test("generated 3D decoder reproduces the server's bytes", () => {
  const update = new Cube3dDeltaDecoder().decode(bytes);
  expect(update).not.toBeNull();
  // A FRESH_SNAPSHOT frame reports its full entries as "entered".
  expect(update!.entered.length).toBe(1);
  const e: any = update!.entered[0];

  expect(e.worldX).toBeCloseTo(1.5, 5);
  expect(e.worldY).toBeCloseTo(-2.5, 5);
  expect(e.worldZ).toBeCloseTo(3.5, 5);

  expect(e.rot.x).toBeCloseTo(%v, 5);
  expect(e.rot.y).toBeCloseTo(%v, 5);
  expect(e.rot.z).toBeCloseTo(%v, 5);
  expect(e.rot.w).toBeCloseTo(%v, 5);

  // The field AFTER the quaternion. This is the assertion that catches a
  // quaternion consuming the wrong number of bytes: the cursor would be
  // misaligned and this value would be garbage. 0x1f40 = 7999 as int16,
  // dequantized at scale 100: 7999/32767*100.
  expect(e.trailer).toBeCloseTo(24.414808, 4);
});
`, frameHex, q.X, q.Y, q.Z, q.W)

	if err := os.WriteFile(filepath.Join(out, "quat.test.ts"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bun, "test", "quat.test.ts")
	cmd.Dir = out
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bun test failed — the generated 3D decoder disagrees with the server:\n%s", combined)
	}
	if !strings.Contains(string(combined), "1 pass") {
		t.Fatalf("expected the assertion to run:\n%s", combined)
	}
}

type quatGoldenEntry struct {
	Name string  `json:"name"`
	Hex  string  `json:"hex"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
	W    float64 `json:"w"`
}

// firstQuatGolden returns a non-identity vector from the shared manifest, so
// this gate and the three unit suites are pinned to the same bytes.
func firstQuatGolden(t *testing.T) quatGoldenEntry {
	t.Helper()
	raw, err := os.ReadFile("../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json")
	if err != nil {
		t.Fatalf("read the cross-language manifest: %v", err)
	}
	var doc struct {
		Quat []quatGoldenEntry `json:"quat"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	for _, c := range doc.Quat {
		if c.Name == "120-about-1,1,1" {
			return c
		}
	}
	t.Fatal("manifest is missing the 120-about-(1,1,1) vector")
	return quatGoldenEntry{}
}

// buildFullFrameHex wraps one entity snapshot in a replication frame, matching
// the layout cmd/csharp-golden assembles and pkg/quantize/wireformat.go emits:
// big-endian throughout.
func buildFullFrameHex(snapHex string) string {
	be32 := func(v uint32) string { return fmt.Sprintf("%08x", v) }
	be16 := func(v uint16) string { return fmt.Sprintf("%04x", v) }
	be64 := func(v uint64) string { return fmt.Sprintf("%016x", v) }

	snapLen := uint16(len(snapHex) / 2)
	var b strings.Builder
	b.WriteString(be32(42)) // tick
	b.WriteString(be32(7))  // seq
	b.WriteString(be32(1))  // flags: FRESH_SNAPSHOT
	b.WriteString(be16(1))  // fullCount
	b.WriteString(be16(0))  // deltaCount
	b.WriteString(be16(0))  // removedCount
	b.WriteString(be16(0))  // exitedCount
	b.WriteString(be32(1001))
	b.WriteString(be32(1))
	b.WriteString("01") // entityType = 1 (Cube)
	b.WriteString(be64(1717000000123))
	b.WriteString(be16(snapLen))
	b.WriteString(snapHex)
	b.WriteString(be16(0)) // no initial payload
	return b.String()
}
