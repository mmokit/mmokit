using System;
using System.Collections.Generic;
using System.Text;

namespace Mmokit.Sdk.Core
{
    /// Little-endian growable writer for the reflect-codec wire format
    /// (pkg/universe/reflect_marshal.go). Field order is the caller's
    /// responsibility; this only writes primitives. Mirrors the layout the
    /// TS SDK encodes — string=u16+utf8, bytes=u32+raw, slice-len=u16, bool=1B.
    public sealed class ReflectWriter
    {
        readonly List<byte> _b = new();

        public byte[] ToArray() => _b.ToArray();

        public void WriteU8(byte v) => _b.Add(v);
        public void WriteI8(sbyte v) => _b.Add((byte)v);
        public void WriteU16(ushort v) { _b.Add((byte)v); _b.Add((byte)(v >> 8)); }
        public void WriteI16(short v) => WriteU16((ushort)v);
        public void WriteU32(uint v) { for (int i = 0; i < 4; i++) _b.Add((byte)(v >> (8 * i))); }
        public void WriteI32(int v) => WriteU32((uint)v);
        public void WriteU64(ulong v) { for (int i = 0; i < 8; i++) _b.Add((byte)(v >> (8 * i))); }
        public void WriteI64(long v) => WriteU64((ulong)v);
        public void WriteF32(float v) => WriteU32((uint)BitConverter.SingleToInt32Bits(v));
        public void WriteF64(double v) => WriteU64((ulong)BitConverter.DoubleToInt64Bits(v));
        public void WriteBool(bool v) => _b.Add((byte)(v ? 1 : 0));
        public void WriteEntity(uint netID) => WriteU32(netID);

        public void WriteString(string s)
        {
            byte[] bytes = Encoding.UTF8.GetBytes(s ?? "");
            WriteU16((ushort)bytes.Length);
            _b.AddRange(bytes);
        }

        public void WriteBytes(byte[] data)
        {
            int n = data?.Length ?? 0;
            WriteU32((uint)n);
            if (n > 0) _b.AddRange(data!);
        }

        /// slice element-count prefix (u16). Caller writes the elements after.
        public void WriteSliceLen(int n) => WriteU16((ushort)n);
    }

    /// Little-endian cursor reader, inverse of ReflectWriter.
    public sealed class ReflectReader
    {
        readonly byte[] _b;
        int _o;

        public ReflectReader(byte[] data) { _b = data; _o = 0; }

        public byte ReadU8() => _b[_o++];
        public sbyte ReadI8() => (sbyte)_b[_o++];
        public ushort ReadU16() { ushort v = (ushort)(_b[_o] | (_b[_o + 1] << 8)); _o += 2; return v; }
        public short ReadI16() => (short)ReadU16();
        public uint ReadU32() { uint v = (uint)_b[_o] | ((uint)_b[_o + 1] << 8) | ((uint)_b[_o + 2] << 16) | ((uint)_b[_o + 3] << 24); _o += 4; return v; }
        public int ReadI32() => (int)ReadU32();
        public ulong ReadU64() { ulong v = 0; for (int i = 0; i < 8; i++) v |= (ulong)_b[_o + i] << (8 * i); _o += 8; return v; }
        public long ReadI64() => (long)ReadU64();
        public float ReadF32() => BitConverter.Int32BitsToSingle((int)ReadU32());
        public double ReadF64() => BitConverter.Int64BitsToDouble((long)ReadU64());
        public bool ReadBool() => _b[_o++] != 0;
        public uint ReadEntity() => ReadU32();

        public string ReadString()
        {
            int n = ReadU16();
            string s = Encoding.UTF8.GetString(_b, _o, n);
            _o += n;
            return s;
        }

        public byte[] ReadBytes()
        {
            int n = (int)ReadU32();
            var r = new byte[n];
            Array.Copy(_b, _o, r, 0, n);
            _o += n;
            return r;
        }

        public int ReadSliceLen() => ReadU16();
    }
}
