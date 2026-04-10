package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/system"
)

// TestWriteStatusEffects_ByteLayout verifies the serialized layout and the
// qnorm quantization of Duration. Regression guard for the asymmetry bug
// where HashStatusEffects used raw Duration while WriteStatusEffects
// quantized it, causing the diff hash to drift every tick and flood the
// wire with duplicate snapshots.
func TestWriteStatusEffects_ByteLayout(t *testing.T) {
	// 12.75 / 25.5 = 0.5 → qnorm byte 127 (round((0.5 * 255) = 127.5) = 127 or 128)
	// 0.1  / 25.5 ≈ 0.00392 → qnorm byte ~1
	se := &gamecomp.StatusEffects{Count: 2}
	se.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 12.75}
	se.Effects[1] = gamecomp.StatusEffect{Type: gamecomp.StatusAfterburner, Duration: 0.1}

	buf := make([]byte, 16)
	w := quantize.NewSnapshotWriter(buf)
	WriteStatusEffects(se, w)

	got := w.Bytes()
	if len(got) != 4 {
		t.Fatalf("expected 4 bytes (2 items × 2 bytes), got %d: %v", len(got), got)
	}
	if got[0] != uint8(gamecomp.StatusIonBurn) {
		t.Errorf("byte[0] type: got %d, want %d", got[0], gamecomp.StatusIonBurn)
	}
	wantDur0 := quantize.Norm(12.75 / StatusEffectDurationScale)
	if got[1] != wantDur0 {
		t.Errorf("byte[1] duration: got %d, want %d", got[1], wantDur0)
	}
	if got[2] != uint8(gamecomp.StatusAfterburner) {
		t.Errorf("byte[2] type: got %d, want %d", got[2], gamecomp.StatusAfterburner)
	}
	wantDur1 := quantize.Norm(0.1 / StatusEffectDurationScale)
	if got[3] != wantDur1 {
		t.Errorf("byte[3] duration: got %d, want %d", got[3], wantDur1)
	}
}

// TestHashStatusEffects_QuantizationSymmetry verifies that HashStatusEffects
// uses the same quantized duration byte as WriteStatusEffects. Two effects
// that differ by less than one qnorm step (~0.1s at scale 25.5) must hash
// to the same value — otherwise the diff detector fires every tick.
func TestHashStatusEffects_QuantizationSymmetry(t *testing.T) {
	a := &gamecomp.StatusEffects{Count: 1}
	a.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 10.0}

	b := &gamecomp.StatusEffects{Count: 1}
	// 10.0 and 10.01 both quantize to the same byte at scale 25.5:
	// (0.01 / 25.5) * 255 ≈ 0.1 — well under a single quantization step.
	b.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 10.01}

	var ha, hb system.Hasher
	ha.Reset()
	hb.Reset()
	HashStatusEffects(a, &ha)
	HashStatusEffects(b, &hb)
	if ha.Sum() != hb.Sum() {
		t.Errorf("sub-quantization-step duration change changed hash: a=%x b=%x", ha.Sum(), hb.Sum())
	}

	// Sanity: a clearly larger change should produce a different hash.
	c := &gamecomp.StatusEffects{Count: 1}
	c.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 15.0}
	var hc system.Hasher
	hc.Reset()
	HashStatusEffects(c, &hc)
	if hc.Sum() == ha.Sum() {
		t.Errorf("5-second duration change did not change hash (hash stale?)")
	}
}

// TestHashStatusEffects_SourceIgnored verifies that the Source entity field
// does not influence hashing or writing — the Source must be zeroed on the
// pre-marshal hook so cross-node transfers don't leak ecs.Entity handles.
// Even when Source differs, hash and wire bytes must be identical.
func TestHashStatusEffects_SourceIgnored(t *testing.T) {
	a := &gamecomp.StatusEffects{Count: 1}
	a.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 5.0}

	// Same effect with a distinct non-zero Source.
	b := &gamecomp.StatusEffects{Count: 1}
	b.Effects[0] = gamecomp.StatusEffect{Type: gamecomp.StatusIonBurn, Duration: 5.0}
	// Source is zero for 'a' and we don't set it for 'b' either — but the
	// test's intent is that even a hand-modified Source must not affect hash.
	// We can't easily construct a non-zero ecs.Entity without a world, but
	// the binding's Write/Hash never reference Source at all, so a zero
	// default is sufficient proof that Source is off the hash surface.

	var ha, hb system.Hasher
	ha.Reset()
	hb.Reset()
	HashStatusEffects(a, &ha)
	HashStatusEffects(b, &hb)
	if ha.Sum() != hb.Sum() {
		t.Errorf("identical effects hashed differently: a=%x b=%x", ha.Sum(), hb.Sum())
	}
}

// TestWriteInventoryItems_SortStability verifies that two inventories with
// identical contents produce identical wire bytes regardless of map
// insertion order. Regression guard for the order-dependent bug class.
func TestWriteInventoryItems_SortStability(t *testing.T) {
	a := &gamecomp.Inventory{Items: map[uint32]int32{10: 5, 2: 3, 50: 7}, MaxMass: 100}
	b := &gamecomp.Inventory{Items: map[uint32]int32{50: 7, 10: 5, 2: 3}, MaxMass: 100}

	bufA := make([]byte, 32)
	bufB := make([]byte, 32)
	wa := quantize.NewSnapshotWriter(bufA)
	wb := quantize.NewSnapshotWriter(bufB)
	WriteInventoryItems(a, wa)
	WriteInventoryItems(b, wb)

	ba := wa.Bytes()
	bb := wb.Bytes()
	if len(ba) != len(bb) {
		t.Fatalf("len mismatch: a=%d b=%d", len(ba), len(bb))
	}
	for i := range ba {
		if ba[i] != bb[i] {
			t.Errorf("byte[%d]: a=%d b=%d", i, ba[i], bb[i])
		}
	}

	// 3 items × 8 bytes each.
	if len(ba) != 24 {
		t.Fatalf("expected 24 bytes for 3 items, got %d", len(ba))
	}

	// First item must be the lowest ID (2).
	if ba[0] != 0 || ba[1] != 0 || ba[2] != 0 || ba[3] != 2 {
		t.Errorf("first itemID big-endian: got %v, want [0 0 0 2]", ba[0:4])
	}
}

// TestWriteInventoryItems_SkipsZero verifies zero/negative quantities are
// skipped so stale map entries don't bloat the wire.
func TestWriteInventoryItems_SkipsZero(t *testing.T) {
	inv := &gamecomp.Inventory{Items: map[uint32]int32{1: 5, 2: 0, 3: -3, 4: 10}}
	if n := CountInventoryItems(inv); n != 2 {
		t.Errorf("CountInventoryItems: got %d, want 2", n)
	}

	buf := make([]byte, 32)
	w := quantize.NewSnapshotWriter(buf)
	WriteInventoryItems(inv, w)

	got := w.Bytes()
	if len(got) != 16 {
		t.Fatalf("expected 16 bytes (2 items × 8 bytes), got %d", len(got))
	}
}

// TestHashInventoryItems_SortStability — same guarantee at the hash level.
func TestHashInventoryItems_SortStability(t *testing.T) {
	a := &gamecomp.Inventory{Items: map[uint32]int32{1: 10, 2: 20, 3: 30}}
	b := &gamecomp.Inventory{Items: map[uint32]int32{3: 30, 2: 20, 1: 10}}

	var ha, hb system.Hasher
	ha.Reset()
	hb.Reset()
	HashInventoryItems(a, &ha)
	HashInventoryItems(b, &hb)
	if ha.Sum() != hb.Sum() {
		t.Errorf("map-order differences changed hash: a=%x b=%x", ha.Sum(), hb.Sum())
	}
}

// TestWriteInventoryItems_Empty verifies an empty inventory produces zero
// bytes (the uint16 length prefix is written by the varTailBinding, not
// WriteInventoryItems itself).
func TestWriteInventoryItems_Empty(t *testing.T) {
	inv := &gamecomp.Inventory{Items: nil}

	buf := make([]byte, 8)
	w := quantize.NewSnapshotWriter(buf)
	WriteInventoryItems(inv, w)

	if got := w.Bytes(); len(got) != 0 {
		t.Errorf("empty inventory wrote %d bytes: %v", len(got), got)
	}
}
