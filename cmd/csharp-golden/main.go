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
	"math"
	"os"
	"path/filepath"

	"github.com/mmokit/mmokit/pkg/net/udpproto"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/universe"
)

// Manifest mirrors the C# GoldenModel DTOs (csharp/.../GoldenModel.cs).
type Manifest struct {
	Dequant []DequantCase `json:"dequant"`
	Frame   FrameCase     `json:"frame"`
	// InputAckFrame is a second frame carrying the FrameFlagInputAck trailer.
	// Unlike Frame above it is produced through the REAL quantize.FrameEncoder
	// rather than hand-assembled bytes, so the golden cannot drift from
	// wireformat.go.
	InputAckFrame FrameCase      `json:"inputAckFrame"`
	ApplyDelta    []ApplyCase    `json:"applyDelta"`
	Strings       []StringCase   `json:"strings"`
	Udp           UdpCases       `json:"udp"`
	Reflect       ReflectCase    `json:"reflect"`
	ClockSync     ClockSyncCase  `json:"clockSync"`
	Playback      PlaybackCase   `json:"playback"`
	Prediction    PredictionCase `json:"prediction"`
}

// ClockSyncCase pins the sliding-window-max offset estimator across langs.
type ClockSyncCase struct {
	Window       int            `json:"window"`
	Observations []ClockSyncObs `json:"observations"`
}

// ClockSyncObs is one fed observation plus the offset the estimator must
// report after consuming it. expectedOffsetMs is computed by the Go reference
// below — the single source of truth both TS and C# must reproduce.
type ClockSyncObs struct {
	ServerMs         float64 `json:"serverMs"`
	ClientNowMs      float64 `json:"clientNowMs"`
	ExpectedOffsetMs float64 `json:"expectedOffsetMs"`
}

// clockSyncRef mirrors clock-sync.ts / ClockSync.cs exactly: offset = max
// instant (serverMs-clientNowMs) over the last `window` observations.
type clockSyncRef struct {
	window      int
	instants    []float64
	idx, count  int
	offset      float64
	initialized bool
}

func (r *clockSyncRef) observe(serverMs, clientNowMs float64) {
	if r.instants == nil {
		r.instants = make([]float64, r.window)
	}
	instant := serverMs - clientNowMs
	r.instants[r.idx] = instant
	r.idx = (r.idx + 1) % len(r.instants)
	if r.count < len(r.instants) {
		r.count++
	}
	if !r.initialized {
		r.offset = instant
		r.initialized = true
		return
	}
	max := math.Inf(-1)
	for i := 0; i < r.count; i++ {
		if r.instants[i] > max {
			max = r.instants[i]
		}
	}
	r.offset = max
}

// buildClockSyncCase exercises: first-obs init, max-tracking (a less-delayed
// later sample raises the offset), a bursty cluster (clientNow fixed while
// serverMs advances), and window rollover (old max ages out after `window`).
func buildClockSyncCase() ClockSyncCase {
	const window = 40
	type in struct{ server, client float64 }
	seq := []in{
		{1000, 0},   // init -> 1000
		{1100, 150}, // instant 950, max stays 1000
		{1200, 180}, // instant 1020, max -> 1020
		{1300, 300}, // burst start (client fixed at 300): instant 1000
		{1350, 300}, // instant 1050
		{1400, 300}, // instant 1100, max -> 1100
	}
	// Window rollover: 45 samples at a steady instant of 500 (server advances
	// 50/step, client advances 50/step). After >window of these, the old 1100
	// ages out and the offset settles to 500.
	for i := 0; i < 45; i++ {
		server := 2000 + float64(i)*50
		seq = append(seq, in{server, server - 500})
	}
	ref := &clockSyncRef{window: window}
	obs := make([]ClockSyncObs, 0, len(seq))
	for _, s := range seq {
		ref.observe(s.server, s.client)
		obs = append(obs, ClockSyncObs{ServerMs: s.server, ClientNowMs: s.client, ExpectedOffsetMs: ref.offset})
	}
	return ClockSyncCase{Window: window, Observations: obs}
}

// ReflectCase carries Go's authoritative reflect-codec bytes + the expected
// decoded field values (flat encodings: f32, u32, string, bool, i64, []u32).
type ReflectCase struct {
	HexBytes string   `json:"hexBytes"`
	A        float32  `json:"a"`
	B        uint32   `json:"b"`
	C        string   `json:"c"`
	D        bool     `json:"d"`
	E        int64    `json:"e"`
	F        []uint32 `json:"f"`
}

// goldenReflect is marshalled by the real reflect codec to anchor the C#
// ReflectReader/Writer to Go's actual wire bytes. All fields flat.
type goldenReflect struct {
	A float32
	B uint32
	C string
	D bool
	E int64
	F []uint32
}

type UdpCases struct {
	Tokens  []TokenCase  `json:"tokens"`
	Packets []PacketCase `json:"packets"`
	SeqCmp  []SeqCase    `json:"seqCmp"`
}

type TokenCase struct {
	ClientSalt uint64 `json:"clientSalt"`
	ServerSalt uint64 `json:"serverSalt"`
	Token      uint32 `json:"token"`
}

type PacketCase struct {
	Kind       string `json:"kind"` // connReq|connAccept|unreliable|reliable|ack|disconnect
	HexBytes   string `json:"hexBytes"`
	Token      uint32 `json:"token"`
	Seq        uint16 `json:"seq"`
	AckSeq     uint16 `json:"ackSeq"`
	AckBits    uint32 `json:"ackBits"`
	ClientSalt uint64 `json:"clientSalt"`
	ServerSalt uint64 `json:"serverSalt"`
	PayloadHex string `json:"payloadHex"`
}

type SeqCase struct {
	S1      uint16 `json:"s1"`
	S2      uint16 `json:"s2"`
	Greater bool   `json:"greater"`
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
	ExitedIDs    []uint32     `json:"exitedIDs"`
	// HasInputAck / ExpectedInputAck cover the optional four-byte
	// processed-input-sequence trailer announced by FrameFlagInputAck.
	HasInputAck      bool   `json:"hasInputAck"`
	ExpectedInputAck uint32 `json:"expectedInputAck"`
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

	m.InputAckFrame = buildInputAckFrameCase()
	m.Playback = buildPlaybackCase()
	m.Prediction = buildPredictionCase()

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

	// --- applyDelta with a variable-length tail: 1 fixed field (2 bytes) +
	// a changed string tail. Exercises the var-tail rebuild branch.
	// baseline = fixed[AABB] + u16 len(2) + "xy"; delta changes ONLY the tail:
	// bitmask 0x02 (tail logical-field bit 1 set, fixed bit 0 clear) + u16 len(3) + "zzz".
	// result = fixed[AABB] + u16 len(3) + "zzz".
	vtBaseline := []byte{0xAA, 0xBB, 0x00, 0x02, 0x78, 0x79}       // AABB | 0002 | "xy"
	vtDelta := []byte{0x02, 0x00, 0x03, 0x7A, 0x7A, 0x7A}          // bitmask | 0003 | "zzz"
	vtExpected := []byte{0xAA, 0xBB, 0x00, 0x03, 0x7A, 0x7A, 0x7A} // AABB | 0003 | "zzz"
	m.ApplyDelta = append(m.ApplyDelta, ApplyCase{
		FieldSizes: []int{2}, HasVarTail: true,
		BaselineHex: hex.EncodeToString(vtBaseline),
		DeltaHex:    hex.EncodeToString(vtDelta),
		ExpectedHex: hex.EncodeToString(vtExpected),
	})

	// --- strings ---
	su8 := append([]byte{byte(len("alice"))}, []byte("alice")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u8", HexBytes: hex.EncodeToString(su8), Expected: "alice"})
	su16 := append(be16(uint16(len("bobbb"))), []byte("bobbb")...)
	m.Strings = append(m.Strings, StringCase{Kind: "u16", HexBytes: hex.EncodeToString(su16), Expected: "bobbb"})

	// --- UDP protocol golden cases (udpproto is little-endian) ---
	var u UdpCases
	cs, ss := uint64(0x1122334455667788), uint64(0x99AABBCCDDEEFF00)
	u.Tokens = append(u.Tokens, TokenCase{ClientSalt: cs, ServerSalt: ss, Token: udpproto.MakeToken(cs, ss)})

	pl := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	u.Packets = append(u.Packets,
		PacketCase{Kind: "connReq", HexBytes: hex.EncodeToString(udpproto.EncodeConnReq(cs)), ClientSalt: cs},
		PacketCase{Kind: "connAccept", HexBytes: hex.EncodeToString(udpproto.EncodeConnAccept(cs, ss)), ClientSalt: cs, ServerSalt: ss},
		PacketCase{Kind: "unreliable", HexBytes: hex.EncodeToString(udpproto.EncodeUnreliable(0xCAFEBABE, pl)), Token: 0xCAFEBABE, PayloadHex: hex.EncodeToString(pl)},
		PacketCase{Kind: "reliable", HexBytes: hex.EncodeToString(udpproto.EncodeReliable(0xCAFEBABE, 7, pl)), Token: 0xCAFEBABE, Seq: 7, PayloadHex: hex.EncodeToString(pl)},
		PacketCase{Kind: "ack", HexBytes: hex.EncodeToString(udpproto.EncodeACK(0xCAFEBABE, 12, 0x0000000B)), Token: 0xCAFEBABE, AckSeq: 12, AckBits: 0x0000000B},
		PacketCase{Kind: "disconnect", HexBytes: hex.EncodeToString(udpproto.EncodeDisconnect(0xCAFEBABE)), Token: 0xCAFEBABE},
	)

	u.SeqCmp = append(u.SeqCmp,
		SeqCase{S1: 5, S2: 3, Greater: udpproto.SeqGreaterThan(5, 3)},
		SeqCase{S1: 3, S2: 5, Greater: udpproto.SeqGreaterThan(3, 5)},
		SeqCase{S1: 1, S2: 65535, Greater: udpproto.SeqGreaterThan(1, 65535)}, // wrap: 1 > 65535
		SeqCase{S1: 65535, S2: 1, Greater: udpproto.SeqGreaterThan(65535, 1)},
	)
	m.Udp = u

	// --- reflect-codec golden: marshalled by Go's REAL codec, cross-checked
	// by the C# ReflectReader/Writer. "héllo" is multibyte UTF-8 so the u16
	// string prefix is exercised as a BYTE length, not a char count. ---
	gr := goldenReflect{A: 1.5, B: 0xDEADBEEF, C: "héllo", D: true, E: -42, F: []uint32{7, 8, 9}}
	grBytes, err := universe.ReflectMarshal(&gr)
	if err != nil {
		log.Fatalf("marshal reflect golden: %v", err)
	}
	m.Reflect = ReflectCase{
		HexBytes: hex.EncodeToString(grBytes),
		A:        gr.A, B: gr.B, C: gr.C, D: gr.D, E: gr.E, F: gr.F,
	}

	m.ClockSync = buildClockSyncCase()

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

// buildInputAckFrameCase produces a frame carrying the FrameFlagInputAck
// trailer through the REAL quantize.FrameEncoder.
//
// The hand-assembled Frame case above is deliberate for the base layout — it
// proves the encoder itself matches the documented byte order. This one is
// deliberately the opposite: the four-byte processed-input-sequence trailer is
// new and lightly covered, so generating it from the production encoder means
// the golden can never drift from wireformat.go, only fail when a port does.
func buildInputAckFrameCase() FrameCase {
	snapshot := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	initial := []byte("ack")
	delta := []byte{0x01, 0x02}

	full := quantize.FullEntry{
		NetID: 2001, Epoch: 11, EntityType: 5, ProducedAtMs: 1717000009999,
		Snapshot: snapshot, InitialData: initial,
	}
	dlt := quantize.DeltaEntry{
		NetID: 2002, Epoch: 12, EntityType: 6, ProducedAtMs: 1717000010111,
		Data: delta,
	}
	removed := []uint32{4242}
	exited := []uint32{4343, 4444}
	const inputAck = uint32(987654321)
	const tick = uint32(99)
	const seq = uint32(1234)

	enc := quantize.NewFrameEncoder(512)
	raw := enc.Encode(
		tick, seq, quantize.FrameFlagFreshSnapshot,
		[]quantize.FullEntry{full}, []quantize.DeltaEntry{dlt},
		removed, exited, inputAck,
	)
	encoded := append([]byte(nil), raw...)

	// Read the flags back rather than asserting them: Encode ORs in
	// FrameFlagInputAck itself, and the golden must record what shipped.
	flags := binary.BigEndian.Uint32(encoded[8:12])

	return FrameCase{
		HexBytes: hex.EncodeToString(encoded),
		Tick:     tick, Seq: seq, Flags: flags,
		FullCount: 1, DeltaCount: 1,
		RemovedCount: uint16(len(removed)), ExitedCount: uint16(len(exited)),
		Full: []FullEntry{{
			NetID: full.NetID, Epoch: full.Epoch, EntityType: full.EntityType,
			ProducedAtMs: full.ProducedAtMs,
			SnapshotHex:  hex.EncodeToString(snapshot),
			InitialHex:   hex.EncodeToString(initial),
		}},
		Delta: []DeltaEntry{{
			NetID: dlt.NetID, Epoch: dlt.Epoch, EntityType: dlt.EntityType,
			ProducedAtMs: dlt.ProducedAtMs,
			DeltaHex:     hex.EncodeToString(delta),
		}},
		RemovedIDs:       removed,
		ExitedIDs:        exited,
		HasInputAck:      true,
		ExpectedInputAck: inputAck,
	}
}
