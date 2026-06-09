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

        public const uint ProtocolID = 0x47414D45; // "GAME"

        public const int UnreliableHeaderSize = 1 + 4;
        public const int ReliableHeaderSize = 1 + 4 + 2;
        public const int AckSize = 1 + 4 + 2 + 4;
        public const int ConnReqSize = 1 + 4 + 8;
        public const int ConnAcceptSize = 1 + 4 + 8 + 8;
        public const int DisconnectSize = 1 + 4;

        // --- little-endian writers ---
        static void PutU16(byte[] b, int o, ushort v) { b[o] = (byte)v; b[o + 1] = (byte)(v >> 8); }
        static void PutU32(byte[] b, int o, uint v) { b[o] = (byte)v; b[o + 1] = (byte)(v >> 8); b[o + 2] = (byte)(v >> 16); b[o + 3] = (byte)(v >> 24); }
        static void PutU64(byte[] b, int o, ulong v) { for (int i = 0; i < 8; i++) b[o + i] = (byte)(v >> (8 * i)); }

        // --- little-endian readers ---
        static ushort GetU16(byte[] b, int o) => (ushort)(b[o] | (b[o + 1] << 8));
        static uint GetU32(byte[] b, int o) => (uint)b[o] | ((uint)b[o + 1] << 8) | ((uint)b[o + 2] << 16) | ((uint)b[o + 3] << 24);
        static ulong GetU64(byte[] b, int o) { ulong v = 0; for (int i = 0; i < 8; i++) v |= (ulong)b[o + i] << (8 * i); return v; }

        public static uint MakeToken(ulong clientSalt, ulong serverSalt)
        {
            ulong combined = clientSalt ^ serverSalt;
            return (uint)combined ^ (uint)(combined >> 32);
        }

        public static byte[] EncodeConnReq(ulong clientSalt)
        {
            var b = new byte[ConnReqSize];
            b[0] = TypeConnReq;
            PutU32(b, 1, ProtocolID);
            PutU64(b, 5, clientSalt);
            return b;
        }

        /// Returns false if too short or wrong protocol ID.
        public static bool TryDecodeConnReq(byte[] data, out ulong clientSalt)
        {
            clientSalt = 0;
            if (data.Length < ConnReqSize) return false;
            if (GetU32(data, 1) != ProtocolID) return false;
            clientSalt = GetU64(data, 5);
            return true;
        }

        public static byte[] EncodeConnAccept(ulong clientSalt, ulong serverSalt)
        {
            var b = new byte[ConnAcceptSize];
            b[0] = TypeConnAccept;
            PutU32(b, 1, ProtocolID);
            PutU64(b, 5, clientSalt);
            PutU64(b, 13, serverSalt);
            return b;
        }

        public static bool TryDecodeConnAccept(byte[] data, out ulong clientSalt, out ulong serverSalt)
        {
            clientSalt = 0; serverSalt = 0;
            if (data.Length < ConnAcceptSize) return false;
            if (GetU32(data, 1) != ProtocolID) return false;
            clientSalt = GetU64(data, 5);
            serverSalt = GetU64(data, 13);
            return true;
        }

        public static byte[] EncodeUnreliable(uint token, byte[]? payload)
        {
            int plen = payload?.Length ?? 0;
            var b = new byte[UnreliableHeaderSize + plen];
            b[0] = TypeUnreliable;
            PutU32(b, 1, token);
            if (plen > 0) Array.Copy(payload!, 0, b, UnreliableHeaderSize, plen);
            return b;
        }

        public static bool TryDecodeUnreliable(byte[] data, out uint token, out byte[] payload)
        {
            token = 0; payload = Array.Empty<byte>();
            if (data.Length < UnreliableHeaderSize) return false;
            token = GetU32(data, 1);
            payload = Sub(data, UnreliableHeaderSize, data.Length - UnreliableHeaderSize);
            return true;
        }

        public static byte[] EncodeReliable(uint token, ushort seq, byte[] payload)
        {
            var b = new byte[ReliableHeaderSize + payload.Length];
            b[0] = TypeReliable;
            PutU32(b, 1, token);
            PutU16(b, 5, seq);
            Array.Copy(payload, 0, b, ReliableHeaderSize, payload.Length);
            return b;
        }

        public static bool TryDecodeReliable(byte[] data, out uint token, out ushort seq, out byte[] payload)
        {
            token = 0; seq = 0; payload = Array.Empty<byte>();
            if (data.Length < ReliableHeaderSize) return false;
            token = GetU32(data, 1);
            seq = GetU16(data, 5);
            payload = Sub(data, ReliableHeaderSize, data.Length - ReliableHeaderSize);
            return true;
        }

        public static byte[] EncodeAck(uint token, ushort ackSeq, uint ackBits)
        {
            var b = new byte[AckSize];
            b[0] = TypeAck;
            PutU32(b, 1, token);
            PutU16(b, 5, ackSeq);
            PutU32(b, 7, ackBits);
            return b;
        }

        public static bool TryDecodeAck(byte[] data, out uint token, out ushort ackSeq, out uint ackBits)
        {
            token = 0; ackSeq = 0; ackBits = 0;
            if (data.Length < AckSize) return false;
            token = GetU32(data, 1);
            ackSeq = GetU16(data, 5);
            ackBits = GetU32(data, 7);
            return true;
        }

        public static byte[] EncodeDisconnect(uint token)
        {
            var b = new byte[DisconnectSize];
            b[0] = TypeDisconnect;
            PutU32(b, 1, token);
            return b;
        }

        public static bool TryDecodeDisconnect(byte[] data, out uint token)
        {
            token = 0;
            if (data.Length < DisconnectSize) return false;
            token = GetU32(data, 1);
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
