package universe

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// fuzzSeed is one committed entry of a fuzz target's seed corpus: a file name
// under testdata/fuzz/<target>/ and the bytes it holds.
type fuzzSeed struct {
	name string
	data []byte
}

// updateFuzzCorpus rewrites the committed corpora instead of asserting against
// them. Driven by `just fuzz-corpus`, same shape as -update-shipdyn-golden.
var updateFuzzCorpus = flag.Bool("update-fuzz-corpus", false,
	"rewrite testdata/fuzz seed corpora from the builders in fuzz_test.go")

// marshalFuzzCorpusFile renders one seed in Go's testdata/fuzz encoding: a
// version line followed by one line per fuzz argument. Every target in this
// repository takes a single []byte, so one line is always enough — see the
// header comment in fuzz_test.go for why the signatures are uniform.
func marshalFuzzCorpusFile(data []byte) []byte {
	return fmt.Appendf(nil, "go test fuzz v1\n[]byte(%q)\n", data)
}

// syncFuzzCorpus writes seeds under root when updating, and otherwise asserts
// every one of them is committed with the bytes the builders produce.
//
// It deliberately does NOT assert the directories contain nothing else. A
// crasher the fuzzer discovers lands in the same directory, and unit 8 adds the
// truncated and oversized seeds alongside these; failing on extra files would
// fight both.
func syncFuzzCorpus(tb testing.TB, root string, corpora map[string][]fuzzSeed) {
	tb.Helper()
	for target, seeds := range corpora {
		dir := filepath.Join(root, "fuzz", target)
		for _, seed := range seeds {
			path := filepath.Join(dir, seed.name)
			want := marshalFuzzCorpusFile(seed.data)

			if *updateFuzzCorpus {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					tb.Fatalf("mkdir %s: %v", dir, err)
				}
				if err := os.WriteFile(path, want, 0o644); err != nil {
					tb.Fatalf("write %s: %v", path, err)
				}
				tb.Logf("wrote %s (%d seed bytes)", path, len(seed.data))
				continue
			}

			got, err := os.ReadFile(path)
			if err != nil {
				tb.Fatalf("seed corpus entry missing: %v\nRun `just fuzz-corpus` to regenerate.", err)
			}
			if !bytes.Equal(got, want) {
				tb.Errorf("%s is stale.\n got %q\nwant %q\nRun `just fuzz-corpus` to regenerate.",
					path, got, want)
			}
		}
	}
}

// TestFuzzSeedCorpus keeps the committed seeds and the builders that produced
// them in one piece.
//
// The corpora are tracked on purpose. Go loads testdata/fuzz/<target>/ entries
// on every plain `go test`, so they are the only part of the harness that runs
// without -fuzz — which is also why every entry here has to decode cleanly
// today (docs/roadmap.md §6.8.4).
func TestFuzzSeedCorpus(t *testing.T) {
	syncFuzzCorpus(t, "testdata", map[string][]fuzzSeed{
		"FuzzReflectUnmarshal":          reflectUnmarshalSeeds(t),
		"FuzzDecodeTypedOpFrame":        decodeTypedOpFrameSeeds(t),
		"FuzzDispatchInboundEventFrame": dispatchInboundEventFrameSeeds(t),
		"FuzzDecodeMeshFrame":           decodeMeshFrameSeeds(t),
		"FuzzUnmarshalTransferFrame":    unmarshalTransferFrameSeeds(t),
	})
}
