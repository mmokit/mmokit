package component

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestSlerp_MatchesCommittedGolden reads the same cross-language manifest the
// C# and TypeScript suites consume, so a change to Slerp is red under plain
// `go test` before anyone regenerates it. Without this, editing the reference
// would silently rewrite its own expectations on the next `just csharp-golden`.
func TestSlerp_MatchesCommittedGolden(t *testing.T) {
	raw, err := os.ReadFile("../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json")
	if err != nil {
		t.Fatalf("read golden manifest: %v", err)
	}
	var m struct {
		Slerp []struct {
			Name string     `json:"name"`
			A    [4]float64 `json:"a"`
			B    [4]float64 `json:"b"`
			T    float64    `json:"t"`
			Out  [4]float64 `json:"out"`
		} `json:"slerp"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse golden manifest: %v", err)
	}
	if len(m.Slerp) < 50 {
		t.Fatalf("manifest has %d slerp cases, want the full corpus", len(m.Slerp))
	}

	for _, c := range m.Slerp {
		a := Rotation{X: float32(c.A[0]), Y: float32(c.A[1]), Z: float32(c.A[2]), W: float32(c.A[3])}
		b := Rotation{X: float32(c.B[0]), Y: float32(c.B[1]), Z: float32(c.B[2]), W: float32(c.B[3])}
		got := a.Slerp(b, float32(c.T))
		for i, pair := range [][2]float64{
			{float64(got.X), c.Out[0]},
			{float64(got.Y), c.Out[1]},
			{float64(got.Z), c.Out[2]},
			{float64(got.W), c.Out[3]},
		} {
			if math.Abs(pair[0]-pair[1]) > 1e-6 {
				t.Errorf("%s: component %d = %v, want %v", c.Name, i, pair[0], pair[1])
			}
		}
	}
}
