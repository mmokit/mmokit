using System;
using System.Collections.Generic;
using System.Text;

namespace Mmokit.Sdk.Core
{
    /// Decoded 20-byte SE_DELTA_WORLD_UPDATE frame header.
    public struct FrameHeader
    {
        public uint Tick;
        public uint Seq;
        public uint Flags;
        public ushort FullCount;
        public ushort DeltaCount;
        public ushort RemovedCount;
        public ushort ExitedCount;
    }

    /// A full entity entry. InitialData is null when the entry carries none.
    public struct FullEntryHeader
    {
        public uint NetID;
        public uint Epoch;
        public byte EntityType;
        public ulong ProducedAtMs;
        public byte[] Snapshot;
        public byte[]? InitialData;
    }

    /// A delta entity entry. DeltaData is the bitmask + changed-field payload.
    public struct DeltaEntryHeader
    {
        public uint NetID;
        public uint Epoch;
        public byte EntityType;
        public ulong ProducedAtMs;
        public byte[] DeltaData;
    }

    /// Per-entity baseline store for delta decoding (latest full snapshot per netID).
    public sealed class BaselineStore<T>
    {
        readonly Dictionary<uint, (byte[] Snapshot, T? Meta)> _store = new();
        public void Set(uint netID, byte[] snapshot, T? meta = default) => _store[netID] = (snapshot, meta);
        public bool TryGet(uint netID, out byte[] snapshot, out T? meta)
        {
            if (_store.TryGetValue(netID, out var v)) { snapshot = v.Snapshot; meta = v.Meta; return true; }
            snapshot = Array.Empty<byte>(); meta = default; return false;
        }
        public void Delete(uint netID) => _store.Remove(netID);
        public void Clear() => _store.Clear();
    }

    /// Reusable building blocks for decoding mmokit binary wire frames.
    /// Faithful port of pkg/quantize/ts/delta-decoder-core.ts. All multi-byte
    /// reads are big-endian. Games layer entity-type-specific decode on top.
    public static class DeltaDecoderCore
    {
        public const int FrameHeaderSize = 20;
        public const uint FrameFlagFreshSnapshot = 1u << 0;
        public const uint FrameFlagInputAck = 1u << 1;

        // --- big-endian primitive reads ---
        public static short ReadInt16(byte[] data, int offset)
        {
            int v = (data[offset] << 8) | data[offset + 1];
            return (short)(v > 32767 ? v - 65536 : v);
        }

        public static ushort ReadUint16(byte[] data, int offset)
            => (ushort)((data[offset] << 8) | data[offset + 1]);

        public static uint ReadUint32(byte[] data, int offset)
            => ((uint)data[offset] << 24) | ((uint)data[offset + 1] << 16)
             | ((uint)data[offset + 2] << 8) | data[offset + 3];

        public static ulong ReadUint64(byte[] data, int offset)
        {
            ulong hi = ReadUint32(data, offset);
            ulong lo = ReadUint32(data, offset + 4);
            return (hi << 32) | lo;
        }

        public static float ReadFloat32(byte[] data, int offset)
            => BitConverter.Int32BitsToSingle((int)ReadUint32(data, offset));

        // --- dequantizers (match pkg/quantize/quantize.go) ---
        public static double UnAngle(int q) => (q / 65535.0) * 2.0 * Math.PI - Math.PI;
        public static double UnNorm(int q) => q / 255.0;
        public static double UnVel(int q, double scale) => (q / 32767.0) * scale;
        public static double UnRel(int q, double halfRange) => (q / 32767.0) * halfRange;

        // --- frame header ---
        public static (FrameHeader Header, int Offset) DecodeFrameHeader(byte[] data, int offset)
        {
            int pos = offset;
            var h = new FrameHeader
            {
                Tick = ReadUint32(data, pos),
                Seq = ReadUint32(data, pos + 4),
                Flags = ReadUint32(data, pos + 8),
                FullCount = ReadUint16(data, pos + 12),
                DeltaCount = ReadUint16(data, pos + 14),
                RemovedCount = ReadUint16(data, pos + 16),
                ExitedCount = ReadUint16(data, pos + 18),
            };
            return (h, pos + 20);
        }

        // --- entries ---
        public static (FullEntryHeader Entry, int Offset) DecodeFullEntry(byte[] data, int pos)
        {
            uint netID = ReadUint32(data, pos); pos += 4;
            uint epoch = ReadUint32(data, pos); pos += 4;
            byte entityType = data[pos]; pos += 1;
            ulong producedAtMs = ReadUint64(data, pos); pos += 8;
            ushort snapLen = ReadUint16(data, pos); pos += 2;
            byte[] snapshot = Slice(data, pos, snapLen); pos += snapLen;
            ushort initLen = ReadUint16(data, pos); pos += 2;
            byte[]? initialData = null;
            if (initLen > 0) { initialData = Slice(data, pos, initLen); pos += initLen; }
            return (new FullEntryHeader { NetID = netID, Epoch = epoch, EntityType = entityType, ProducedAtMs = producedAtMs, Snapshot = snapshot, InitialData = initialData }, pos);
        }

        public static (DeltaEntryHeader Entry, int Offset) DecodeDeltaEntry(byte[] data, int pos)
        {
            uint netID = ReadUint32(data, pos); pos += 4;
            uint epoch = ReadUint32(data, pos); pos += 4;
            byte entityType = data[pos]; pos += 1;
            ulong producedAtMs = ReadUint64(data, pos); pos += 8;
            ushort deltaLen = ReadUint16(data, pos); pos += 2;
            byte[] deltaData = Slice(data, pos, deltaLen); pos += deltaLen;
            return (new DeltaEntryHeader { NetID = netID, Epoch = epoch, EntityType = entityType, ProducedAtMs = producedAtMs, DeltaData = deltaData }, pos);
        }

        public static (uint[] Ids, int Offset) DecodeRemovedIDs(byte[] data, int pos, int count)
        {
            var ids = new uint[count];
            for (int i = 0; i < count; i++) { ids[i] = ReadUint32(data, pos); pos += 4; }
            return (ids, pos);
        }

        /// Decode the optional uint32 processed-input acknowledgement trailer.
        /// pos must point just after the frame's exited-ID list.
        public static (uint? Sequence, int Offset) DecodeInputAck(byte[] data, int pos, uint flags)
        {
            if ((flags & FrameFlagInputAck) == 0) return (null, pos);
            if (pos + 4 > data.Length) throw new ArgumentException("delta frame: truncated input acknowledgement trailer", nameof(data));
            return (ReadUint32(data, pos), pos + 4);
        }

        // --- generic delta application (mirrors applyDelta in the TS) ---
        public static byte[] ApplyDelta(int[] fieldSizes, bool hasVarTail, byte[] baseline, byte[] delta)
        {
            int totalLogicalFields = fieldSizes.Length + (hasVarTail ? 1 : 0);
            int bitmaskSize = (totalLogicalFields + 7) / 8; // ceil(/8)

            int fixedSize = 0;
            for (int i = 0; i < fieldSizes.Length; i++) fixedSize += fieldSizes[i];

            int dpos = bitmaskSize;
            byte[] result = (byte[])baseline.Clone();

            int baseOff = 0;
            for (int i = 0; i < fieldSizes.Length; i++)
            {
                int bit = (delta[i / 8] >> (i % 8)) & 1;
                if (bit != 0)
                {
                    int sz = fieldSizes[i];
                    Array.Copy(delta, dpos, result, baseOff, sz);
                    dpos += sz;
                }
                baseOff += fieldSizes[i];
            }

            if (hasVarTail)
            {
                int tailIdx = fieldSizes.Length;
                int bit = (delta[tailIdx / 8] >> (tailIdx % 8)) & 1;
                if (bit != 0)
                {
                    int newLen = ReadUint16(delta, dpos); dpos += 2;
                    var rebuilt = new byte[fixedSize + 2 + newLen];
                    Array.Copy(result, 0, rebuilt, 0, fixedSize);
                    rebuilt[fixedSize] = (byte)((newLen >> 8) & 0xFF);
                    rebuilt[fixedSize + 1] = (byte)(newLen & 0xFF);
                    Array.Copy(delta, dpos, rebuilt, fixedSize + 2, newLen);
                    result = rebuilt;
                }
            }

            return result;
        }

        // --- length-prefixed strings (UTF-8) ---
        public static string DecodeLengthPrefixedStringU8(byte[] data)
        {
            if (data.Length < 1) return "";
            int len = data[0];
            if (len == 0 || 1 + len > data.Length) return "";
            return Encoding.UTF8.GetString(data, 1, len);
        }

        public static string DecodeLengthPrefixedStringU16(byte[] data)
        {
            if (data.Length < 2) return "";
            int len = ReadUint16(data, 0);
            if (len == 0 || 2 + len > data.Length) return "";
            return Encoding.UTF8.GetString(data, 2, len);
        }

        static byte[] Slice(byte[] data, int start, int len)
        {
            var b = new byte[len];
            Array.Copy(data, start, b, 0, len);
            return b;
        }
    }
}
