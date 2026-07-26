package bot

import (
	"encoding/binary"
	"math"
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
)

func TestDecodeBinaryFrameAcceptsEmptyFrame(t *testing.T) {
	decoderState := newDeltaDecoderState()
	frame := botTestFrame(41, 0, nil, nil)

	state, ok := decodeBinaryFrame(frame, 1, decoderState, newDeltaDecoders())
	if !ok {
		t.Fatal("legal 20-byte empty frame was rejected")
	}
	if state.Tick != 41 {
		t.Fatalf("tick = %d, want 41", state.Tick)
	}
	if len(state.Entities) != 0 {
		t.Fatalf("entities = %d, want 0", len(state.Entities))
	}
}

func TestDecodeBinaryFrameFreshSnapshotClearsBaselines(t *testing.T) {
	decoderState := newDeltaDecoderState()
	decoderState.baselines = map[uint32]*baselineEntry{
		7: {
			snapshot:       botTestSnapshot(gamecomp.KindShip, 12),
			authorityEpoch: 3,
			entityType:     gamecomp.KindShip,
			pilotName:      "stale",
		},
	}
	frame := botTestFrame(42, quantize.FrameFlagFreshSnapshot, nil, nil)

	_, ok := decodeBinaryFrame(frame, 1, decoderState, newDeltaDecoders())
	if !ok {
		t.Fatal("fresh empty frame was rejected")
	}
	if len(decoderState.baselines) != 0 {
		t.Fatalf("fresh snapshot retained %d baselines", len(decoderState.baselines))
	}
}

func TestDecodeBinaryFrameFreshSnapshotPreservesAuthorityFence(t *testing.T) {
	const netID = uint32(8)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()
	newer := quantize.FullEntry{
		NetID:      netID,
		Epoch:      4,
		EntityType: gamecomp.KindShip,
		Snapshot:   botTestSnapshot(gamecomp.KindShip, 4),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(1, 0, []quantize.FullEntry{newer}, nil), 1, decoderState, decoders); !ok {
		t.Fatal("newer frame decode failed")
	}

	older := newer
	older.Epoch = 3
	older.Snapshot = botTestSnapshot(gamecomp.KindShip, 3)
	state, ok := decodeBinaryFrame(
		botTestFrame(2, quantize.FrameFlagFreshSnapshot, []quantize.FullEntry{older}, nil),
		1,
		decoderState,
		decoders,
	)
	if !ok {
		t.Fatal("delayed fresh frame decode failed")
	}
	if _, exists := state.Entities[netID]; exists {
		t.Fatal("delayed fresh frame resurrected an older authority epoch")
	}
	if got := decoderState.authorityEpochs[netID]; got != 4 {
		t.Fatalf("authority fence = %d, want 4", got)
	}
}

func TestDecodeBinaryFrameRejectsStaleStreamFreshBeforeMutation(t *testing.T) {
	const netID = uint32(9)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()

	oldAuthority := quantize.FullEntry{
		NetID:      netID,
		Epoch:      7,
		EntityType: gamecomp.KindShip,
		Snapshot:   botTestSnapshot(gamecomp.KindShip, 1),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(100, 0, []quantize.FullEntry{oldAuthority}, nil), math.MaxUint32, decoderState, decoders); !ok {
		t.Fatal("old stream setup frame decode failed")
	}

	newAuthority := oldAuthority
	newAuthority.Epoch = 8
	newAuthority.Snapshot = botTestSnapshot(gamecomp.KindShip, 2)
	if _, ok := decodeBinaryFrame(
		botTestFrame(1, quantize.FrameFlagFreshSnapshot, []quantize.FullEntry{newAuthority}, nil),
		0,
		decoderState,
		decoders,
	); !ok {
		t.Fatal("new stream fresh frame decode failed")
	}

	// A delayed frame from the old stream has a numerically later per-stream
	// sequence and FreshSnapshot set. It must be rejected before either the
	// baseline clear or the older entity epoch can run.
	oldAuthority.Snapshot = botTestSnapshot(gamecomp.KindShip, 99)
	state, ok := decodeBinaryFrame(
		botTestFrame(101, quantize.FrameFlagFreshSnapshot, []quantize.FullEntry{oldAuthority}, nil),
		math.MaxUint32,
		decoderState,
		decoders,
	)
	if !ok || !state.Discarded {
		t.Fatal("stale stream fresh frame was not cleanly discarded")
	}

	baseline := decoderState.baselines[netID]
	if baseline == nil {
		t.Fatal("stale fresh frame cleared the current baseline")
	}
	if got := binary.BigEndian.Uint32(baseline.snapshot[:4]); got != math.Float32bits(2) {
		t.Fatalf("stale fresh frame replaced baseline x bits: got %08x", got)
	}
	if got := decoderState.authorityEpochs[netID]; got != 8 {
		t.Fatalf("authority fence = %d, want 8", got)
	}
	if decoderState.streamEpoch != 0 || decoderState.lastFrameSeq != 1 {
		t.Fatalf("stream frontier = (%d, %d), want (0, 1)", decoderState.streamEpoch, decoderState.lastFrameSeq)
	}
}

func TestDecodeBinaryFrameRejectsSameStreamReorderingBeforeFreshMutation(t *testing.T) {
	const netID = uint32(10)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()
	entry := quantize.FullEntry{
		NetID:      netID,
		Epoch:      3,
		EntityType: gamecomp.KindShip,
		Snapshot:   botTestSnapshot(gamecomp.KindShip, 1),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(100, 0, []quantize.FullEntry{entry}, nil), 20, decoderState, decoders); !ok {
		t.Fatal("setup frame decode failed")
	}
	entry.Snapshot = botTestSnapshot(gamecomp.KindShip, 2)
	if _, ok := decodeBinaryFrame(botTestFrame(102, 0, []quantize.FullEntry{entry}, nil), 20, decoderState, decoders); !ok {
		t.Fatal("forward frame decode failed")
	}

	for _, sequence := range []uint32{102, 101, 102 + 0x80000000} {
		entry.Snapshot = botTestSnapshot(gamecomp.KindShip, 99)
		state, ok := decodeBinaryFrame(
			botTestFrame(sequence, quantize.FrameFlagFreshSnapshot, []quantize.FullEntry{entry}, nil),
			20,
			decoderState,
			decoders,
		)
		if !ok || !state.Discarded {
			t.Fatalf("same-stream sequence %d was not cleanly discarded", sequence)
		}
		baseline := decoderState.baselines[netID]
		if baseline == nil {
			t.Fatalf("sequence %d cleared the current baseline", sequence)
		}
		if got := binary.BigEndian.Uint32(baseline.snapshot[:4]); got != math.Float32bits(2) {
			t.Fatalf("sequence %d replaced baseline x bits: got %08x", sequence, got)
		}
	}

	// Rejected frames do not advance the frontier; the next ordered frame is
	// still accepted normally.
	entry.Snapshot = botTestSnapshot(gamecomp.KindShip, 3)
	if _, ok := decodeBinaryFrame(botTestFrame(103, 0, []quantize.FullEntry{entry}, nil), 20, decoderState, decoders); !ok {
		t.Fatal("ordered frame after reordering was rejected")
	}
}

func TestDecodeBinaryFrameAcceptsSameStreamSequenceWrap(t *testing.T) {
	const netID = uint32(14)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()
	entry := quantize.FullEntry{
		NetID:      netID,
		Epoch:      1,
		EntityType: gamecomp.KindShip,
		Snapshot:   botTestSnapshot(gamecomp.KindShip, 1),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(math.MaxUint32, 0, []quantize.FullEntry{entry}, nil), 30, decoderState, decoders); !ok {
		t.Fatal("pre-wrap frame decode failed")
	}
	entry.Snapshot = botTestSnapshot(gamecomp.KindShip, 2)
	state, ok := decodeBinaryFrame(botTestFrame(0, 0, []quantize.FullEntry{entry}, nil), 30, decoderState, decoders)
	if !ok {
		t.Fatal("wrapped frame sequence was rejected")
	}
	if got := state.Entities[netID]; got == nil || got.X != 2 {
		t.Fatalf("wrapped frame entity = %#v, want x=2", got)
	}
}

func TestDecodeBinaryFrameFullAuthorityEpochOrdering(t *testing.T) {
	const netID = uint32(11)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()

	initial := quantize.FullEntry{
		NetID:       netID,
		Epoch:       math.MaxUint32,
		EntityType:  gamecomp.KindShip,
		Snapshot:    botTestSnapshot(gamecomp.KindShip, 1),
		InitialData: botTestInitialString("pilot"),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(1, 0, []quantize.FullEntry{initial}, nil), 1, decoderState, decoders); !ok {
		t.Fatal("initial frame decode failed")
	}

	// Authority epochs wrap: zero is newer than MaxUint32. Initial-only data
	// must not leak across that authority boundary.
	newer := initial
	newer.Epoch = 0
	newer.Snapshot = botTestSnapshot(gamecomp.KindShip, 2)
	newer.InitialData = nil
	state, ok := decodeBinaryFrame(botTestFrame(2, 0, []quantize.FullEntry{newer}, nil), 1, decoderState, decoders)
	if !ok {
		t.Fatal("newer wrapped epoch frame decode failed")
	}
	if got := state.Entities[netID]; got == nil || got.X != 2 || got.PilotName != "" {
		t.Fatalf("newer entity = %#v, want x=2 with cleared pilot name", got)
	}
	if got := decoderState.baselines[netID].authorityEpoch; got != 0 {
		t.Fatalf("baseline epoch = %d, want 0", got)
	}

	older := initial
	older.Snapshot = botTestSnapshot(gamecomp.KindShip, 3)
	state, ok = decodeBinaryFrame(botTestFrame(3, 0, []quantize.FullEntry{older}, nil), 1, decoderState, decoders)
	if !ok {
		t.Fatal("older epoch frame decode failed")
	}
	if _, exists := state.Entities[netID]; exists {
		t.Fatal("older full snapshot was emitted")
	}
	if got := binary.BigEndian.Uint32(decoderState.baselines[netID].snapshot[:4]); got != math.Float32bits(2) {
		t.Fatalf("older full snapshot replaced baseline x bits: got %08x", got)
	}
}

func TestDecodeBinaryFrameFullPreservesInitialDataOnlyWithinScope(t *testing.T) {
	const netID = uint32(12)
	decoderState := newDeltaDecoderState()
	decoders := newDeltaDecoders()
	first := quantize.FullEntry{
		NetID:       netID,
		Epoch:       9,
		EntityType:  gamecomp.KindShip,
		Snapshot:    botTestSnapshot(gamecomp.KindShip, 1),
		InitialData: botTestInitialString("pilot"),
	}
	if _, ok := decodeBinaryFrame(botTestFrame(1, 0, []quantize.FullEntry{first}, nil), 1, decoderState, decoders); !ok {
		t.Fatal("initial frame decode failed")
	}

	keyframe := first
	keyframe.Snapshot = botTestSnapshot(gamecomp.KindShip, 2)
	keyframe.InitialData = nil
	state, ok := decodeBinaryFrame(botTestFrame(2, 0, []quantize.FullEntry{keyframe}, nil), 1, decoderState, decoders)
	if !ok {
		t.Fatal("same-scope keyframe decode failed")
	}
	if got := state.Entities[netID]; got == nil || got.PilotName != "pilot" {
		t.Fatalf("same-scope keyframe = %#v, want pilot name", got)
	}

	changedType := quantize.FullEntry{
		NetID:      netID,
		Epoch:      9,
		EntityType: gamecomp.KindNPC,
		Snapshot:   botTestSnapshot(gamecomp.KindNPC, 3),
	}
	state, ok = decodeBinaryFrame(botTestFrame(3, 0, []quantize.FullEntry{changedType}, nil), 1, decoderState, decoders)
	if !ok {
		t.Fatal("changed-type keyframe decode failed")
	}
	if got := state.Entities[netID]; got == nil || got.PilotName != "" {
		t.Fatalf("changed-type keyframe retained pilot name: %#v", got)
	}
}

func TestDecodeBinaryFrameDeltaRequiresMatchingEpochAndType(t *testing.T) {
	const netID = uint32(13)
	baseSnapshot := botTestSnapshot(gamecomp.KindShip, 1)
	nextSnapshot := botTestSnapshot(gamecomp.KindShip, 2)
	shipDelta := newDeltaDecoders().ship.Encode(baseSnapshot, nextSnapshot, nil)

	tests := []struct {
		name       string
		deltaEpoch uint32
		deltaType  uint8
	}{
		{name: "epoch", deltaEpoch: 8, deltaType: gamecomp.KindShip},
		{name: "type", deltaEpoch: 7, deltaType: gamecomp.KindNPC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoderState := newDeltaDecoderState()
			decoderState.baselines = map[uint32]*baselineEntry{
				netID: {
					snapshot:       append([]byte(nil), baseSnapshot...),
					authorityEpoch: 7,
					entityType:     gamecomp.KindShip,
					pilotName:      "pilot",
				},
			}
			decoderState.authorityEpochs[netID] = 7
			delta := quantize.DeltaEntry{
				NetID:      netID,
				Epoch:      tt.deltaEpoch,
				EntityType: tt.deltaType,
				Data:       shipDelta,
			}

			state, ok := decodeBinaryFrame(botTestFrame(2, 0, nil, []quantize.DeltaEntry{delta}), 1, decoderState, newDeltaDecoders())
			if !ok {
				t.Fatal("mismatched delta frame decode failed")
			}
			if _, exists := state.Entities[netID]; exists {
				t.Fatal("mismatched delta was emitted")
			}
			if got := binary.BigEndian.Uint32(decoderState.baselines[netID].snapshot[:4]); got != math.Float32bits(1) {
				t.Fatalf("mismatched delta mutated baseline x bits: got %08x", got)
			}
		})
	}
}

func botTestFrame(tick, flags uint32, full []quantize.FullEntry, deltas []quantize.DeltaEntry) []byte {
	return quantize.NewFrameEncoder(256).Encode(tick, tick, flags, full, deltas, nil, nil)
}

func botTestSnapshot(entityType uint8, x float32) []byte {
	size := 25
	switch entityType {
	case gamecomp.KindShip:
		size = 42
	case gamecomp.KindNPC:
		size = 33
	case gamecomp.KindAsteroid:
		size = 33
	}
	snapshot := make([]byte, size)
	binary.BigEndian.PutUint32(snapshot[:4], math.Float32bits(x))
	return snapshot
}

func botTestInitialString(value string) []byte {
	data := make([]byte, 2+len(value))
	binary.BigEndian.PutUint16(data, uint16(len(value)))
	copy(data[2:], value)
	return data
}
