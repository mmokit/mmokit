package universe

import (
	"testing"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
)

// TestReplicationReceiptFrameRoundTrip pins the return leg's typed fields.
//
// The predecessor of this test exercised a string marker parser —
// "@mmokit/repl-receipt/v2/<len>/<host>/<token>" packed into DestCellId — and
// most of its cases (empty host, bad length prefix, truncated host,
// non-numeric token, wrong marker version) tested only that hand-rolled
// parser. Those failure modes no longer exist: the fields are typed, so the
// only thing left to validate is the two enums, below.
func TestReplicationReceiptFrameRoundTrip(t *testing.T) {
	want := pkgnet.SendResult{
		Disposition: pkgnet.SendQueued,
		Delivery:    pkgnet.DeliveryReliableOrdered,
	}
	frame := newReplicationReceiptFrame("host-1", "gw-1", 42, 7, 991, want)

	rr := frame.GetReplicationReceipt()
	if rr == nil {
		t.Fatal("receipt did not land on its own oneof arm")
	}
	if rr.GetSourceHostId() != "host-1" {
		t.Fatalf("source host = %q, want host-1", rr.GetSourceHostId())
	}
	if rr.GetReceiptToken() != 991 {
		t.Fatalf("token = %d, want 991", rr.GetReceiptToken())
	}
	if rr.GetGatewayId() != "gw-1" || rr.GetConnId() != 42 || rr.GetEpoch() != 7 {
		t.Fatalf("route fields = %+v", rr)
	}

	got, ok := receiptResult(rr)
	if !ok {
		t.Fatal("receiptResult rejected a valid receipt")
	}
	if got.Disposition != want.Disposition || got.Delivery != want.Delivery {
		t.Fatalf("result = %+v, want %+v", got, want)
	}

	// A receipt must no longer occupy the ClientInput arm, or it would reach
	// game input decoding.
	if frame.GetClientInput() != nil {
		t.Fatal("receipt still rides the ClientInput arm")
	}
	if frame.GetDestCellId() != "" {
		t.Fatalf("receipt still overloads DestCellId: %q", frame.GetDestCellId())
	}
}

// TestReplicationReceiptRejectsOutOfRangeEnums is what survives of the
// malformed-frame table. Both values are peer-supplied uint32s, so casting
// them blindly into typed enums would let a peer inject a nonsense delivery
// class into the connection-mode latch.
func TestReplicationReceiptRejectsOutOfRangeEnums(t *testing.T) {
	tests := []struct {
		name                  string
		disposition, delivery uint32
		wantOK                bool
	}{
		{"valid", uint32(pkgnet.SendQueued), uint32(pkgnet.DeliveryReliableOrdered), true},
		{"disposition out of range", 250, uint32(pkgnet.DeliveryReliableOrdered), false},
		{"delivery out of range", uint32(pkgnet.SendQueued), 250, false},
		{"both out of range", 99, 99, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := &meshpb.ReplicationReceipt{
				SourceHostId: "host-1",
				GatewayId:    "gw-1",
				ConnId:       1,
				Epoch:        1,
				ReceiptToken: 1,
				Disposition:  tt.disposition,
				Delivery:     tt.delivery,
			}
			if _, ok := receiptResult(rr); ok != tt.wantOK {
				t.Fatalf("receiptResult ok = %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

// TestTrackedClientFrame_CarriesTypedReceiptFields pins the outbound leg.
func TestTrackedClientFrame_CarriesTypedReceiptFields(t *testing.T) {
	frame := trackedClientFrame("host-1", "gw-1", 42, 7, 991, []byte{0x01})

	cf := frame.GetClientFrame()
	if cf == nil {
		t.Fatal("tracked frame is not a ClientFrame")
	}
	if cf.GetSourceHostId() != "host-1" || cf.GetReceiptToken() != 991 {
		t.Fatalf("receipt fields = (%q, %d), want (host-1, 991)", cf.GetSourceHostId(), cf.GetReceiptToken())
	}
	if frame.GetDestCellId() != "" {
		t.Fatalf("tracked frame still overloads DestCellId: %q", frame.GetDestCellId())
	}

	// An untracked frame must be distinguishable by the token alone.
	plain := trackedClientFrame("", "gw-1", 42, 7, 0, []byte{0x01})
	if plain.GetClientFrame().GetReceiptToken() != 0 {
		t.Fatal("an untracked frame carries a receipt token")
	}
}
