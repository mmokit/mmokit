// Package udpproto implements the custom UDP game protocol encoding/decoding.
// Shared between server and Go client — no server-specific imports.
//
// # Version 2 (CE-005b Tier 2 + CE-009)
//
// v1 was plaintext with a 32-bit token as its only credential. Tier 1 bound
// that token to a source address, which stopped replay from elsewhere but left
// every byte readable and forgeable by anyone on path.
//
// v2 changes three things in ONE wire break — deliberately bundled so there is
// one lockstep redeploy and one golden regeneration rather than three:
//
//  1. A version byte follows the type byte on every packet (CE-009). Without it
//     a client and server that disagree about the shape of the world decode
//     each other's valid bytes into the wrong types instead of disconnecting.
//  2. Data packets carry an explicit AEAD counter and a sealed body. The token
//     stops being a credential and becomes a session index; the tag is the
//     credential.
//  3. The handshake gains a stateless cookie and a ConnConfirm step, so the
//     server holds no state for peers that have not proven return routability,
//     and the client proves possession of an HTTPS-issued key before any
//     session exists.
//
// Header fields stay cleartext because the server must read them to find the
// session before it can decrypt. They are all fed to the AEAD as additional
// authenticated data, so they cannot be altered without breaking the tag.
//
// This package deliberately has no crypto dependency: encoders emit a cleartext
// header and decoders return the header slice to use as AAD plus the sealed
// body. Nonce ownership stays in exactly one place, udpcrypto.Session.
package udpproto

import (
	"encoding/binary"
	"errors"
)

// Packet types.
const (
	TypeUnreliable  byte = 0x00
	TypeReliable    byte = 0x01
	TypeACK         byte = 0x02
	TypeConnReq     byte = 0x03
	TypeConnAccept  byte = 0x04
	TypeDisconnect  byte = 0x05
	TypeConnConfirm byte = 0x06
)

// Version is the protocol version carried by every packet (CE-009). Bump it for
// any layout change or any change to the meaning of a sealed body.
const Version byte = 0x02

// ProtocolID is a magic number to reject stray UDP packets.
const ProtocolID uint32 = 0x47414D45 // "GAME"

// Wire sizes. Every header size counts the type and version bytes.
const (
	// HeaderPrefixSize is type + version, present on every packet.
	HeaderPrefixSize = 2

	// CounterSize is the explicit AEAD packet counter. Explicit because the
	// unreliable channel reorders and drops, so a receiver-side implicit
	// counter would desynchronise on the first loss and never recover.
	CounterSize = 8
	// TagSize is the AEAD tag appended to every sealed body.
	TagSize = 16
	// CookieSize is the stateless handshake cookie.
	CookieSize = 16

	UnreliableHeaderSize = HeaderPrefixSize + 4 + CounterSize
	ReliableHeaderSize   = HeaderPrefixSize + 4 + 2 + CounterSize
	ACKHeaderSize        = HeaderPrefixSize + 4 + CounterSize
	DisconnectHeaderSize = HeaderPrefixSize + 4 + CounterSize

	// ACKBodySize is the plaintext an ACK seals: ackSeq + ackBits.
	ACKBodySize = 2 + 4

	ConnReqSize     = HeaderPrefixSize + 4 + 8
	ConnAcceptSize  = HeaderPrefixSize + 4 + 8 + 8 + CookieSize
	ConnConfirmSize = HeaderPrefixSize + 8 + 8 + 8 + CookieSize
)

var (
	ErrTooShort      = errors.New("packet too short")
	ErrBadType       = errors.New("unknown packet type")
	ErrBadProtocolID = errors.New("wrong protocol ID")
	ErrBadVersion    = errors.New("unsupported protocol version")
)

// MakeToken computes the connection token from client and server salts.
//
// The token identifies a session; it does not authenticate one. Authentication
// is the AEAD tag, with Tier 1's source-address binding kept as defence in
// depth.
func MakeToken(clientSalt, serverSalt uint64) uint32 {
	combined := clientSalt ^ serverSalt
	return uint32(combined) ^ uint32(combined>>32)
}

// SessionSalt is the HKDF salt both peers derive traffic keys under. Binding
// both salts means two sessions issued the same master key still produce
// different traffic keys.
func SessionSalt(clientSalt, serverSalt uint64) []byte {
	salt := make([]byte, 16)
	binary.BigEndian.PutUint64(salt[0:8], clientSalt)
	binary.BigEndian.PutUint64(salt[8:16], serverSalt)
	return salt
}

// checkPrefix validates the type and version bytes shared by every packet.
func checkPrefix(data []byte, want byte) error {
	if len(data) < HeaderPrefixSize {
		return ErrTooShort
	}
	if data[0] != want {
		return ErrBadType
	}
	if data[1] != Version {
		return ErrBadVersion
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------------

// EncodeConnReq encodes a connection request packet.
func EncodeConnReq(clientSalt uint64) []byte {
	buf := make([]byte, ConnReqSize)
	buf[0] = TypeConnReq
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], ProtocolID)
	binary.LittleEndian.PutUint64(buf[6:14], clientSalt)
	return buf
}

// DecodeConnReq decodes a connection request packet.
func DecodeConnReq(data []byte) (clientSalt uint64, err error) {
	if err := checkPrefix(data, TypeConnReq); err != nil {
		return 0, err
	}
	if len(data) < ConnReqSize {
		return 0, ErrTooShort
	}
	if binary.LittleEndian.Uint32(data[2:6]) != ProtocolID {
		return 0, ErrBadProtocolID
	}
	return binary.LittleEndian.Uint64(data[6:14]), nil
}

// EncodeConnAccept encodes a connection accept carrying the stateless cookie
// the client must echo in ConnConfirm.
func EncodeConnAccept(clientSalt, serverSalt uint64, cookie []byte) []byte {
	buf := make([]byte, ConnAcceptSize)
	buf[0] = TypeConnAccept
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], ProtocolID)
	binary.LittleEndian.PutUint64(buf[6:14], clientSalt)
	binary.LittleEndian.PutUint64(buf[14:22], serverSalt)
	copy(buf[22:38], cookie)
	return buf
}

// DecodeConnAccept decodes a connection accept. cookie aliases data.
func DecodeConnAccept(data []byte) (clientSalt, serverSalt uint64, cookie []byte, err error) {
	if err := checkPrefix(data, TypeConnAccept); err != nil {
		return 0, 0, nil, err
	}
	if len(data) < ConnAcceptSize {
		return 0, 0, nil, ErrTooShort
	}
	if binary.LittleEndian.Uint32(data[2:6]) != ProtocolID {
		return 0, 0, nil, ErrBadProtocolID
	}
	return binary.LittleEndian.Uint64(data[6:14]),
		binary.LittleEndian.Uint64(data[14:22]),
		data[22:38], nil
}

// EncodeConnConfirm encodes the client's proof step: it echoes the cookie and
// names the HTTPS-issued key it will authenticate with.
//
// This packet exists because the server cannot decrypt anything until it knows
// which key to use. Carrying the keyID on every data packet would cost eight
// bytes forever; carrying it once costs it once.
func EncodeConnConfirm(keyID, clientSalt, serverSalt uint64, cookie []byte) []byte {
	buf := make([]byte, ConnConfirmSize)
	buf[0] = TypeConnConfirm
	buf[1] = Version
	binary.LittleEndian.PutUint64(buf[2:10], keyID)
	binary.LittleEndian.PutUint64(buf[10:18], clientSalt)
	binary.LittleEndian.PutUint64(buf[18:26], serverSalt)
	copy(buf[26:42], cookie)
	return buf
}

// DecodeConnConfirm decodes a connection confirm. cookie aliases data.
func DecodeConnConfirm(data []byte) (keyID, clientSalt, serverSalt uint64, cookie []byte, err error) {
	if err := checkPrefix(data, TypeConnConfirm); err != nil {
		return 0, 0, 0, nil, err
	}
	if len(data) < ConnConfirmSize {
		return 0, 0, 0, nil, ErrTooShort
	}
	return binary.LittleEndian.Uint64(data[2:10]),
		binary.LittleEndian.Uint64(data[10:18]),
		binary.LittleEndian.Uint64(data[18:26]),
		data[26:42], nil
}

// ---------------------------------------------------------------------------
// Data packets
// ---------------------------------------------------------------------------

// EncodeUnreliableHeader writes the cleartext header; append the sealed body.
func EncodeUnreliableHeader(token uint32, counter uint64) []byte {
	buf := make([]byte, UnreliableHeaderSize)
	buf[0] = TypeUnreliable
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], token)
	binary.BigEndian.PutUint64(buf[6:14], counter)
	return buf
}

// DecodeUnreliable splits a packet into token, counter, AAD and sealed body.
func DecodeUnreliable(data []byte) (token uint32, counter uint64, aad, sealed []byte, err error) {
	if err := checkPrefix(data, TypeUnreliable); err != nil {
		return 0, 0, nil, nil, err
	}
	if len(data) < UnreliableHeaderSize+TagSize {
		return 0, 0, nil, nil, ErrTooShort
	}
	return binary.LittleEndian.Uint32(data[2:6]),
		binary.BigEndian.Uint64(data[6:14]),
		data[:UnreliableHeaderSize], data[UnreliableHeaderSize:], nil
}

// EncodeReliableHeader writes the cleartext header; append the sealed body.
func EncodeReliableHeader(token uint32, seq uint16, counter uint64) []byte {
	buf := make([]byte, ReliableHeaderSize)
	buf[0] = TypeReliable
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], token)
	binary.LittleEndian.PutUint16(buf[6:8], seq)
	binary.BigEndian.PutUint64(buf[8:16], counter)
	return buf
}

// DecodeReliable splits a packet into token, seq, counter, AAD and sealed body.
func DecodeReliable(data []byte) (token uint32, seq uint16, counter uint64, aad, sealed []byte, err error) {
	if err := checkPrefix(data, TypeReliable); err != nil {
		return 0, 0, 0, nil, nil, err
	}
	if len(data) < ReliableHeaderSize+TagSize {
		return 0, 0, 0, nil, nil, ErrTooShort
	}
	return binary.LittleEndian.Uint32(data[2:6]),
		binary.LittleEndian.Uint16(data[6:8]),
		binary.BigEndian.Uint64(data[8:16]),
		data[:ReliableHeaderSize], data[ReliableHeaderSize:], nil
}

// EncodeACKHeader writes the cleartext header; append the sealed ACK body.
//
// ACKs are sealed because a forged ACK breaks reliable delivery exactly as
// effectively as a dropped one: it retires a frame the peer never received.
func EncodeACKHeader(token uint32, counter uint64) []byte {
	buf := make([]byte, ACKHeaderSize)
	buf[0] = TypeACK
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], token)
	binary.BigEndian.PutUint64(buf[6:14], counter)
	return buf
}

// EncodeACKBody returns the plaintext an ACK seals.
func EncodeACKBody(ackSeq uint16, ackBits uint32) []byte {
	body := make([]byte, ACKBodySize)
	binary.LittleEndian.PutUint16(body[0:2], ackSeq)
	binary.LittleEndian.PutUint32(body[2:6], ackBits)
	return body
}

// DecodeACKBody parses a decrypted ACK body.
func DecodeACKBody(body []byte) (ackSeq uint16, ackBits uint32, err error) {
	if len(body) < ACKBodySize {
		return 0, 0, ErrTooShort
	}
	return binary.LittleEndian.Uint16(body[0:2]), binary.LittleEndian.Uint32(body[2:6]), nil
}

// DecodeACK splits a packet into token, counter, AAD and sealed body.
func DecodeACK(data []byte) (token uint32, counter uint64, aad, sealed []byte, err error) {
	if err := checkPrefix(data, TypeACK); err != nil {
		return 0, 0, nil, nil, err
	}
	if len(data) < ACKHeaderSize+TagSize {
		return 0, 0, nil, nil, ErrTooShort
	}
	return binary.LittleEndian.Uint32(data[2:6]),
		binary.BigEndian.Uint64(data[6:14]),
		data[:ACKHeaderSize], data[ACKHeaderSize:], nil
}

// EncodeDisconnectHeader writes the cleartext header; append a sealed EMPTY
// body, so the tag alone proves the sender holds the session key. In v1 anyone
// who learned a token could tear a session down.
func EncodeDisconnectHeader(token uint32, counter uint64) []byte {
	buf := make([]byte, DisconnectHeaderSize)
	buf[0] = TypeDisconnect
	buf[1] = Version
	binary.LittleEndian.PutUint32(buf[2:6], token)
	binary.BigEndian.PutUint64(buf[6:14], counter)
	return buf
}

// DecodeDisconnect splits a packet into token, counter, AAD and sealed body.
func DecodeDisconnect(data []byte) (token uint32, counter uint64, aad, sealed []byte, err error) {
	if err := checkPrefix(data, TypeDisconnect); err != nil {
		return 0, 0, nil, nil, err
	}
	if len(data) < DisconnectHeaderSize+TagSize {
		return 0, 0, nil, nil, ErrTooShort
	}
	return binary.LittleEndian.Uint32(data[2:6]),
		binary.BigEndian.Uint64(data[6:14]),
		data[:DisconnectHeaderSize], data[DisconnectHeaderSize:], nil
}

// SeqGreaterThan returns true if s1 > s2 accounting for 16-bit wrapping.
func SeqGreaterThan(s1, s2 uint16) bool {
	return (s1 > s2 && s1-s2 <= 32768) || (s1 < s2 && s2-s1 > 32768)
}
