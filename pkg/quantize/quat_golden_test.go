package quantize

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// The committed cross-language manifest. Reading it from here — rather than
// only from the C# and TypeScript suites — is what makes a Go-side change red
// under plain `go test`, BEFORE anyone runs `just csharp-golden`. Without it,
// a change to the encoder would silently rewrite its own expectations on the
// next regeneration.
const quatGoldenPath = "../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json"

type quatGoldenCase struct {
	Name   string    `json:"name"`
	Hex    string    `json:"hex"`
	Packed uint64    `json:"packed"`
	X      float64   `json:"x"`
	Y      float64   `json:"y"`
	Z      float64   `json:"z"`
	W      float64   `json:"w"`
	Bits   [4]uint32 `json:"bits"`
}

type quatGoldenManifest struct {
	Quat []quatGoldenCase `json:"quat"`
}

func loadQuatGolden(t *testing.T) quatGoldenManifest {
	t.Helper()
	raw, err := os.ReadFile(quatGoldenPath)
	if err != nil {
		t.Fatalf("read golden manifest: %v", err)
	}
	var m quatGoldenManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse golden manifest: %v", err)
	}
	if len(m.Quat) < 100 {
		t.Fatalf("manifest has %d quat cases, want the full corpus", len(m.Quat))
	}
	return m
}

// TestUnQuat_MatchesCommittedGolden compares on EXACT float32 identity, not a
// tolerance. A tolerance would hide precisely the rounding disagreement this
// corpus exists to catch between three independent implementations.
func TestUnQuat_MatchesCommittedGolden(t *testing.T) {
	for _, c := range loadQuatGolden(t).Quat {
		x, y, z, w := UnQuat(c.Packed)
		got := [4]uint32{
			math.Float32bits(x), math.Float32bits(y),
			math.Float32bits(z), math.Float32bits(w),
		}
		if got != c.Bits {
			t.Errorf("%s: UnQuat bits = %v, want %v", c.Name, got, c.Bits)
		}
	}
}

// TestQuatGoldenHexMatchesTheWireWriter pins the manifest's hex against the
// REAL SnapshotWriter, so the generator's packing cannot drift from what the
// server actually emits. This is the relationship a client port depends on:
// it decodes hex, not the packed integer the manifest also carries.
func TestQuatGoldenHexMatchesTheWireWriter(t *testing.T) {
	for _, c := range loadQuatGolden(t).Quat {
		want, err := hex.DecodeString(c.Hex)
		if err != nil {
			t.Fatalf("%s: bad hex %q: %v", c.Name, c.Hex, err)
		}
		if len(want) != QuatWireSize {
			t.Fatalf("%s: hex is %d bytes, want %d", c.Name, len(want), QuatWireSize)
		}

		// Round-trip the packed value through the real reader and writer.
		buf := make([]byte, QuatWireSize)
		r := NewSnapshotReader(want)
		x, y, z, w := r.UnQQuat()
		wtr := NewSnapshotWriter(buf)
		wtr.QQuat(x, y, z, w)

		got := [4]uint32{
			math.Float32bits(x), math.Float32bits(y),
			math.Float32bits(z), math.Float32bits(w),
		}
		if got != c.Bits {
			t.Errorf("%s: reading the golden hex gave bits %v, want %v", c.Name, got, c.Bits)
		}
	}
}

type slerpGoldenCase struct {
	Name string     `json:"name"`
	A    [4]float64 `json:"a"`
	B    [4]float64 `json:"b"`
	T    float64    `json:"t"`
	Out  [4]float64 `json:"out"`
}

// TestSlerpGoldenIsWellFormed keeps the slerp corpus honest from the quantize
// side; pkg/component owns the maths and asserts the values themselves.
func TestSlerpGoldenIsWellFormed(t *testing.T) {
	raw, err := os.ReadFile(quatGoldenPath)
	if err != nil {
		t.Fatalf("read golden manifest: %v", err)
	}
	var m struct {
		Slerp []slerpGoldenCase `json:"slerp"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.Slerp) < 50 {
		t.Fatalf("manifest has %d slerp cases, want the full corpus", len(m.Slerp))
	}
	sawBelowThreshold := false
	for _, c := range m.Slerp {
		n := math.Sqrt(c.Out[0]*c.Out[0] + c.Out[1]*c.Out[1] + c.Out[2]*c.Out[2] + c.Out[3]*c.Out[3])
		if math.Abs(n-1) > 1e-6 {
			t.Errorf("%s: output norm %v, want 1", c.Name, n)
		}
		dot := c.A[0]*c.B[0] + c.A[1]*c.B[1] + c.A[2]*c.B[2] + c.A[3]*c.B[3]
		if dot < 0 {
			sawBelowThreshold = true
		}
	}
	// Non-vacuity: a corpus with no opposite-hemisphere pair cannot detect a
	// port that omits the shortest-arc negation.
	if !sawBelowThreshold {
		t.Fatal("no case has dot < 0; the corpus cannot catch a missing shortest-arc negation")
	}
}
