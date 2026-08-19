package universe

import (
	"encoding/hex"
	"testing"
)

// Byte-level pins for the two client channel framings.
//
// These are the outermost layer a client sees: every typed event and every
// typed operation is wrapped in one of them, on both transports. Until now
// they were covered only by round trips — encode, decode, compare — which pass
// unchanged when encoder and decoder are altered together. That is exactly what
// a codec refactor does, so those tests cannot detect the failure they look
// like they are guarding against. The party that notices is a peer running the
// other build.
//
// The expectations below are hand-derived from the layout, not captured from a
// run, so a failure means the bytes moved rather than that someone re-recorded
// them. Regenerating one is a wire break: it invalidates every generated SDK
// and rotates the CE-009 schema fingerprint's transport assumptions.
//
// The border-snapshot body is deliberately absent: it is
// replication.Frame.Encode(), already pinned by
// pkg/replication/frame_golden_test.go, and the border path sends exactly that.

// Single typed event, little-endian throughout:
//
//	[0x00]                channel byte (pkgnet.ChannelEvent)
//	ddccbbaa       u32    typeID     0xaabbccdd
//	03000000       u32    body_len   3
//	deadbe                body
const goldenTypedEventHex = "00ddccbbaa03000000deadbe"

// Empty body is a distinct case: body_len 0 with no trailing bytes. A codec
// that conflated "no body" with "no length prefix" would still round-trip.
//
//	[0x00] 09000000 (typeID 9) 00000000 (len 0)
const goldenTypedEventEmptyHex = "000900000000000000"

// Batched typed events share ONE channel byte, then repeat [typeID][len][body].
// The second entry carries an empty body so the pin covers a zero-length entry
// in the middle of a stream, where a length mistake shifts everything after it.
//
//	[0x00]
//	01000000 01000000 aa      typeID 1, len 1, body aa
//	02000000 00000000         typeID 2, len 0
const goldenTypedEventBatchHex = "000100000001000000aa0200000000000000"

// Typed operation, little-endian throughout:
//
//	[0x01]                channel byte (pkgnet.ChannelOperation)
//	44332211       u32    typeID      0x11223344
//	0807060504030201 u64  request_id  0x0102030405060708
//	02000000       u32    body_len    2
//	feed                  body
const goldenTypedOpHex = "0144332211080706050403020102000000feed"

func TestTypedEventFrame_EncodeMatchesGoldenBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []byte
		want string
	}{
		{"single", EncodeTypedEventFrame(0xAABBCCDD, []byte{0xDE, 0xAD, 0xBE}), goldenTypedEventHex},
		{"empty body", EncodeTypedEventFrame(9, nil), goldenTypedEventEmptyHex},
		{"batched", EncodeBatchedTypedEventFrame([]BroadcastEvent{
			{TypeID: 1, Body: []byte{0xAA}},
			{TypeID: 2, Body: nil},
		}), goldenTypedEventBatchHex},
	} {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("%s: typed-event framing changed — this is a WIRE BREAK, not a test to re-record.\n got  %s\n want %s",
				c.name, got, c.want)
		}
	}
}

// An empty batch must produce nil, not a bare channel byte. A one-byte frame
// would reach the client as a zero-event batch and cost a wakeup per tick per
// viewer; the send paths check for nil to skip the write entirely.
func TestTypedEventFrame_EmptyBatchIsNil(t *testing.T) {
	if got := EncodeBatchedTypedEventFrame(nil); got != nil {
		t.Errorf("empty batch encoded to %x, want nil so the caller skips the write", got)
	}
}

func TestTypedOpFrame_EncodeMatchesGoldenBytes(t *testing.T) {
	got := hex.EncodeToString(EncodeTypedOpFrame(0x11223344, 0x0102030405060708, []byte{0xFE, 0xED}))
	if got != goldenTypedOpHex {
		t.Fatalf("typed-op framing changed — this is a WIRE BREAK, not a test to re-record.\n got  %s\n want %s",
			got, goldenTypedOpHex)
	}
}

// Decode is asserted against the same hex INDEPENDENTLY of encode, so a
// symmetric change to both does not slip through. This is the property a round
// trip cannot give.
func TestTypedOpFrame_DecodeMatchesGoldenBytes(t *testing.T) {
	raw, err := hex.DecodeString(goldenTypedOpHex)
	if err != nil {
		t.Fatalf("bad golden hex: %v", err)
	}
	// The decoder takes the payload with the channel byte already stripped,
	// which is how every drain path calls it.
	typeID, requestID, body, err := DecodeTypedOpFrame(raw[1:])
	if err != nil {
		t.Fatalf("DecodeTypedOpFrame: %v", err)
	}
	if typeID != 0x11223344 {
		t.Errorf("typeID = %#x, want 0x11223344", typeID)
	}
	if requestID != 0x0102030405060708 {
		t.Errorf("requestID = %#x, want 0x0102030405060708", requestID)
	}
	if got := hex.EncodeToString(body); got != "feed" {
		t.Errorf("body = %s, want feed", got)
	}
}
