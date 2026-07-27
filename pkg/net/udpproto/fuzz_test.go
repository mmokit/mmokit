package udpproto

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// FuzzUDPProtoDecode is one of the two controls in the CE-002 fuzz harness
// (docs/roadmap.md §6.3 criterion 6): every decoder in this package compares
// len(data) against its fixed header size before reading and returns
// ErrTooShort or ErrBadProtocolID, so the target should pass immediately and
// keep passing. It exists to prove that, and to notice if it ever stops being
// true — these are the first bytes off an unauthenticated UDP socket.
//
// No recover(): a panic here is a finding, not noise to be swallowed.

// fuzzSeed is one committed entry of the seed corpus.
type fuzzSeed struct {
	name string
	data []byte
}

var updateFuzzCorpus = flag.Bool("update-fuzz-corpus", false,
	"rewrite testdata/fuzz seed corpora from the builders in fuzz_test.go")

// goldenPacketHex is the udp.packets[].hexBytes block emitted by
// cmd/csharp-golden/main.go — one packet per type, the same bytes the C# SDK
// is pinned to. Seeding from the cross-language golden means the fuzzer starts
// from packets a real client actually sends.
var goldenPacketHex = []struct{ name, hex string }{
	{"conn-req", "03454d41478877665544332211"},
	{"conn-accept", "04454d4147887766554433221100ffeeddccbbaa99"},
	{"unreliable", "00bebafecadeadbeef"},
	{"reliable", "01bebafeca0700deadbeef"},
	{"ack", "02bebafeca0c000b000000"},
	{"disconnect", "05bebafeca"},
}

func udpProtoSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()
	seeds := make([]fuzzSeed, 0, len(goldenPacketHex)+3)
	for _, g := range goldenPacketHex {
		data, err := hex.DecodeString(g.hex)
		if err != nil {
			tb.Fatalf("decode golden %s: %v", g.name, err)
		}
		seeds = append(seeds, fuzzSeed{name: g.name, data: data})
	}
	seeds = append(seeds,
		// A type byte with nothing behind it: the ErrTooShort path for every
		// decoder at once.
		fuzzSeed{name: "type-byte-only", data: []byte{TypeReliable}},
		// Right length, wrong magic: the ErrBadProtocolID path.
		fuzzSeed{name: "bad-protocol-id", data: []byte{TypeConnReq, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}},
		// A type byte no decoder claims.
		fuzzSeed{name: "unknown-type", data: []byte{0x7F, 0xDE, 0xAD, 0xBE, 0xEF}},
	)
	return seeds
}

func FuzzUDPProtoDecode(f *testing.F) {
	for _, s := range udpProtoSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		switch data[0] {
		case TypeConnReq:
			if _, err := DecodeConnReq(data); err != nil {
				return
			}
		case TypeConnAccept:
			if _, _, err := DecodeConnAccept(data); err != nil {
				return
			}
		case TypeUnreliable:
			_, payload, err := DecodeUnreliable(data)
			if err != nil {
				return
			}
			// The payload is a view into data; the server hands it straight to
			// the reflection codec, so an over-long view is a real bug.
			if len(payload) != len(data)-UnreliableHeaderSize {
				t.Fatalf("unreliable payload is %d bytes, want %d",
					len(payload), len(data)-UnreliableHeaderSize)
			}
		case TypeReliable:
			_, _, payload, err := DecodeReliable(data)
			if err != nil {
				return
			}
			if len(payload) != len(data)-ReliableHeaderSize {
				t.Fatalf("reliable payload is %d bytes, want %d",
					len(payload), len(data)-ReliableHeaderSize)
			}
		case TypeACK:
			if _, _, _, err := DecodeACK(data); err != nil {
				return
			}
		case TypeDisconnect:
			if _, err := DecodeDisconnect(data); err != nil {
				return
			}
		}
	})
}

// marshalFuzzCorpusFile renders one seed in Go's testdata/fuzz encoding.
// Duplicated from pkg/universe/fuzz_corpus_test.go rather than exported: test
// helpers cannot cross a package boundary without becoming production API, and
// this is three lines.
func marshalFuzzCorpusFile(data []byte) []byte {
	return fmt.Appendf(nil, "go test fuzz v1\n[]byte(%q)\n", data)
}

// TestFuzzSeedCorpus keeps the committed seeds and their builder in one piece.
// See pkg/universe/fuzz_corpus_test.go for why the corpora are tracked and why
// extra files in the directory are tolerated.
func TestFuzzSeedCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "fuzz", "FuzzUDPProtoDecode")
	for _, seed := range udpProtoSeeds(t) {
		path := filepath.Join(dir, seed.name)
		want := marshalFuzzCorpusFile(seed.data)

		if *updateFuzzCorpus {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			t.Logf("wrote %s (%d seed bytes)", path, len(seed.data))
			continue
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("seed corpus entry missing: %v\nRun `just fuzz-corpus` to regenerate.", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale.\n got %q\nwant %q\nRun `just fuzz-corpus` to regenerate.",
				path, got, want)
		}
	}
}
