package mmokit

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/mmokit/mmokit/pkg/system"
)

func TestMakeWorldDeltaFrameCarriesStreamEpochOutsideDeltaBody(t *testing.T) {
	body := []byte{0x10, 0x20, 0x30}
	const wantEpoch = uint32(41)
	wire := makeWorldDeltaFrame(&system.ReplicationFrame{StreamEpoch: wantEpoch}, body)

	if len(wire) < 13+len(body)+4 {
		t.Fatalf("wire length = %d, want at least %d", len(wire), 13+len(body)+4)
	}
	payloadLen := int(binary.LittleEndian.Uint32(wire[5:9]))
	if payloadLen != 4+len(body)+4 {
		t.Fatalf("payload length = %d, want %d", payloadLen, 4+len(body)+4)
	}
	bodyLen := int(binary.LittleEndian.Uint32(wire[9:13]))
	if bodyLen != len(body) || !bytes.Equal(wire[13:13+bodyLen], body) {
		t.Fatalf("encoded delta body = %v, want %v", wire[13:13+bodyLen], body)
	}
	if got := binary.LittleEndian.Uint32(wire[13+bodyLen:]); got != wantEpoch {
		t.Fatalf("stream epoch = %d, want %d", got, wantEpoch)
	}
}
