using System;
using System.Security.Cryptography;
using System.Text;

namespace Mmokit.Sdk.Core
{
    /// <summary>
    /// Client half of the UDP authenticated-encryption layer (CE-005b Tier 2).
    /// Must stay byte-compatible with pkg/net/udpcrypto — the golden vectors in
    /// UdpCryptoGoldenTests are generated from the Go implementation, not from
    /// this one.
    ///
    /// AES-256-GCM, not ChaCha20-Poly1305: this assembly targets netstandard2.1,
    /// the TFM Unity consumes, which ships AesGcm and does not ship
    /// ChaCha20Poly1305. netstandard2.1 also has no HKDF class, so Hkdf below is
    /// hand-rolled over HMACSHA256 and pinned to Go's output by golden test.
    /// </summary>
    public static class UdpCrypto
    {
        public const int KeySize = 32;
        public const int NonceSize = 12;
        public const int TagSize = 16;
        public const int CounterSize = 8;

        /// <summary>
        /// HKDF info labels. These strings are part of the wire contract:
        /// changing either one changes every derived key.
        /// </summary>
        public const string LabelC2S = "mmokit/udp/v1 client-to-server";
        public const string LabelS2C = "mmokit/udp/v1 server-to-client";

        /// <summary>
        /// Derives one direction's traffic key. Mirrors Go's
        /// hkdf.Key(sha256.New, master, salt, label, 32).
        /// </summary>
        public static byte[] DeriveKey(byte[] master, byte[] salt, string label)
        {
            if (master == null) throw new ArgumentNullException(nameof(master));
            if (master.Length != KeySize)
                throw new ArgumentException($"master key must be {KeySize} bytes", nameof(master));
            return Hkdf(master, salt ?? Array.Empty<byte>(), Encoding.UTF8.GetBytes(label), KeySize);
        }

        /// <summary>
        /// HKDF-SHA256 (RFC 5869): extract then expand. netstandard2.1 has no
        /// HKDF class, so this is the whole algorithm.
        /// </summary>
        public static byte[] Hkdf(byte[] ikm, byte[] salt, byte[] info, int length)
        {
            // Extract. RFC 5869: an empty salt is replaced by HashLen zero bytes,
            // which is what Go's hkdf.Extract does too.
            if (salt == null || salt.Length == 0) salt = new byte[32];
            byte[] prk;
            using (var h = new HMACSHA256(salt))
            {
                prk = h.ComputeHash(ikm);
            }

            // Expand.
            var okm = new byte[length];
            using (var h = new HMACSHA256(prk))
            {
                byte[] t = Array.Empty<byte>();
                int done = 0;
                byte counter = 1;
                while (done < length)
                {
                    var block = new byte[t.Length + info.Length + 1];
                    Buffer.BlockCopy(t, 0, block, 0, t.Length);
                    Buffer.BlockCopy(info, 0, block, t.Length, info.Length);
                    block[block.Length - 1] = counter;

                    t = h.ComputeHash(block);
                    int take = Math.Min(t.Length, length - done);
                    Buffer.BlockCopy(t, 0, okm, done, take);
                    done += take;
                    counter++;
                }
            }
            return okm;
        }

        /// <summary>
        /// Builds the GCM nonce for a packet counter. Leading four bytes are
        /// zero; the counter is big-endian in the trailing eight. The two
        /// directions use different keys, so the same counter value in each is
        /// safe.
        /// </summary>
        public static byte[] NonceFor(ulong counter)
        {
            var n = new byte[NonceSize];
            for (int i = 0; i < CounterSize; i++)
            {
                n[NonceSize - 1 - i] = (byte)(counter >> (8 * i));
            }
            return n;
        }

        /// <summary>
        /// Encrypts and authenticates, returning nonce-less ciphertext||tag —
        /// the same layout Go's Session.Seal produces.
        /// </summary>
        public static byte[] Seal(byte[] key, ulong counter, byte[] plaintext, byte[] aad)
        {
            var nonce = NonceFor(counter);
            var ct = new byte[plaintext.Length];
            var tag = new byte[TagSize];
            using (var gcm = NewGcm(key))
            {
                gcm.Encrypt(nonce, plaintext, ct, tag, aad);
            }
            var outBuf = new byte[ct.Length + TagSize];
            Buffer.BlockCopy(ct, 0, outBuf, 0, ct.Length);
            Buffer.BlockCopy(tag, 0, outBuf, ct.Length, TagSize);
            return outBuf;
        }

        /// <summary>
        /// Authenticates and decrypts. Returns null on any failure — callers
        /// must not distinguish authentication failure from a length problem,
        /// since that distinction is an oracle.
        /// </summary>
        public static byte[]? Open(byte[] key, ulong counter, byte[] sealed_, byte[] aad)
        {
            if (sealed_ == null || sealed_.Length < TagSize) return null;
            var nonce = NonceFor(counter);
            int ctLen = sealed_.Length - TagSize;
            var ct = new byte[ctLen];
            var tag = new byte[TagSize];
            Buffer.BlockCopy(sealed_, 0, ct, 0, ctLen);
            Buffer.BlockCopy(sealed_, ctLen, tag, 0, TagSize);

            var pt = new byte[ctLen];
            try
            {
                using (var gcm = NewGcm(key))
                {
                    gcm.Decrypt(nonce, ct, tag, pt, aad);
                }
            }
            catch (CryptographicException)
            {
                return null;
            }
            return pt;
        }

        /// <summary>
        /// Constructs an AesGcm instance. Isolated here because this is the one
        /// call whose availability differs across runtimes: netstandard2.1
        /// exposes only AesGcm(byte[]), while .NET 8+ obsoletes it in favour of
        /// AesGcm(byte[], int tagSizeInBytes). If Unity's IL2CPP backend cannot
        /// service this, it is the single line the whole cipher choice turns on.
        /// </summary>
        private static AesGcm NewGcm(byte[] key)
        {
#if NET8_0_OR_GREATER
            return new AesGcm(key, TagSize);
#else
            return new AesGcm(key);
#endif
        }
    }
}
