package universe

import (
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// Fuzz harness for the decoder families docs/roadmap.md §6.7 names: the
// reflection codec, the operation AND input frame decoders, and the mesh frame
// decoder. UnmarshalTransferFrame is covered too, but note it is a PAYLOAD
// decoder reached from inside decodeMeshFrame — covering it does not cover the
// mesh-frame family, which is why both targets exist.
//
// Two rules hold for every target in this file.
//
//   - No recover(). A target that swallows the panic reports nothing, and the
//     whole point of the harness is that the fuzzer sees the crash. The panic
//     barriers unit 5 added live on the ingress goroutines, deliberately
//     outside the decoders, so these targets still observe raw panics.
//   - Every committed seed decodes cleanly TODAY. Go runs testdata/fuzz
//     entries as ordinary subtests under a plain `go test`, so a crashing seed
//     committed before the checked decoder lands reddens the required gate for
//     days (§6.8.4). The truncated and oversized seeds belong in the same
//     commit as the checked decoder, not this one.
//
// Every target takes a single []byte. That is not a stylistic choice: Go's
// seed-corpus file format encodes one line per fuzz argument, and a
// single-[]byte signature keeps the committed corpus files to one trivially
// verifiable form. Targets that need to select among several shapes take the
// selector from the first byte of the input.

// ── Reflection codec ────────────────────────────────────────────────────────

// The registered wire shapes FuzzReflectUnmarshal drives, chosen for the arms
// they reach rather than for realism.
type (
	// fuzzLeadingString puts a string FIRST so a truncated body hits the u16
	// length prefix at offset 0, with no preceding field to absorb the damage.
	fuzzLeadingString struct {
		Name  string
		Level uint16
	}

	// fuzzStringSlice mirrors chat.ChatBulkSetMembersRequest{ChannelID string;
	// UserIDs []string}: the generic-slice amplifier named in CE-002, which is
	// registered RouteGatewayLocal and therefore reflect-decoded on the gateway
	// before any handler-side auth check.
	fuzzStringSlice struct {
		ChannelID string
		UserIDs   []string
	}

	// fuzzByteSlice exercises the []byte fast path, [u32 len][N bytes] — the
	// only arm whose length prefix is a u32 and the one that allocates before
	// reading.
	fuzzByteSlice struct {
		Tag  uint32
		Blob []byte
	}

	// fuzzNested exercises struct recursion, a fixed array, and both
	// length-prefixed arms behind an offset that earlier fields have moved.
	fuzzNested struct {
		Head fuzzLeadingString
		Vec  [3]float32
		Body fuzzByteSlice
	}

	// fuzzGolden is cmd/csharp-golden/main.go's goldenReflect, field for field.
	// Its seed is the exact reflect.hexBytes the C# ReflectReader is pinned to,
	// so the fuzzer starts from the one shape three languages agree on.
	fuzzGolden struct {
		A float32
		B uint32
		C string
		D bool
		E int64
		F []uint32
	}
)

// reflectFuzzShapes is indexed by the selector byte. Order is load-bearing:
// the committed corpus files encode the index, so appending is safe and
// reordering silently repoints every seed at a different shape.
var reflectFuzzShapes = []reflect.Type{
	reflect.TypeFor[fuzzLeadingString](),
	reflect.TypeFor[fuzzStringSlice](),
	reflect.TypeFor[fuzzByteSlice](),
	reflect.TypeFor[fuzzNested](),
	reflect.TypeFor[fuzzGolden](),
}

// goldenReflectHex is cmd/csharp-golden/main.go's emitted reflect.hexBytes.
// Pinned here rather than read from delta_golden.json so the seed cannot
// quietly stop being the cross-language anchor it claims to be.
const goldenReflectHex = "0000c03fefbeadde060068c3a96c6c6f01d6ffffffffffffff0300070000000800000009000000"

// reflectUnmarshalSeeds returns one valid round-trip body per shape, each
// prefixed with its shape selector.
func reflectUnmarshalSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()

	golden := &fuzzGolden{A: 1.5, B: 0xDEADBEEF, C: "héllo", D: true, E: -42, F: []uint32{7, 8, 9}}
	goldenBytes := mustMarshal(tb, golden)
	if got := hex.EncodeToString(goldenBytes); got != goldenReflectHex {
		tb.Fatalf("fuzzGolden no longer marshals to the C# golden bytes:\n got %s\nwant %s\n"+
			"Either the shape drifted from cmd/csharp-golden's goldenReflect or the codec changed; "+
			"fix the shape, or regenerate the golden and update goldenReflectHex.", got, goldenReflectHex)
	}

	values := []any{
		&fuzzLeadingString{Name: "border-cell", Level: 7},
		&fuzzStringSlice{ChannelID: "general", UserIDs: []string{"alice", "bob", ""}},
		&fuzzByteSlice{Tag: 0x0BADC0DE, Blob: []byte{0x00, 0x01, 0xFE, 0xFF}},
		&fuzzNested{
			Head: fuzzLeadingString{Name: "nested", Level: 65535},
			Vec:  [3]float32{-1, 0, 1.5},
			Body: fuzzByteSlice{Tag: 1, Blob: nil},
		},
		golden,
	}
	if len(values) != len(reflectFuzzShapes) {
		tb.Fatalf("seed count %d != shape count %d", len(values), len(reflectFuzzShapes))
	}

	names := []string{"leading-string", "string-slice", "byte-slice", "nested", "csharp-golden"}
	seeds := make([]fuzzSeed, 0, len(values))
	for i, v := range values {
		body := goldenBytes
		if i != len(values)-1 {
			body = mustMarshal(tb, v)
		}
		seeds = append(seeds, fuzzSeed{
			name: names[i],
			data: append([]byte{byte(i)}, body...),
		})
	}
	// An empty input is the cheapest possible truncation and every arm must
	// survive it once the decoder is checked. It does not crash today because
	// the target returns before decoding, which is what makes it committable.
	seeds = append(seeds, fuzzSeed{name: "empty", data: nil})

	// The crashers unit 6 deliberately withheld, landing in the same commit as
	// the checked decoder exactly as docs/roadmap.md §6.8.4 requires: committed
	// earlier they would have reddened a plain `go test` for days, because Go
	// runs testdata/fuzz entries as ordinary subtests.
	//
	// Each of these aborted the process before CE-002 unit 8. They are the
	// permanent regression seeds for that: any future decoder change that
	// reintroduces an unchecked read or an uncharged allocation fails here
	// under a plain `go test`, without needing a fuzzing budget.
	seeds = append(seeds,
		// The crasher FuzzReflectUnmarshal found in 0.14 s on HEAD. Selector
		// 0x30 % 5 = 3 selects fuzzNested and leaves an empty body, so
		// Head.Name's u16 length prefix is read off the end of a zero-byte
		// buffer.
		fuzzSeed{name: "crash-nested-empty-body", data: []byte("0")},
		// fuzzLeadingString with [u16 len = 0xFFFF][no bytes]: the string arm
		// used slen directly as a slice bound.
		fuzzSeed{name: "crash-string-past-end", data: []byte{0x00, 0xFF, 0xFF}},
		// fuzzStringSlice with an empty ChannelID and UserIDs declaring 65535
		// elements: reflect.MakeSlice reserved ~1 MiB from a 5-byte body.
		fuzzSeed{name: "crash-slice-65535-elems", data: []byte{0x01, 0x00, 0x00, 0xFF, 0xFF}},
		// fuzzByteSlice with [u32 len = 0xFFFFFFFF]: make([]byte, 4294967295).
		// The one seed whose old failure mode depended on the host — a
		// survivable 4 GiB reservation where overcommit allows it, Go's
		// unrecoverable out-of-memory fatal error where it does not, and no
		// panic barrier can help with the latter. It is committable only
		// because the length is now compared against the limit and the
		// remaining payload before it is used as an allocation size.
		fuzzSeed{name: "crash-bytes-4gib", data: []byte{0x02, 0, 0, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}},
	)
	return seeds
}

// FuzzReflectUnmarshal drives the reflection decoder over a fixed set of
// registered shapes. data[0] selects the shape; the rest is the body.
//
// Expected to find crashers immediately until the checked decoder lands —
// TestReflectUnmarshal_Truncated is the hand-written inventory of what it will
// find. Do not add a recover here to make it quiet.
func FuzzReflectUnmarshal(f *testing.F) {
	for _, s := range reflectUnmarshalSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		shape := reflectFuzzShapes[int(data[0])%len(reflectFuzzShapes)]
		ReflectUnmarshal(data[1:], reflect.New(shape).Interface())
	})
}

// ── Operation frames (channel 0x01) ─────────────────────────────────────────

func decodeTypedOpFrameSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()

	// stripChannel mirrors the drain path: the channel byte is already gone by
	// the time DecodeTypedOpFrame sees the payload.
	stripChannel := func(frame []byte) []byte { return frame[1:] }

	req := mustMarshal(tb, &fuzzStringSlice{ChannelID: "general", UserIDs: []string{"alice"}})
	return []fuzzSeed{
		{name: "empty-body", data: stripChannel(EncodeTypedOpFrame(0xC0FFEE01, 1, nil))},
		{name: "reflect-body", data: stripChannel(EncodeTypedOpFrame(0xC0FFEE02, 0xFFFFFFFFFFFFFFFF, req))},
		{name: "header-only-truncated", data: []byte{0x01, 0x02, 0x03}},
	}
}

// FuzzDecodeTypedOpFrame covers the 0x01 frame decoder — the pre-auth path
// CE-002 calls out at gateway.go:1080 and ops/router.go.
//
// The post-condition is the contract every caller relies on: a frame the
// decoder ACCEPTS must yield a body that is genuinely a view into the payload,
// because op_dispatch hands that body straight to the reflection codec.
func FuzzDecodeTypedOpFrame(f *testing.F) {
	for _, s := range decodeTypedOpFrameSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		const headerLen = 4 + 8 + 4
		_, _, body, err := DecodeTypedOpFrame(payload)
		if err != nil {
			return
		}
		if headerLen+len(body) > len(payload) {
			t.Fatalf("accepted a frame whose body escapes the payload: body=%d payload=%d",
				len(body), len(payload))
		}
	})
}

// ── Input frames (channel 0x00) ─────────────────────────────────────────────

// Stable typeIDs for the fuzz fixture's registered client-input types. Real
// IDs are fnv32 of a package-qualified name; these only have to be distinct
// and constant so the committed corpus keeps resolving.
const (
	fuzzInputPingTypeID uint32 = 0x50494E47 // "PING"
	fuzzInputMoveTypeID uint32 = 0x4D4F5645 // "MOVE"
)

type (
	fuzzClientPing struct {
		Seq  uint32
		Note string
	}
	fuzzClientMove struct {
		X, Y float32
		Tags []string
	}
)

// clientInputEntry builds one [u32 typeID][u32 bodyLen][body] entry.
func clientInputEntry(typeID uint32, body []byte) []byte {
	entry := make([]byte, clientInputHeaderBytes+len(body))
	binary.LittleEndian.PutUint32(entry[0:4], typeID)
	binary.LittleEndian.PutUint32(entry[4:8], uint32(len(body)))
	copy(entry[clientInputHeaderBytes:], body)
	return entry
}

// goldenDeltaFrameHex is cmd/csharp-golden/main.go's frame.hexBytes: a
// well-formed replication frame travelling the OTHER direction. It is seeded
// here on purpose — a real server→client frame arriving on the client→server
// walker is exactly the confusion a fuzzer should start from.
const goldenDeltaFrameHex = "0000002a00000007000000010001000100020000000003e900000007030000018fc52cd27b0004" +
	"0102030400026869000003ea0000000804000001" + "8fc52cd2b800030faabb0000022b0000029a"

func dispatchInboundEventFrameSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()

	ping := clientInputEntry(fuzzInputPingTypeID, mustMarshal(tb, &fuzzClientPing{Seq: 9, Note: "hi"}))
	move := clientInputEntry(fuzzInputMoveTypeID, mustMarshal(tb, &fuzzClientMove{X: 1, Y: -2, Tags: []string{"a"}}))

	golden, err := hex.DecodeString(goldenDeltaFrameHex)
	if err != nil {
		tb.Fatalf("decode goldenDeltaFrameHex: %v", err)
	}

	return []fuzzSeed{
		{name: "single-entry", data: ping},
		{name: "batched-entries", data: append(append([]byte(nil), ping...), move...)},
		{name: "unregistered-typeid", data: clientInputEntry(0xDEADBEEF, []byte{1, 2, 3})},
		{name: "header-underrun", data: []byte{0x01, 0x02, 0x03}},
		{name: "csharp-golden-delta-frame", data: golden},
	}
}

// FuzzDispatchInboundEventFrame covers the channel-0x00 walker, which is a
// DISTINCT decoder from the 0x01 op decoder: it walks a batch of concatenated
// [u32 typeID][u32 bodyLen][body] entries until the buffer is exhausted.
//
// Note what this target can and cannot report. dispatchOneClientInput already
// carries a per-frame recover, so a panic inside the reflection decode of one
// entry is swallowed there and never reaches the fuzzer. What is exposed here
// is the WALK: the offset arithmetic, the bodyLen bound, and termination. Body
// decoding is FuzzReflectUnmarshal's job.
func FuzzDispatchInboundEventFrame(f *testing.F) {
	prev := ClientInputHooks.TypeOfTypeID
	ClientInputHooks.TypeOfTypeID = func(id uint32) reflect.Type {
		switch id {
		case fuzzInputPingTypeID:
			return reflect.TypeFor[fuzzClientPing]()
		case fuzzInputMoveTypeID:
			return reflect.TypeFor[fuzzClientMove]()
		default:
			return nil
		}
	}
	f.Cleanup(func() { ClientInputHooks.TypeOfTypeID = prev })

	// One fixture for the whole run: the walker is stateless, and rebuilding a
	// Stage per input would make the fuzzer measure ECS setup instead of the
	// decoder.
	cell := newTestCell("fuzz-input", CellID{X: 0, Y: 0})
	ent := cell.Stage.Spawn(component.Position{X: 10, Y: 20})
	sess := &engine.PlayerSession{ConnID: 1, Username: "fuzz", Entity: ent.Handle()}

	for _, s := range dispatchInboundEventFrameSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, frame []byte) {
		cell.Stage.dispatchInboundEventFrame(sess, frame)
	})
}

// ── Mesh frames ─────────────────────────────────────────────────────────────

func decodeMeshFrameSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()

	blob := mustTransferFrameBytes(tb)
	msgs := []struct {
		name string
		msg  CellMessage
	}{
		{"border-frame", CellMessage{
			Type: MsgBorderFrame, FromCellID: "cell_0_0",
			BorderFrame: []byte{0x01, 0x02, 0x03, 0x04},
		}},
		{"handoff", CellMessage{
			Type: MsgHandoff, FromCellID: "cell_0_0",
			Handoff: &HandoffPayload{NetID: 42, Epoch: 3, CommitTick: 900, TransferBlob: blob, ConnID: 7},
		}},
		{"handoff-accepted", CellMessage{
			Type: MsgHandoffAccepted, FromCellID: "cell_1_0",
			HandoffAck: &HandoffAckPayload{NetID: 42, Epoch: 3, CommitTick: 900},
		}},
		{"forward-input", CellMessage{
			Type: MsgForwardInput, FromCellID: "cell_0_0",
			ForwardInput: &ForwardInputPayload{
				GatewayID: "gw-1", ConnID: 7, SessionEpoch: 2,
				InputBlob: clientInputEntry(fuzzInputPingTypeID, []byte{0, 0, 0, 0, 0, 0}),
			},
		}},
		{"cross-action", CellMessage{
			Type: MsgCrossCellAction, FromCellID: "cell_0_0",
			Action: &CrossCellAction{
				Type: ActionType(1), TargetNetID: 42, SourceNetID: 43,
				SourceCellID: "cell_0_0", Payload: []byte{0xAA, 0xBB},
			},
		}},
		{"player-assignment", CellMessage{
			Type: MsgPlayerAssignment, FromCellID: "cell_0_0",
			Assignment: &PlayerAssignment{
				ConnID: 7, UserID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
				Username: "alice", SessionToken: "tok", IsReconnect: true, StreamGeneration: 2,
				SpawnLocation: coords.Location{X: 1, Y: 2, Facing: 0.5, Tag: "start"},
			},
		}},
		{"session-transfer", CellMessage{
			Type: MsgSessionTransfer, FromCellID: "cell_0_0",
			Sessions: []SessionTransfer{{
				ConnID: 7, GatewayID: "gw-1", GatewayConnID: 11, SessionEpoch: 2,
				UserID:   uuid.MustParse("11111111-2222-3333-4444-555555555555"),
				Username: "alice", StreamGeneration: 2, StateTag: "docked",
				Data: []byte{0x01, 0x02},
			}},
		}},
		{"spawn-transfer", CellMessage{
			Type: MsgSpawnTransfer, FromCellID: "cell_0_0",
			Spawn: &SpawnTransfer{
				ConnID: 7, Username: "alice", StreamGeneration: 2,
				SpawnLocation: coords.Location{X: 3, Y: 4},
			},
		}},
	}

	seeds := make([]fuzzSeed, 0, len(msgs)+2)
	for _, m := range msgs {
		frame, err := encodeCellMessage(m.msg, "cell_1_0")
		if err != nil {
			tb.Fatalf("encodeCellMessage(%s): %v", m.name, err)
		}
		wire, err := proto.Marshal(frame)
		if err != nil {
			tb.Fatalf("proto.Marshal(%s): %v", m.name, err)
		}
		seeds = append(seeds, fuzzSeed{name: m.name, data: wire})
	}

	// ServiceEvent has no encodeCellMessage arm — routeInboundFrame handles it
	// inline and decodeMeshFrame rejects it — but it is a real oneof variant a
	// peer can send, so the decoder must see it.
	svc, err := proto.Marshal(&meshpb.MeshFrame{
		DestCellId: "cell_1_0",
		Msg: &meshpb.MeshFrame_ServiceEvent{ServiceEvent: &meshpb.ServiceEvent{
			SourceProcessId: "proc-1",
			TypeName:        "github.com/zenion/mmoserver/pkg/service.SessionEnterEvent",
			Payload:         []byte{0x01},
			Sequence:        3,
		}},
	})
	if err != nil {
		tb.Fatalf("proto.Marshal(service-event): %v", err)
	}
	seeds = append(seeds, fuzzSeed{name: "service-event", data: svc})

	// No oneof set at all: decodeMeshFrame's nil-payload guard.
	bare, err := proto.Marshal(&meshpb.MeshFrame{DestCellId: "cell_1_0"})
	if err != nil {
		tb.Fatalf("proto.Marshal(no-payload): %v", err)
	}
	seeds = append(seeds, fuzzSeed{name: "no-oneof", data: bare})

	return seeds
}

// FuzzDecodeMeshFrame covers the mesh frame decoder, the third family §6.7
// names. The input is raw MeshData wire bytes: proto.Unmarshal owns its own
// error handling, and everything past it is decodeMeshFrame's oneof dispatch.
//
// This is the surface a cluster-secret holder reaches today — MeshData does no
// identity checking at all (CE-006 criterion 12) — so "authenticated peer" is
// not a reason to treat these bytes as trusted.
func FuzzDecodeMeshFrame(f *testing.F) {
	for _, s := range decodeMeshFrameSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		var frame meshpb.MeshFrame
		if err := proto.Unmarshal(wire, &frame); err != nil {
			return
		}
		if _, err := decodeMeshFrame(&frame); err != nil {
			return
		}
	})
}

// ── Transfer payloads ───────────────────────────────────────────────────────

// mustTransferFrameBytes builds a realistic player transfer blob: the payload a
// MsgHandoff carries, and the one Cell.guardDecode peeks at before promoting.
func mustTransferFrameBytes(tb testing.TB) []byte {
	tb.Helper()
	blob, err := MarshalTransferFrame(&TransferFrame{
		NetworkID: 42, Epoch: 3, StreamGeneration: 2, EntityType: 1,
		ConnID: 7, GatewayID: "gw-1", GatewayConnID: 11, Username: "alice",
		UserID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		PosX:   100.5, PosY: -20.25, VelX: 1, VelY: -1, Rotation: 0.5,
		Collider: component.Collider{Radius: 8, Width: 4, Height: 4, Layer: 1, Shape: 0},
		CellX:    0, CellY: 0, DebugFlags: 0,
		Components: []ComponentSlice{
			{ID: 1, Data: []byte{0x01, 0x02, 0x03, 0x04}},
			{ID: 2, Data: nil},
		},
	})
	if err != nil {
		tb.Fatalf("MarshalTransferFrame: %v", err)
	}
	return blob
}

func unmarshalTransferFrameSeeds(tb testing.TB) []fuzzSeed {
	tb.Helper()
	player := mustTransferFrameBytes(tb)

	npc, err := MarshalTransferFrame(&TransferFrame{
		NetworkID: 900, Epoch: 1, EntityType: 2,
		PosX: 1, PosY: 2, CellX: -1, CellY: 3,
	})
	if err != nil {
		tb.Fatalf("MarshalTransferFrame(npc): %v", err)
	}

	return []fuzzSeed{
		{name: "player-with-components", data: player},
		{name: "npc-no-components", data: npc},
		{name: "shorter-than-header", data: make([]byte, 16)},
	}
}

// FuzzUnmarshalTransferFrame is one of the two controls that should pass
// immediately: this decoder already reserves the fixed tail behind every
// variable length before reading it, and rejects an impossible component count
// before allocating. A crash here means that discipline regressed.
//
// PeekTransferPlayer reads the SAME bytes on the promotion path
// (Cell.guardDecode), so it is driven from the same input.
func FuzzUnmarshalTransferFrame(f *testing.F) {
	for _, s := range unmarshalTransferFrameSeeds(f) {
		f.Add(s.data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Unconditional: PeekTransferPlayer is reached before, and independently
		// of, a successful full decode.
		PeekTransferPlayer(data)

		frame, err := UnmarshalTransferFrame(data)
		if err != nil {
			return
		}
		for i, c := range frame.Components {
			if len(c.Data) > len(data) {
				t.Fatalf("component %d (id=%d) view is %d bytes, longer than the %d-byte input",
					i, c.ID, len(c.Data), len(data))
			}
		}
	})
}
