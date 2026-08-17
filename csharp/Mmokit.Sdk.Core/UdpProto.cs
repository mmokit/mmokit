using System;

namespace Mmokit.Sdk.Core
{
    /// Stateless little-endian packet codec for the custom UDP game protocol.
    /// Faithful port of pkg/net/udpproto/proto.go. NOTE: little-endian — the
    /// delta-frame wire format (DeltaDecoderCore) is big-endian; these are
    /// different layers, do not conflate.
    public static class UdpProto
    {
        public const byte TypeUnreliable = 0x00;
        public const byte TypeReliable = 0x01;
        public const byte TypeAck = 0x02;
        public const byte TypeConnReq = 0x03;
        public const byte TypeConnAccept = 0x04;
        public const byte TypeDisconnect = 0x05;
        public const byte TypeConnConfirm = 0x06;

        /// Protocol version carried by every packet (CE-009). A peer sending a
        /// different version is rejected at the first packet rather than being
        /// allowed to misparse later ones.
        public const byte Version = 0x02;

        public const uint ProtocolID = 0x47414D45; // "GAME"

        public const int HeaderPrefixSize = 2; // type + version
        public const int CounterSize = 8;      // explicit AEAD packet counter
        public const int TagSize = 16;         // AEAD tag on every sealed body
        public const int CookieSize = 16;      // stateless handshake cookie

        public const int UnreliableHeaderSize = HeaderPrefixSize + 4 + CounterSize;
        public const int ReliableHeaderSize = HeaderPrefixSize + 4 + 2 + CounterSize;
        public const int AckHeaderSize = HeaderPrefixSize + 4 + CounterSize;
        public const int DisconnectHeaderSize = HeaderPrefixSize + 4 + CounterSize;
        public const int AckBodySize = 2 + 4;

        public const int ConnReqSize = HeaderPrefixSize + 4 + 8;
        public const int ConnAcceptSize = HeaderPrefixSize + 4 + 8 + 8 + CookieSize;
        public const int ConnConfirmSize = HeaderPrefixSize + 8 + 8 + 8 + CookieSize;

        // --- little-endian writers ---
        static void PutU16(byte[] b, int o, ushort v) { b[o] = (byte)v; b[o + 1] = (byte)(v >> 8); }
        static void PutU32(byte[] b, int o, uint v) { b[o] = (byte)v; b[o + 1] = (byte)(v >> 8); b[o + 2] = (byte)(v >> 16); b[o + 3] = (byte)(v >> 24); }
        static void PutU64(byte[] b, int o, ulong v) { for (int i = 0; i < 8; i++) b[o + i] = (byte)(v >> (8 * i)); }

        // The AEAD counter is big-endian; every other multi-byte field is
        // little-endian. Matching Go byte for byte matters more than internal
        // consistency here.
        static void PutU64BE(byte[] b, int o, ulong v) { for (int i = 0; i < 8; i++) b[o + 7 - i] = (byte)(v >> (8 * i)); }
        static ulong GetU64BE(byte[] b, int o) { ulong v = 0; for (int i = 0; i < 8; i++) v = (v << 8) | b[o + i]; return v; }

        // --- little-endian readers ---
        static ushort GetU16(byte[] b, int o) => (ushort)(b[o] | (b[o + 1] << 8));
        static uint GetU32(byte[] b, int o) => (uint)b[o] | ((uint)b[o + 1] << 8) | ((uint)b[o + 2] << 16) | ((uint)b[o + 3] << 24);
        static ulong GetU64(byte[] b, int o) { ulong v = 0; for (int i = 0; i < 8; i++) v |= (ulong)b[o + i] << (8 * i); return v; }

        public static uint MakeToken(ulong clientSalt, ulong serverSalt)
        {
            ulong combined = clientSalt ^ serverSalt;
            return (uint)combined ^ (uint)(combined >> 32);
        }

        // --- handshake ---

        public static byte[] EncodeConnReq(ulong clientSalt)
        {
            var b = new byte[ConnReqSize];
            b[0] = TypeConnReq; b[1] = Version;
            PutU32(b, 2, ProtocolID);
            PutU64(b, 6, clientSalt);
            return b;
        }

        /// Returns false if too short, wrong version, or wrong protocol ID.
        public static bool TryDecodeConnReq(byte[] data, out ulong clientSalt)
        {
            clientSalt = 0;
            if (data.Length < ConnReqSize) return false;
            if (data[1] != Version) return false;
            if (GetU32(data, 2) != ProtocolID) return false;
            clientSalt = GetU64(data, 6);
            return true;
        }

        public static byte[] EncodeConnAccept(ulong clientSalt, ulong serverSalt, byte[] cookie)
        {
            var b = new byte[ConnAcceptSize];
            b[0] = TypeConnAccept; b[1] = Version;
            PutU32(b, 2, ProtocolID);
            PutU64(b, 6, clientSalt);
            PutU64(b, 14, serverSalt);
            Array.Copy(cookie, 0, b, 22, CookieSize);
            return b;
        }

        public static bool TryDecodeConnAccept(byte[] data, out ulong clientSalt, out ulong serverSalt, out byte[] cookie)
        {
            clientSalt = 0; serverSalt = 0; cookie = Array.Empty<byte>();
            if (data.Length < ConnAcceptSize) return false;
            if (data[1] != Version) return false;
            if (GetU32(data, 2) != ProtocolID) return false;
            clientSalt = GetU64(data, 6);
            serverSalt = GetU64(data, 14);
            cookie = Sub(data, 22, CookieSize);
            return true;
        }

        /// The client's proof step: echoes the cookie and names the key issued
        /// over HTTPS. The server cannot decrypt anything until it knows which
        /// key to use, which is why the key ID travels once here rather than on
        /// every data packet.
        public static byte[] EncodeConnConfirm(ulong keyID, ulong clientSalt, ulong serverSalt, byte[] cookie)
        {
            var b = new byte[ConnConfirmSize];
            b[0] = TypeConnConfirm; b[1] = Version;
            PutU64(b, 2, keyID);
            PutU64(b, 10, clientSalt);
            PutU64(b, 18, serverSalt);
            Array.Copy(cookie, 0, b, 26, CookieSize);
            return b;
        }

        public static bool TryDecodeConnConfirm(byte[] data, out ulong keyID, out ulong clientSalt, out ulong serverSalt, out byte[] cookie)
        {
            keyID = 0; clientSalt = 0; serverSalt = 0; cookie = Array.Empty<byte>();
            if (data.Length < ConnConfirmSize) return false;
            if (data[1] != Version) return false;
            keyID = GetU64(data, 2);
            clientSalt = GetU64(data, 10);
            serverSalt = GetU64(data, 18);
            cookie = Sub(data, 26, CookieSize);
            return true;
        }

        // --- data packets ---
        //
        // Encoders emit the CLEARTEXT header only; the caller appends the sealed
        // body. Decoders return the header to use as the AEAD's additional
        // authenticated data plus the sealed body. Keeping the split here mirrors
        // Go's udpproto and keeps this class free of any crypto dependency.

        public static byte[] EncodeUnreliableHeader(uint token, ulong counter)
        {
            var b = new byte[UnreliableHeaderSize];
            b[0] = TypeUnreliable; b[1] = Version;
            PutU32(b, 2, token);
            PutU64BE(b, 6, counter);
            return b;
        }

        public static bool TryDecodeUnreliable(byte[] data, out uint token, out ulong counter, out byte[] aad, out byte[] sealedBody)
        {
            token = 0; counter = 0; aad = Array.Empty<byte>(); sealedBody = Array.Empty<byte>();
            if (data.Length < UnreliableHeaderSize + TagSize) return false;
            if (data[1] != Version) return false;
            token = GetU32(data, 2);
            counter = GetU64BE(data, 6);
            aad = Sub(data, 0, UnreliableHeaderSize);
            sealedBody = Sub(data, UnreliableHeaderSize, data.Length - UnreliableHeaderSize);
            return true;
        }

        public static byte[] EncodeReliableHeader(uint token, ushort seq, ulong counter)
        {
            var b = new byte[ReliableHeaderSize];
            b[0] = TypeReliable; b[1] = Version;
            PutU32(b, 2, token);
            PutU16(b, 6, seq);
            PutU64BE(b, 8, counter);
            return b;
        }

        public static bool TryDecodeReliable(byte[] data, out uint token, out ushort seq, out ulong counter, out byte[] aad, out byte[] sealedBody)
        {
            token = 0; seq = 0; counter = 0; aad = Array.Empty<byte>(); sealedBody = Array.Empty<byte>();
            if (data.Length < ReliableHeaderSize + TagSize) return false;
            if (data[1] != Version) return false;
            token = GetU32(data, 2);
            seq = GetU16(data, 6);
            counter = GetU64BE(data, 8);
            aad = Sub(data, 0, ReliableHeaderSize);
            sealedBody = Sub(data, ReliableHeaderSize, data.Length - ReliableHeaderSize);
            return true;
        }

        public static byte[] EncodeAckHeader(uint token, ulong counter)
        {
            var b = new byte[AckHeaderSize];
            b[0] = TypeAck; b[1] = Version;
            PutU32(b, 2, token);
            PutU64BE(b, 6, counter);
            return b;
        }

        /// The plaintext an ACK seals. ACKs are authenticated because a forged
        /// one retires a frame the peer never received.
        public static byte[] EncodeAckBody(ushort ackSeq, uint ackBits)
        {
            var b = new byte[AckBodySize];
            PutU16(b, 0, ackSeq);
            PutU32(b, 2, ackBits);
            return b;
        }

        public static bool TryDecodeAckBody(byte[] body, out ushort ackSeq, out uint ackBits)
        {
            ackSeq = 0; ackBits = 0;
            if (body.Length < AckBodySize) return false;
            ackSeq = GetU16(body, 0);
            ackBits = GetU32(body, 2);
            return true;
        }

        public static bool TryDecodeAck(byte[] data, out uint token, out ulong counter, out byte[] aad, out byte[] sealedBody)
        {
            token = 0; counter = 0; aad = Array.Empty<byte>(); sealedBody = Array.Empty<byte>();
            if (data.Length < AckHeaderSize + TagSize) return false;
            if (data[1] != Version) return false;
            token = GetU32(data, 2);
            counter = GetU64BE(data, 6);
            aad = Sub(data, 0, AckHeaderSize);
            sealedBody = Sub(data, AckHeaderSize, data.Length - AckHeaderSize);
            return true;
        }

        /// Disconnect seals an EMPTY body, so the tag alone proves the sender
        /// holds the session key. In v1 anyone who learned a token could tear a
        /// session down.
        public static byte[] EncodeDisconnectHeader(uint token, ulong counter)
        {
            var b = new byte[DisconnectHeaderSize];
            b[0] = TypeDisconnect; b[1] = Version;
            PutU32(b, 2, token);
            PutU64BE(b, 6, counter);
            return b;
        }

        public static bool TryDecodeDisconnect(byte[] data, out uint token, out ulong counter, out byte[] aad, out byte[] sealedBody)
        {
            token = 0; counter = 0; aad = Array.Empty<byte>(); sealedBody = Array.Empty<byte>();
            if (data.Length < DisconnectHeaderSize + TagSize) return false;
            if (data[1] != Version) return false;
            token = GetU32(data, 2);
            counter = GetU64BE(data, 6);
            aad = Sub(data, 0, DisconnectHeaderSize);
            sealedBody = Sub(data, DisconnectHeaderSize, data.Length - DisconnectHeaderSize);
            return true;
        }

        /// s1 > s2 accounting for 16-bit wrap.
        public static bool SeqGreaterThan(ushort s1, ushort s2)
            => (s1 > s2 && s1 - s2 <= 32768) || (s1 < s2 && s2 - s1 > 32768);

        static byte[] Sub(byte[] data, int start, int len)
        {
            var b = new byte[len];
            Array.Copy(data, start, b, 0, len);
            return b;
        }
    }
}
