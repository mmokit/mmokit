// Package udpproto implements the custom UDP game protocol encoding/decoding.
// Shared between server and Go client — no server-specific imports.
package udpproto

import (
	"encoding/binary"
	"errors"
)

// Packet types.
const (
	TypeUnreliable byte = 0x00
	TypeReliable   byte = 0x01
	TypeACK        byte = 0x02
	TypeConnReq    byte = 0x03
	TypeConnAccept byte = 0x04
	TypeDisconnect byte = 0x05
)

// ProtocolID is a magic number to reject stray UDP packets.
const ProtocolID uint32 = 0x47414D45 // "GAME"

// Header sizes (excluding type byte which is always first).
const (
	UnreliableHeaderSize = 1 + 4         // type + token
	ReliableHeaderSize   = 1 + 4 + 2     // type + token + seq
	ACKSize              = 1 + 4 + 2 + 4 // type + token + ack_seq + ack_bits
	ConnReqSize          = 1 + 4 + 8     // type + protocol_id + client_salt
	ConnAcceptSize       = 1 + 4 + 8 + 8 // type + protocol_id + client_salt + server_salt
	DisconnectSize       = 1 + 4         // type + token
)

var (
	ErrTooShort      = errors.New("packet too short")
	ErrBadType       = errors.New("unknown packet type")
	ErrBadProtocolID = errors.New("wrong protocol ID")
)

// MakeToken computes the connection token from client and server salts.
func MakeToken(clientSalt, serverSalt uint64) uint32 {
	combined := clientSalt ^ serverSalt
	return uint32(combined) ^ uint32(combined>>32)
}

// EncodeConnReq encodes a connection request packet.
func EncodeConnReq(clientSalt uint64) []byte {
	buf := make([]byte, ConnReqSize)
	buf[0] = TypeConnReq
	binary.LittleEndian.PutUint32(buf[1:5], ProtocolID)
	binary.LittleEndian.PutUint64(buf[5:13], clientSalt)
	return buf
}

// DecodeConnReq decodes a connection request. Returns clientSalt.
func DecodeConnReq(data []byte) (clientSalt uint64, err error) {
	if len(data) < ConnReqSize {
		return 0, ErrTooShort
	}
	pid := binary.LittleEndian.Uint32(data[1:5])
	if pid != ProtocolID {
		return 0, ErrBadProtocolID
	}
	clientSalt = binary.LittleEndian.Uint64(data[5:13])
	return clientSalt, nil
}

// EncodeConnAccept encodes a connection accepted response.
func EncodeConnAccept(clientSalt, serverSalt uint64) []byte {
	buf := make([]byte, ConnAcceptSize)
	buf[0] = TypeConnAccept
	binary.LittleEndian.PutUint32(buf[1:5], ProtocolID)
	binary.LittleEndian.PutUint64(buf[5:13], clientSalt)
	binary.LittleEndian.PutUint64(buf[13:21], serverSalt)
	return buf
}

// DecodeConnAccept decodes a connection accepted packet.
func DecodeConnAccept(data []byte) (clientSalt, serverSalt uint64, err error) {
	if len(data) < ConnAcceptSize {
		return 0, 0, ErrTooShort
	}
	pid := binary.LittleEndian.Uint32(data[1:5])
	if pid != ProtocolID {
		return 0, 0, ErrBadProtocolID
	}
	clientSalt = binary.LittleEndian.Uint64(data[5:13])
	serverSalt = binary.LittleEndian.Uint64(data[13:21])
	return clientSalt, serverSalt, nil
}

// EncodeUnreliable encodes an unreliable message.
func EncodeUnreliable(token uint32, payload []byte) []byte {
	buf := make([]byte, UnreliableHeaderSize+len(payload))
	buf[0] = TypeUnreliable
	binary.LittleEndian.PutUint32(buf[1:5], token)
	copy(buf[5:], payload)
	return buf
}

// DecodeUnreliable decodes an unreliable message. Returns token and payload.
func DecodeUnreliable(data []byte) (token uint32, payload []byte, err error) {
	if len(data) < UnreliableHeaderSize {
		return 0, nil, ErrTooShort
	}
	token = binary.LittleEndian.Uint32(data[1:5])
	payload = data[UnreliableHeaderSize:]
	return token, payload, nil
}

// EncodeReliable encodes a reliable message.
func EncodeReliable(token uint32, seq uint16, payload []byte) []byte {
	buf := make([]byte, ReliableHeaderSize+len(payload))
	buf[0] = TypeReliable
	binary.LittleEndian.PutUint32(buf[1:5], token)
	binary.LittleEndian.PutUint16(buf[5:7], seq)
	copy(buf[7:], payload)
	return buf
}

// DecodeReliable decodes a reliable message. Returns token, seq, payload.
func DecodeReliable(data []byte) (token uint32, seq uint16, payload []byte, err error) {
	if len(data) < ReliableHeaderSize {
		return 0, 0, nil, ErrTooShort
	}
	token = binary.LittleEndian.Uint32(data[1:5])
	seq = binary.LittleEndian.Uint16(data[5:7])
	payload = data[ReliableHeaderSize:]
	return token, seq, payload, nil
}

// EncodeACK encodes an ACK packet.
func EncodeACK(token uint32, ackSeq uint16, ackBits uint32) []byte {
	buf := make([]byte, ACKSize)
	buf[0] = TypeACK
	binary.LittleEndian.PutUint32(buf[1:5], token)
	binary.LittleEndian.PutUint16(buf[5:7], ackSeq)
	binary.LittleEndian.PutUint32(buf[7:11], ackBits)
	return buf
}

// DecodeACK decodes an ACK packet.
func DecodeACK(data []byte) (token uint32, ackSeq uint16, ackBits uint32, err error) {
	if len(data) < ACKSize {
		return 0, 0, 0, ErrTooShort
	}
	token = binary.LittleEndian.Uint32(data[1:5])
	ackSeq = binary.LittleEndian.Uint16(data[5:7])
	ackBits = binary.LittleEndian.Uint32(data[7:11])
	return token, ackSeq, ackBits, nil
}

// EncodeDisconnect encodes a disconnect packet.
func EncodeDisconnect(token uint32) []byte {
	buf := make([]byte, DisconnectSize)
	buf[0] = TypeDisconnect
	binary.LittleEndian.PutUint32(buf[1:5], token)
	return buf
}

// DecodeDisconnect decodes a disconnect packet.
func DecodeDisconnect(data []byte) (token uint32, err error) {
	if len(data) < DisconnectSize {
		return 0, ErrTooShort
	}
	token = binary.LittleEndian.Uint32(data[1:5])
	return token, nil
}

// SeqGreaterThan returns true if s1 > s2 accounting for 16-bit wrapping.
func SeqGreaterThan(s1, s2 uint16) bool {
	return (s1 > s2 && s1-s2 <= 32768) || (s1 < s2 && s2-s1 > 32768)
}
