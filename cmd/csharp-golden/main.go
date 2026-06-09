// Command csharp-golden emits the cross-language golden manifest used by the
// C# Mmokit.Sdk.Core tests. It is the authoritative producer of canonical
// wire bytes (big-endian, matching pkg/quantize/wireformat.go layout) and the
// expected decoded values; the hand-ported C# DeltaDecoderCore must reproduce
// them exactly. Regenerate via `just csharp-golden`.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/zenion/mmoserver/pkg/quantize"
)

// Manifest mirrors the C# GoldenModel DTOs (csharp/.../GoldenModel.cs).
type Manifest struct {
	Dequant    []DequantCase `json:"dequant"`
	Frame      FrameCase     `json:"frame"`
	ApplyDelta []ApplyCase   `json:"applyDelta"`
	Strings    []StringCase  `json:"strings"`
}

type DequantCase struct {
	Kind     string  `json:"kind"`     // "unAngle" | "unNorm" | "unVel" | "unRel"
	Q        int64   `json:"q"`        // quantized input (already sign-extended for vel/rel)
	Scale    float64 `json:"scale"`    // for unVel/unRel; ignored otherwise
	Expected float64 `json:"expected"` // Go-computed dequantized value
}

type FrameCase struct {
	HexBytes     string       `json:"hexBytes"`
	Tick         uint32       `json:"tick"`
	Seq          uint32       `json:"seq"`
	Flags        uint32       `json:"flags"`
	FullCount    uint16       `json:"fullCount"`
	DeltaCount   uint16       `json:"deltaCount"`
	RemovedCount uint16       `json:"removedCount"`
	ExitedCount  uint16       `json:"exitedCount"`
	Full         []FullEntry  `json:"full"`
	Delta        []DeltaEntry `json:"delta"`
	RemovedIDs   []uint32     `json:"removedIDs"`
}

type FullEntry struct {
	NetID        uint32 `json:"netID"`
	Epoch        uint32 `json:"epoch"`
	EntityType   uint8  `json:"entityType"`
	ProducedAtMs uint64 `json:"producedAtMs"`
	SnapshotHex  string `json:"snapshotHex"`
	InitialHex   string `json:"initialHex"` // "" when none
}

type DeltaEntry struct {
	NetID        uint32 `json:"netID"`
	Epoch        uint32 `json:"epoch"`
	EntityType   uint8  `json:"entityType"`
	ProducedAtMs uint64 `json:"producedAtMs"`
	DeltaHex     string `json:"deltaHex"`
}

type ApplyCase struct {
	FieldSizes  []int  `json:"fieldSizes"`
	HasVarTail  bool   `json:"hasVarTail"`
	BaselineHex string `json:"baselineHex"`
	DeltaHex    string `json:"deltaHex"`
	ExpectedHex string `json:"expectedHex"`
}

type StringCase struct {
	Kind     string `json:"kind"` // "u8" | "u16"
	HexBytes string `json:"hexBytes"`
	Expected string `json:"expected"`
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func main() {
	m := Manifest{}

	// --- Dequant cases: quantize a known float in Go, record q + expected un* ---
	angQ := quantize.Angle(1.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unAngle", Q: int64(angQ), Expected: float64(quantize.UnAngle(angQ))})
	normQ := quantize.Norm(0.6)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unNorm", Q: int64(normQ), Expected: float64(quantize.UnNorm(normQ))})
	velQ := quantize.Vel(12.5, 50.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unVel", Q: int64(velQ), Scale: 50.0, Expected: float64(quantize.UnVel(velQ, 50.0))})
	relQ := quantize.Rel(-30.0, 100.0)
	m.Dequant = append(m.Dequant, DequantCase{Kind: "unRel", Q: int64(relQ), Scale: 100.0, Expected: float64(quantize.UnRel(relQ, 100.0))})

	// --- A representative frame: header + 1 full + 1 delta + 2 removed ---
	snapshot := []byte{0x01, 0x02, 0x03, 0x04}
	initial := []byte("hi")
	delta := []byte{0xFF, 0xAA, 0xBB} // arbitrary delta payload bytes
	full := FullEntry{NetID: 1001, Epoch: 7, EntityType: 3, ProducedAtMs: 1717000000123,
		SnapshotHex: hex.EncodeToString(snapshot), InitialHex: hex.EncodeToString(initial)}
	dlt := DeltaEntry{NetID: 1002, Epoch: 8, EntityType: 4, ProducedAtMs: 1717000000456,
		DeltaHex: hex.EncodeToString(delta)}
	removed := []uint32{555, 666}

	var fr []byte
	fr = append(fr, be32(42)...) // tick
	fr = append(fr, be32(7)...)  // seq
	fr = append(fr, be32(1)...)  // flags (FRESH_SNAPSHOT)
	fr = append(fr, be16(1)...)  // fullCount
	fr = append(fr, be16(1)...)  // deltaCount
	fr = append(fr, be16(2)...)  // removedCount
	fr = append(fr, be16(0)...)  // exitedCount
	// full entry
	fr = append(fr, be32(full.NetID)...)
	fr = append(fr, be32(full.Epoch)...)
	fr = append(fr, full.EntityType)
	fr = append(fr, be64(full.ProducedAtMs)...)
	fr = append(fr, be16(uint16(len(snapshot)))...)
	fr = append(fr, snapshot...)
	fr = append(fr, be16(uint16(len(initial)))...)
	fr = append(fr, initial...)
	// delta entry
	fr = append(fr, be32(dlt.NetID)...)
	fr = append(fr, be32(dlt.Epoch)...)
	fr = append(fr, dlt.EntityType)
	fr = append(fr, be64(dlt.ProducedAtMs)...)
	fr = append(fr, be16(uint16(len(delta)))...)
	fr = append(fr, delta...)
	// removed IDs
	for _, id := range removed {
		fr = append(fr, be32(id)...)
	}

	m.Frame = FrameCase{
		HexBytes: hex.EncodeToString(fr),
		Tick:     42, Seq: 7, Flags: 1, FullCount: 1, DeltaCount: 1, RemovedCount: 2, ExitedCount: 0,
		Full: []FullEntry{full}, Delta: []DeltaEntry{dlt}, RemovedIDs: removed,
	}

	// --- applyDelta: 2 fixed fields (2 bytes, 2 bytes), no var tail; change field 1 only ---
	// bitmask 1 byte = 0b00000010 (field index 1 set); delta = [bitmask][field1 new bytes]
	baseline := []byte{0x11, 0x22, 0x33, 0x44}
	deltaPayload := []byte{0x02, 0x99, 0x88} // bit for field 1, new 2 bytes for field 1
	expected := []byte{0x11, 0x22, 0x99, 0x88}
	m.ApplyDelta = append(m.ApplyDelta, ApplyCase{
		FieldSizes: []int{2, 2}, HasVarTail: false,
		BaselineHex: hex.EncodeToString(baseline),
		DeltaHex:    hex.EncodeToString(deltaPayload),
		ExpectedHex: hex.EncodeToString(expected),
	})

	// --- strings ---
	su8 := append([]byte{byte(len("alice"))}, []byte("alice")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u8", HexBytes: hex.EncodeToString(su8), Expected: "alice"})
	su16 := append(be16(uint16(len("bobbb"))), []byte("bobbb")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u16", HexBytes: hex.EncodeToString(su16), Expected: "bobbb"})

	out := filepath.Join("csharp", "Mmokit.Sdk.Core.Tests", "testdata", "delta_golden.json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	log.Printf("wrote %s (%d bytes)", out, len(data))
}
