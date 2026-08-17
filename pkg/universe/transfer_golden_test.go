package universe

import (
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/component"
)

// Byte-level pin for the cell-transfer frame.
//
// This codec is the one §7.5 phase 0 calls its worst offender: the fixed
// header size is a hand-summed constant (87) that appears in three places with
// offsets derived from it in two more. Nothing in the package checks that the
// sum matches the bytes actually written — every existing test round-trips,
// and a round trip is symmetric under exactly the edit a codec collapse makes.
//
// The frame crosses hosts, so a drift here does not fail locally: it fails
// when a mid-upgrade cluster hands an entity from a new host to an old one,
// and it fails as a corrupted entity rather than an error.
//
// The expectation is the current encoder's output, deliberately: the contract
// is "these bytes do not change". Regenerating it is a wire break and needs a
// lockstep cluster redeploy.
const goldenTransferHex = "ddccbbaa07000000030000000244332211" + // netID, epoch, streamGen, entityType, connID
	"0467772d31" + // gatewayID len=4 "gw-1"
	"88776655" + // gatewayConnID
	"05616c696365" + // username len=5 "alice"
	"11111111222233334444555555555555" + // userID (16 raw bytes)
	"0000c03f000010c0" + // posX=1.5 posY=-2.25
	"0000003f000040bf" + // velX=0.5 velY=-0.75
	"00004040" + // rotation=3
	"0000484100000000000000000000" + // collider: radius=12.5, w=0, h=0, layer=0, shape=0
	"ffffffff02000000" + // cellX=-1 cellY=2
	"0f000000" + // debugFlags
	"0200" + // component count = 2
	"02010400deadbeef" + // component id=0x0102 (LE) len=4 (LE) dead beef
	"04030000" // component id=0x0304 (LE) len=0 (nil Data encodes as empty)

func goldenTransferFrame() *TransferFrame {
	return &TransferFrame{
		NetworkID: 0xAABBCCDD, Epoch: 7, StreamGeneration: 3,
		EntityType: 2, ConnID: 0x11223344,
		GatewayID: "gw-1", GatewayConnID: 0x55667788,
		Username: "alice",
		UserID:   uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		PosX:     1.5, PosY: -2.25, VelX: 0.5, VelY: -0.75, Rotation: 3.0,
		Collider: component.Collider{Radius: 12.5},
		// Negative CellX on purpose: the cell coordinates are int32 and a
		// sign-handling change would be invisible in a positive-only fixture.
		CellX: -1, CellY: 2, DebugFlags: 0x0F,
		Components: []ComponentSlice{
			{ID: 0x0102, Data: []byte{0xDE, 0xAD, 0xBE, 0xEF}},
			// nil Data, because "nil encodes as empty" is a property a rewrite
			// could plausibly break into a nil-length-prefix panic.
			{ID: 0x0304, Data: nil},
		},
	}
}

func TestMarshalTransferFrame_MatchesGoldenBytes(t *testing.T) {
	got, err := MarshalTransferFrame(goldenTransferFrame())
	if err != nil {
		t.Fatalf("MarshalTransferFrame: %v", err)
	}
	if h := hex.EncodeToString(got); h != goldenTransferHex {
		t.Fatalf("transfer frame encoding changed — this is a WIRE BREAK across hosts.\n got  %s\n want %s", h, goldenTransferHex)
	}
}

// The hand-summed header constant, checked against the bytes rather than
// against itself. Variable parts of the golden: gatewayID(4) + username(5) +
// two components (2+2+4 and 2+2+0). Everything else is the fixed header.
//
// This is the assertion the duplicated constant has never had. If someone adds
// a field and updates two of the three copies of the sum, this fails; today
// nothing would.
func TestTransferFrame_FixedHeaderSizeIsStill87(t *testing.T) {
	const wantHeader = 87
	total := len(goldenTransferHex) / 2
	variable := len("gw-1") + len("alice") + (2 + 2 + 4) + (2 + 2 + 0)
	if got := total - variable; got != wantHeader {
		t.Fatalf("fixed header size derived from the golden = %d, want %d — "+
			"the hand-summed constant in MarshalTransferFrame no longer matches the bytes it writes", got, wantHeader)
	}
}

// Decode pinned independently of encode: rewriting only one side is the
// failure a round-trip test cannot see.
func TestUnmarshalTransferFrame_GoldenBytesReproduceSource(t *testing.T) {
	raw, err := hex.DecodeString(goldenTransferHex)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	got, err := UnmarshalTransferFrame(raw)
	if err != nil {
		t.Fatalf("UnmarshalTransferFrame(golden): %v", err)
	}
	want := goldenTransferFrame()

	if got.NetworkID != want.NetworkID || got.Epoch != want.Epoch ||
		got.StreamGeneration != want.StreamGeneration || got.EntityType != want.EntityType ||
		got.ConnID != want.ConnID || got.GatewayID != want.GatewayID ||
		got.GatewayConnID != want.GatewayConnID || got.Username != want.Username ||
		got.UserID != want.UserID {
		t.Fatalf("identity fields = %+v, want %+v", got, want)
	}
	if got.PosX != want.PosX || got.PosY != want.PosY ||
		got.VelX != want.VelX || got.VelY != want.VelY || got.Rotation != want.Rotation {
		t.Fatalf("motion = %+v, want %+v", got, want)
	}
	if got.Collider != want.Collider {
		t.Fatalf("collider = %+v, want %+v", got.Collider, want.Collider)
	}
	if got.CellX != want.CellX || got.CellY != want.CellY || got.DebugFlags != want.DebugFlags {
		t.Fatalf("cell/debug = (%d,%d,%#x), want (%d,%d,%#x)",
			got.CellX, got.CellY, got.DebugFlags, want.CellX, want.CellY, want.DebugFlags)
	}
	if len(got.Components) != len(want.Components) {
		t.Fatalf("decoded %d components, want %d", len(got.Components), len(want.Components))
	}
	for i := range want.Components {
		if got.Components[i].ID != want.Components[i].ID {
			t.Fatalf("component %d id = %#x, want %#x", i, got.Components[i].ID, want.Components[i].ID)
		}
		if len(got.Components[i].Data) != len(want.Components[i].Data) {
			t.Fatalf("component %d data len = %d, want %d",
				i, len(got.Components[i].Data), len(want.Components[i].Data))
		}
	}
}
