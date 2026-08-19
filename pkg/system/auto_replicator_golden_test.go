package system

import (
	"encoding/hex"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// Byte-level pin for the binding walker's REAL output.
//
// This package had no golden at all, and the nearest thing — TestAutoReplicatorSnapshot —
// builds its expectation with a second SnapshotWriter making the same calls in
// the same order. That passes unchanged when the encoder changes, because both
// sides move together. It is a round trip wearing a golden's clothes.
//
// Every snapshot payload in the cross-language manifest is a literal too
// (cmd/csharp-golden uses {0x01,0x02,0x03,0x04}), so nothing anywhere pinned
// what auto_replicator actually emits from real bindings. That is the gap this
// closes, and it is what grades the encoding-table work in phase 0 step 6 and
// the bot's offset tables in step 7.
//
// Big-endian throughout, which is the snapshot convention (the UDP packet
// codec is little-endian — different layers, do not conflate).

// The fixture includes QAngle deliberately. The golden had ZERO rotation
// coverage, so it could not grade the one binding the quaternion swap edits —
// and that swap is the only step in phase 1 with a behavioural risk. Pinned
// here, while the storage is still a scalar and the bytes are provably
// unchanged, so the swap is measured against a fixed reference.

// goldenScalars exercises every fixed-width encoding a production tag uses.
type goldenScalars struct {
	F32   float32 `net:"f32"`
	U8    uint8   `net:"u8"`
	U16   uint16  `net:"u16"`
	U32   uint32  `net:"u32"`
	I16   int16   `net:"i16"`
	Flag  bool    `net:"bool"`
	QNorm float32 `net:"qnorm"`
}

// goldenInitial covers the initial-payload path: a length-prefixed string and
// a scalar, which travel in a separate buffer sent once on entry.
type goldenInitial struct {
	Label string `net:"initial"`
	Tier  uint8  `net:"initial,u8"`
}

// Snapshot stream, field by field. Values chosen so each is checkable by eye.
//
//	41280000  f32    10.5        EntryPosition X
//	c1a20000  f32   -20.25       EntryPosition Y
//	0028      qvel   1.25        QVelocity X   1.25/1000*32767 =  40
//	ffaf      qvel  -2.5         QVelocity Y  -2.5/1000*32767 = -81
//	020c      qsize  8           QSize radius     8/500*32767 = 524
//	0000      qsize  0           QSize width  (unset on the Collider)
//	0000      qsize  0           QSize height (unset)
//	945e      qangle 0.5         QAngle: (0.5+pi)/(2pi)*65535 = 37982
//	3fc00000  f32    1.5         goldenScalars.F32
//	07        u8     7
//	0258      u16    600
//	00011170  u32    70000
//	fed4      i16   -300
//	01        bool   true
//	7f        qnorm  0.5         0.5*255 = 127
const goldenSnapshotHex = "41280000c1a200000028ffaf020c00000000945e3fc0000007025800011170fed4017f"

// Initial payload: u8 length prefix, UTF-8 bytes, then the u8 field.
//
//	04        len 4
//	676f6c64  "gold"
//	03        Tier 3
const goldenInitialHex = "04676f6c6403"

// The layout the delta encoder prefix-sums into its offset table. A width here
// that disagrees with the bytes above shifts every field after it — which is
// exactly the defect `net:"pos"` was (declared 8, wrote 4).
var goldenLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 2, 4, 2, 1, 1}

func goldenReplicator(t *testing.T) (EntityReplicator, spatial.Entry, *ViewerInfo) {
	t.Helper()
	world := ecs.NewWorld()
	scalars := ecs.NewMap1[goldenScalars](world)
	initial := ecs.NewMap1[goldenInitial](world)
	vel := ecs.NewMap1[component.Velocity](world)
	col := ecs.NewMap1[component.Collider](world)
	rot := ecs.NewMap1[component.Rotation](world)

	e := scalars.NewEntity(&goldenScalars{
		F32: 1.5, U8: 7, U16: 600, U32: 70000, I16: -300, Flag: true, QNorm: 0.5,
	})
	initial.Add(e, &goldenInitial{Label: "gold", Tier: 3})
	vel.Add(e, &component.Velocity{X: 1.25, Y: -2.5})
	col.Add(e, &component.Collider{Radius: 8})
	rotv := component.RotationFromYaw(0.5)
	rot.Add(e, &rotv)

	rep := AutoReplicator(42,
		EntryPosition(),
		QVelocity(vel, 1000),
		QSize(col, 500),
		QAngle(rot),
		Component(scalars),
		Component(initial),
	)
	return rep, spatial.Entry{Entity: e, X: 10.5, Y: -20.25}, &ViewerInfo{ConnID: 1}
}

func TestAutoReplicator_SnapshotMatchesGoldenBytes(t *testing.T) {
	rep, entry, viewer := goldenReplicator(t)

	w := quantize.NewSnapshotWriter(make([]byte, 512))
	rep.Snapshot(w, viewer, entry)

	if got := hex.EncodeToString(w.Bytes()); got != goldenSnapshotHex {
		t.Fatalf("binding-walker snapshot bytes changed — this is a WIRE BREAK.\n got  %s\n want %s",
			got, goldenSnapshotHex)
	}
}

func TestAutoReplicator_InitialDataMatchesGoldenBytes(t *testing.T) {
	rep, entry, viewer := goldenReplicator(t)

	if got := hex.EncodeToString(rep.InitialData(viewer, entry)); got != goldenInitialHex {
		t.Fatalf("initial-payload bytes changed — this is a WIRE BREAK.\n got  %s\n want %s",
			got, goldenInitialHex)
	}
}

// The layout must agree with the snapshot bytes: summing it gives the payload
// length. Asserting both separately is what catches a width that drifts from
// what its writer emits, since the delta encoder trusts the layout and never
// re-measures the bytes.
func TestAutoReplicator_LayoutMatchesGoldenBytes(t *testing.T) {
	rep, _, _ := goldenReplicator(t)

	layout := rep.SnapshotLayout()
	if len(layout) != len(goldenLayout) {
		t.Fatalf("layout has %d fields, want %d:\n got  %v\n want %v",
			len(layout), len(goldenLayout), layout, goldenLayout)
	}
	total := 0
	for i, w := range layout {
		if w != goldenLayout[i] {
			t.Errorf("layout[%d] = %d, want %d", i, w, goldenLayout[i])
		}
		total += w
	}

	snapBytes := len(goldenSnapshotHex) / 2
	if total != snapBytes {
		t.Errorf("layout sums to %d bytes but the snapshot golden is %d — "+
			"the delta encoder slices by this table and never re-measures the payload",
			total, snapBytes)
	}
}
