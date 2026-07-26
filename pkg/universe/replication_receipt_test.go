package universe

import (
	"testing"

	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

func TestReplicationReceiptFrameRoundTrip(t *testing.T) {
	want := pkgnet.SendResult{
		Disposition: pkgnet.SendQueued,
		Delivery:    pkgnet.DeliveryReliableOrdered,
	}
	frame := newReplicationReceiptFrame("host-1", "gw-1", 42, 7, 991, want)

	hostID, token, got, ok := decodeReplicationReceiptFrame(frame)
	if !ok {
		t.Fatal("decodeReplicationReceiptFrame rejected valid receipt")
	}
	if hostID != "host-1" {
		t.Fatalf("hostID = %q, want host-1", hostID)
	}
	if token != 991 {
		t.Fatalf("token = %d, want 991", token)
	}
	if got.Disposition != want.Disposition || got.Delivery != want.Delivery {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
	ci := frame.GetClientInput()
	if ci.GatewayId != "gw-1" || ci.ConnId != 42 || ci.Epoch != 7 {
		t.Fatalf("receipt route fields = %+v", ci)
	}
}

func TestReplicationReceiptFrameRejectsMalformedReservedFrames(t *testing.T) {
	result := pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
	tests := []struct {
		name   string
		marker string
		data   []byte
	}{
		{name: "empty host", marker: replicationReceiptMarkerPrefix + "0//1", data: encodeReplicationReceiptResult(result)},
		{name: "invalid host length", marker: replicationReceiptMarkerPrefix + "!/host-1/1", data: encodeReplicationReceiptResult(result)},
		{name: "truncated host", marker: replicationReceiptMarkerPrefix + "9/host-1/1", data: encodeReplicationReceiptResult(result)},
		{name: "zero token", marker: replicationReceiptMarker("host-1", 0), data: encodeReplicationReceiptResult(result)},
		{name: "non-numeric token", marker: replicationReceiptMarkerPrefix + "6/host-1/oops", data: encodeReplicationReceiptResult(result)},
		{name: "unsupported marker version", marker: replicationReceiptNamespacePrefix + "v1/1", data: encodeReplicationReceiptResult(result)},
		{name: "unknown payload version", marker: replicationReceiptMarker("host-1", 1), data: []byte{99, byte(pkgnet.SendQueued), byte(pkgnet.DeliveryReliableOrdered)}},
		{name: "trailing payload", marker: replicationReceiptMarker("host-1", 1), data: append(encodeReplicationReceiptResult(result), 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := newReplicationReceiptFrame("host-1", "gw-1", 1, 1, 1, result)
			frame.DestCellId = tt.marker
			frame.GetClientInput().Data = tt.data
			if _, _, _, ok := decodeReplicationReceiptFrame(frame); ok {
				t.Fatal("malformed receipt decoded successfully")
			}
		})
	}
}
