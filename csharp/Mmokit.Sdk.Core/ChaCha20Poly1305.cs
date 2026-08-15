using System;

namespace Mmokit.Sdk.Core
{
    /// <summary>
    /// ChaCha20-Poly1305 AEAD (RFC 8439), implemented in pure managed C#.
    ///
    /// WHY THIS EXISTS RATHER THAN System.Security.Cryptography:
    /// netstandard2.1 declares AesGcm but Mono deliberately does not implement
    /// it — the constructor throws PlatformNotSupportedException
    /// (mono/mono#19285) — and Unity 6.5 still ships the Mono class library,
    /// which IL2CPP inherits. netstandard2.1 has no ChaCha20Poly1305 class at
    /// all. Either way the client has to bring its own AEAD, so it brings the
    /// one that is safe to implement in software.
    ///
    /// ChaCha20 is add-rotate-xor: no S-boxes, no table lookups, and therefore
    /// no data-dependent memory access. A managed AES would be the opposite —
    /// table-driven and cache-timing observable — so this is the more correct
    /// primitive for a managed client, not merely the more available one.
    ///
    /// Must stay byte-compatible with golang.org/x/crypto/chacha20poly1305 as
    /// used by pkg/net/udpcrypto. Pinned by both the RFC 8439 vectors and
    /// vectors generated from Go.
    /// </summary>
    public static class ChaCha20Poly1305
    {
        public const int KeySize = 32;
        public const int NonceSize = 12;
        public const int TagSize = 16;

        /// <summary>
        /// Encrypts and authenticates, returning ciphertext||tag.
        /// </summary>
        public static byte[] Seal(byte[] key, byte[] nonce, byte[] plaintext, byte[]? aad)
        {
            if (key == null || key.Length != KeySize) throw new ArgumentException("key must be 32 bytes", nameof(key));
            if (nonce == null || nonce.Length != NonceSize) throw new ArgumentException("nonce must be 12 bytes", nameof(nonce));
            plaintext ??= Array.Empty<byte>();
            aad ??= Array.Empty<byte>();

            // Block 0 of the keystream yields the one-time Poly1305 key; the
            // message starts at block 1. Reusing block 0 for data would hand the
            // attacker the MAC key.
            var polyKey = new byte[64];
            ChaCha20.Block(key, nonce, 0, polyKey);

            var output = new byte[plaintext.Length + TagSize];
            Buffer.BlockCopy(plaintext, 0, output, 0, plaintext.Length);
            ChaCha20.XorKeyStream(key, nonce, 1, output, 0, plaintext.Length);

            var tag = ComputeTag(polyKey, aad, output, plaintext.Length);
            Buffer.BlockCopy(tag, 0, output, plaintext.Length, TagSize);
            Array.Clear(polyKey, 0, polyKey.Length);
            return output;
        }

        /// <summary>
        /// Authenticates and decrypts ciphertext||tag. Returns null on any
        /// failure; callers must not distinguish the reasons, since doing so is
        /// an oracle.
        /// </summary>
        public static byte[]? Open(byte[]? key, byte[]? nonce, byte[]? sealedBytes, byte[]? aad)
        {
            if (key == null || key.Length != KeySize) return null;
            if (nonce == null || nonce.Length != NonceSize) return null;
            if (sealedBytes == null || sealedBytes.Length < TagSize) return null;
            aad ??= Array.Empty<byte>();

            int ctLen = sealedBytes.Length - TagSize;

            var polyKey = new byte[64];
            ChaCha20.Block(key, nonce, 0, polyKey);
            var expected = ComputeTag(polyKey, aad, sealedBytes, ctLen);
            Array.Clear(polyKey, 0, polyKey.Length);

            // Constant-time: never return early on the first differing byte.
            int diff = 0;
            for (int i = 0; i < TagSize; i++) diff |= expected[i] ^ sealedBytes[ctLen + i];
            if (diff != 0) return null;

            var plaintext = new byte[ctLen];
            Buffer.BlockCopy(sealedBytes, 0, plaintext, 0, ctLen);
            ChaCha20.XorKeyStream(key, nonce, 1, plaintext, 0, ctLen);
            return plaintext;
        }

        /// <summary>
        /// RFC 8439 §2.8: MAC over aad || pad16(aad) || ct || pad16(ct) ||
        /// len(aad) || len(ct), both lengths little-endian u64.
        ///
        /// The padding and the trailing lengths are what stop an attacker
        /// shifting bytes between the AAD and the ciphertext without changing
        /// the tag.
        /// </summary>
        private static byte[] ComputeTag(byte[] polyKey, byte[] aad, byte[] ct, int ctLen)
        {
            var poly = new Poly1305(polyKey);
            poly.Update(aad, 0, aad.Length);
            poly.UpdatePadding(aad.Length);
            poly.Update(ct, 0, ctLen);
            poly.UpdatePadding(ctLen);

            var lengths = new byte[16];
            WriteUInt64LE(lengths, 0, (ulong)aad.Length);
            WriteUInt64LE(lengths, 8, (ulong)ctLen);
            poly.Update(lengths, 0, 16);

            var tag = new byte[TagSize];
            poly.Finish(tag);
            return tag;
        }

        private static void WriteUInt64LE(byte[] dst, int offset, ulong v)
        {
            for (int i = 0; i < 8; i++) dst[offset + i] = (byte)(v >> (8 * i));
        }
    }

    /// <summary>ChaCha20 stream cipher (RFC 8439 §2.3-2.4).</summary>
    internal static class ChaCha20
    {
        // "expand 32-byte k"
        private const uint C0 = 0x61707865, C1 = 0x3320646e, C2 = 0x79622d32, C3 = 0x6b206574;

        /// <summary>Writes one 64-byte keystream block for the given counter.</summary>
        internal static void Block(byte[] key, byte[] nonce, uint counter, byte[] output)
        {
            Span<uint> state = stackalloc uint[16];
            InitState(state, key, nonce, counter);

            Span<uint> x = stackalloc uint[16];
            state.CopyTo(x);
            Rounds(x);

            unchecked
            {
                for (int i = 0; i < 16; i++)
                {
                    uint v = x[i] + state[i];
                    output[i * 4 + 0] = (byte)v;
                    output[i * 4 + 1] = (byte)(v >> 8);
                    output[i * 4 + 2] = (byte)(v >> 16);
                    output[i * 4 + 3] = (byte)(v >> 24);
                }
            }
        }

        /// <summary>XORs count bytes of keystream into buf, starting at counter.</summary>
        internal static void XorKeyStream(byte[] key, byte[] nonce, uint counter, byte[] buf, int offset, int count)
        {
            var block = new byte[64];
            int done = 0;
            while (done < count)
            {
                Block(key, nonce, counter, block);
                int n = Math.Min(64, count - done);
                for (int i = 0; i < n; i++) buf[offset + done + i] ^= block[i];
                done += n;
                counter++;
            }
            Array.Clear(block, 0, block.Length);
        }

        private static void InitState(Span<uint> s, byte[] key, byte[] nonce, uint counter)
        {
            s[0] = C0; s[1] = C1; s[2] = C2; s[3] = C3;
            for (int i = 0; i < 8; i++) s[4 + i] = ReadUInt32LE(key, i * 4);
            s[12] = counter;
            for (int i = 0; i < 3; i++) s[13 + i] = ReadUInt32LE(nonce, i * 4);
        }

        private static void Rounds(Span<uint> x)
        {
            // 20 rounds = 10 iterations of (column round + diagonal round).
            for (int i = 0; i < 10; i++)
            {
                QuarterRound(x, 0, 4, 8, 12);
                QuarterRound(x, 1, 5, 9, 13);
                QuarterRound(x, 2, 6, 10, 14);
                QuarterRound(x, 3, 7, 11, 15);
                QuarterRound(x, 0, 5, 10, 15);
                QuarterRound(x, 1, 6, 11, 12);
                QuarterRound(x, 2, 7, 8, 13);
                QuarterRound(x, 3, 4, 9, 14);
            }
        }

        private static void QuarterRound(Span<uint> x, int a, int b, int c, int d)
        {
            unchecked
            {
                x[a] += x[b]; x[d] = RotL(x[d] ^ x[a], 16);
                x[c] += x[d]; x[b] = RotL(x[b] ^ x[c], 12);
                x[a] += x[b]; x[d] = RotL(x[d] ^ x[a], 8);
                x[c] += x[d]; x[b] = RotL(x[b] ^ x[c], 7);
            }
        }

        private static uint RotL(uint v, int n) => (v << n) | (v >> (32 - n));

        private static uint ReadUInt32LE(byte[] b, int o) =>
            (uint)(b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | (b[o + 3] << 24));
    }

    /// <summary>
    /// Poly1305 one-time authenticator (RFC 8439 §2.5).
    ///
    /// The 26-bit-limb representation is the standard portable construction: it
    /// keeps every intermediate inside a ulong so 130-bit arithmetic works
    /// without BigInteger, which netstandard2.1 has but which allocates per
    /// operation and is not constant-time.
    /// </summary>
    internal sealed class Poly1305
    {
        private readonly uint[] _r = new uint[5];
        private readonly uint[] _pad = new uint[4];
        private readonly uint[] _h = new uint[5];
        private readonly byte[] _buffer = new byte[16];
        private int _leftover;
        private bool _final;

        internal Poly1305(byte[] key)
        {
            // r is clamped: the masks below fold RFC 8439's clamp into the limb
            // split, so r's top four bits and the low two bits of three bytes
            // are cleared.
            uint t0 = ReadUInt32LE(key, 0), t1 = ReadUInt32LE(key, 4);
            uint t2 = ReadUInt32LE(key, 8), t3 = ReadUInt32LE(key, 12);

            _r[0] = t0 & 0x3ffffff;
            _r[1] = ((t0 >> 26) | (t1 << 6)) & 0x3ffff03;
            _r[2] = ((t1 >> 20) | (t2 << 12)) & 0x3ffc0ff;
            _r[3] = ((t2 >> 14) | (t3 << 18)) & 0x3f03fff;
            _r[4] = (t3 >> 8) & 0x00fffff;

            for (int i = 0; i < 4; i++) _pad[i] = ReadUInt32LE(key, 16 + i * 4);
        }

        internal void Update(byte[] m, int offset, int count)
        {
            if (_leftover > 0)
            {
                int want = Math.Min(16 - _leftover, count);
                Buffer.BlockCopy(m, offset, _buffer, _leftover, want);
                _leftover += want;
                offset += want;
                count -= want;
                if (_leftover < 16) return;
                Blocks(_buffer, 0, 16);
                _leftover = 0;
            }
            if (count >= 16)
            {
                int want = count & ~15;
                Blocks(m, offset, want);
                offset += want;
                count -= want;
            }
            if (count > 0)
            {
                Buffer.BlockCopy(m, offset, _buffer, _leftover, count);
                _leftover += count;
            }
        }

        /// <summary>Feeds RFC 8439's zero padding to the next 16-byte boundary.</summary>
        internal void UpdatePadding(int lengthSoFar)
        {
            int rem = lengthSoFar % 16;
            if (rem == 0) return;
            var zeros = new byte[16 - rem];
            Update(zeros, 0, zeros.Length);
        }

        private void Blocks(byte[] m, int offset, int bytes)
        {
            unchecked
            {
                uint hibit = _final ? 0u : (1u << 24);
                uint r0 = _r[0], r1 = _r[1], r2 = _r[2], r3 = _r[3], r4 = _r[4];
                uint s1 = r1 * 5, s2 = r2 * 5, s3 = r3 * 5, s4 = r4 * 5;
                uint h0 = _h[0], h1 = _h[1], h2 = _h[2], h3 = _h[3], h4 = _h[4];

                while (bytes >= 16)
                {
                    // h += m
                    h0 += ReadUInt32LE(m, offset + 0) & 0x3ffffff;
                    h1 += (ReadUInt32LE(m, offset + 3) >> 2) & 0x3ffffff;
                    h2 += (ReadUInt32LE(m, offset + 6) >> 4) & 0x3ffffff;
                    h3 += (ReadUInt32LE(m, offset + 9) >> 6) & 0x3ffffff;
                    h4 += (ReadUInt32LE(m, offset + 12) >> 8) | hibit;

                    // h *= r, with the 2^130-5 reduction folded in via s1..s4
                    ulong d0 = (ulong)h0 * r0 + (ulong)h1 * s4 + (ulong)h2 * s3 + (ulong)h3 * s2 + (ulong)h4 * s1;
                    ulong d1 = (ulong)h0 * r1 + (ulong)h1 * r0 + (ulong)h2 * s4 + (ulong)h3 * s3 + (ulong)h4 * s2;
                    ulong d2 = (ulong)h0 * r2 + (ulong)h1 * r1 + (ulong)h2 * r0 + (ulong)h3 * s4 + (ulong)h4 * s3;
                    ulong d3 = (ulong)h0 * r3 + (ulong)h1 * r2 + (ulong)h2 * r1 + (ulong)h3 * r0 + (ulong)h4 * s4;
                    ulong d4 = (ulong)h0 * r4 + (ulong)h1 * r3 + (ulong)h2 * r2 + (ulong)h3 * r1 + (ulong)h4 * r0;

                    ulong c = d0 >> 26; h0 = (uint)d0 & 0x3ffffff;
                    d1 += c; c = d1 >> 26; h1 = (uint)d1 & 0x3ffffff;
                    d2 += c; c = d2 >> 26; h2 = (uint)d2 & 0x3ffffff;
                    d3 += c; c = d3 >> 26; h3 = (uint)d3 & 0x3ffffff;
                    d4 += c; c = d4 >> 26; h4 = (uint)d4 & 0x3ffffff;
                    h0 += (uint)c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
                    h1 += (uint)c;

                    offset += 16;
                    bytes -= 16;
                }

                _h[0] = h0; _h[1] = h1; _h[2] = h2; _h[3] = h3; _h[4] = h4;
            }
        }

        internal void Finish(byte[] mac)
        {
            unchecked
            {
                if (_leftover > 0)
                {
                    _buffer[_leftover++] = 1;
                    while (_leftover < 16) _buffer[_leftover++] = 0;
                    _final = true;
                    Blocks(_buffer, 0, 16);
                }

                uint h0 = _h[0], h1 = _h[1], h2 = _h[2], h3 = _h[3], h4 = _h[4];

                uint c = h1 >> 26; h1 &= 0x3ffffff;
                h2 += c; c = h2 >> 26; h2 &= 0x3ffffff;
                h3 += c; c = h3 >> 26; h3 &= 0x3ffffff;
                h4 += c; c = h4 >> 26; h4 &= 0x3ffffff;
                h0 += c * 5; c = h0 >> 26; h0 &= 0x3ffffff;
                h1 += c;

                // g = h + 5; if g overflows 2^130 then h >= p and g is the answer.
                uint g0 = h0 + 5; c = g0 >> 26; g0 &= 0x3ffffff;
                uint g1 = h1 + c; c = g1 >> 26; g1 &= 0x3ffffff;
                uint g2 = h2 + c; c = g2 >> 26; g2 &= 0x3ffffff;
                uint g3 = h3 + c; c = g3 >> 26; g3 &= 0x3ffffff;
                uint g4 = h4 + c - (1u << 26);

                // Branch-free select: mask is all-ones when g did not borrow.
                uint mask = (g4 >> 31) - 1;
                g0 &= mask; g1 &= mask; g2 &= mask; g3 &= mask; g4 &= mask;
                mask = ~mask;
                h0 = (h0 & mask) | g0;
                h1 = (h1 & mask) | g1;
                h2 = (h2 & mask) | g2;
                h3 = (h3 & mask) | g3;
                h4 = (h4 & mask) | g4;

                // Collapse 26-bit limbs back to 32-bit words.
                h0 = (h0 | (h1 << 26));
                h1 = ((h1 >> 6) | (h2 << 20));
                h2 = ((h2 >> 12) | (h3 << 14));
                h3 = ((h3 >> 18) | (h4 << 8));

                // mac = (h + pad) mod 2^128
                ulong f = (ulong)h0 + _pad[0]; h0 = (uint)f;
                f = (ulong)h1 + _pad[1] + (f >> 32); h1 = (uint)f;
                f = (ulong)h2 + _pad[2] + (f >> 32); h2 = (uint)f;
                f = (ulong)h3 + _pad[3] + (f >> 32); h3 = (uint)f;

                WriteUInt32LE(mac, 0, h0);
                WriteUInt32LE(mac, 4, h1);
                WriteUInt32LE(mac, 8, h2);
                WriteUInt32LE(mac, 12, h3);
            }
        }

        private static uint ReadUInt32LE(byte[] b, int o) =>
            (uint)(b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | (b[o + 3] << 24));

        private static void WriteUInt32LE(byte[] b, int o, uint v)
        {
            b[o] = (byte)v; b[o + 1] = (byte)(v >> 8);
            b[o + 2] = (byte)(v >> 16); b[o + 3] = (byte)(v >> 24);
        }
    }
}
