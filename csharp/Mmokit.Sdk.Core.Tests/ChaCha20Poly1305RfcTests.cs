using System;
using System.Text;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// <summary>
    /// RFC 8439 official test vectors for the hand-rolled ChaCha20-Poly1305.
    ///
    /// These matter more than the cross-language vectors: the Go vectors prove
    /// the two implementations agree, but two implementations can agree on the
    /// wrong answer. The RFC vectors prove the algorithm itself is right, which
    /// is the only thing that makes "agrees with Go" meaningful.
    ///
    /// This code exists because netstandard2.1 — Unity's TFM — has no
    /// ChaCha20Poly1305, and its AesGcm is a Mono stub that throws
    /// PlatformNotSupportedException. See ChaCha20Poly1305.cs.
    /// </summary>
    public class ChaCha20Poly1305RfcTests
    {
        private static byte[] Hex(string s)
        {
            s = s.Replace(" ", "").Replace(":", "").Replace("\n", "");
            var b = new byte[s.Length / 2];
            for (int i = 0; i < b.Length; i++)
                b[i] = Convert.ToByte(s.Substring(i * 2, 2), 16);
            return b;
        }

        private static string ToHex(byte[] b) =>
            BitConverter.ToString(b).Replace("-", "").ToLowerInvariant();

        // RFC 8439 §2.8.2 — the AEAD worked example. Exercises the whole
        // construction: block 0 for the Poly1305 key, encryption from block 1,
        // AAD padding, and the trailing length block.
        private const string RfcKey =
            "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f";
        private const string RfcNonce = "070000004041424344454647";
        private const string RfcAad = "50515253c0c1c2c3c4c5c6c7";
        private const string RfcPlaintext =
            "Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.";
        private const string RfcCiphertext =
            "d31a8d34648e60db7b86afbc53ef7ec2a4aded51296e08fea9e2b5a736ee62d6" +
            "3dbea45e8ca9671282fafb69da92728b1a71de0a9e060b2905d6a5b67ecd3b36" +
            "92ddbd7f2d778b8c9803aee328091b58fab324e4fad675945585808b4831d7bc" +
            "3ff4def08e4b7a9de576d26586cec64b6116";
        private const string RfcTag = "1ae10b594f09e26a7e902ecbd0600691";

        [Fact]
        public void SealMatchesRfc8439Vector()
        {
            var got = ChaCha20Poly1305.Seal(
                Hex(RfcKey), Hex(RfcNonce),
                Encoding.ASCII.GetBytes(RfcPlaintext), Hex(RfcAad));

            Assert.Equal(RfcCiphertext + RfcTag, ToHex(got));
        }

        [Fact]
        public void OpenMatchesRfc8439Vector()
        {
            var pt = ChaCha20Poly1305.Open(
                Hex(RfcKey), Hex(RfcNonce),
                Hex(RfcCiphertext + RfcTag), Hex(RfcAad));

            Assert.NotNull(pt);
            Assert.Equal(RfcPlaintext, Encoding.ASCII.GetString(pt!));
        }

        [Fact]
        public void OpenRejectsTamperedAad()
        {
            var badAad = Hex(RfcAad);
            badAad[0] ^= 0x01;
            Assert.Null(ChaCha20Poly1305.Open(
                Hex(RfcKey), Hex(RfcNonce), Hex(RfcCiphertext + RfcTag), badAad));
        }

        [Fact]
        public void OpenRejectsTamperedCiphertext()
        {
            var sealedBytes = Hex(RfcCiphertext + RfcTag);
            sealedBytes[5] ^= 0x01;
            Assert.Null(ChaCha20Poly1305.Open(
                Hex(RfcKey), Hex(RfcNonce), sealedBytes, Hex(RfcAad)));
        }

        [Fact]
        public void OpenRejectsTamperedTag()
        {
            var sealedBytes = Hex(RfcCiphertext + RfcTag);
            sealedBytes[sealedBytes.Length - 1] ^= 0x01;
            Assert.Null(ChaCha20Poly1305.Open(
                Hex(RfcKey), Hex(RfcNonce), sealedBytes, Hex(RfcAad)));
        }

        [Fact]
        public void OpenRejectsWrongNonce()
        {
            var badNonce = Hex(RfcNonce);
            badNonce[0] ^= 0x01;
            Assert.Null(ChaCha20Poly1305.Open(
                Hex(RfcKey), badNonce, Hex(RfcCiphertext + RfcTag), Hex(RfcAad)));
        }

        // Empty plaintext and empty AAD are the boundaries of the padding rules
        // in §2.8, and are where a hand-rolled Poly1305 most often gets the
        // pad16 handling wrong.
        [Theory]
        [InlineData("", "")]
        [InlineData("", "aabbcc")]
        [InlineData("00", "")]
        [InlineData("0102030405060708090a0b0c0d0e0f", "")]
        [InlineData("0102030405060708090a0b0c0d0e0f10", "")]        // exactly one block
        [InlineData("0102030405060708090a0b0c0d0e0f1011", "aabb")]  // one block + 1
        public void RoundTripsAcrossPaddingBoundaries(string ptHex, string aadHex)
        {
            var key = Hex(RfcKey);
            var nonce = Hex(RfcNonce);
            var pt = Hex(ptHex);
            var aad = Hex(aadHex);

            var sealedBytes = ChaCha20Poly1305.Seal(key, nonce, pt, aad);
            Assert.Equal(pt.Length + ChaCha20Poly1305.TagSize, sealedBytes.Length);

            var got = ChaCha20Poly1305.Open(key, nonce, sealedBytes, aad);
            Assert.NotNull(got);
            Assert.Equal(ToHex(pt), ToHex(got!));
        }

        // Multi-block plaintext crosses the ChaCha20 block counter, which is
        // where an off-by-one in the starting counter hides: a wrong start would
        // still round-trip against itself but disagree with Go and the RFC.
        [Fact]
        public void RoundTripsMultipleChaChaBlocks()
        {
            var key = Hex(RfcKey);
            var nonce = Hex(RfcNonce);
            var pt = new byte[64 * 3 + 7];
            for (int i = 0; i < pt.Length; i++) pt[i] = (byte)(i * 31);

            var sealedBytes = ChaCha20Poly1305.Seal(key, nonce, pt, Array.Empty<byte>());
            var got = ChaCha20Poly1305.Open(key, nonce, sealedBytes, Array.Empty<byte>());
            Assert.NotNull(got);
            Assert.Equal(ToHex(pt), ToHex(got!));
        }

        [Fact]
        public void OpenRejectsShortInput()
        {
            var key = Hex(RfcKey);
            var nonce = Hex(RfcNonce);
            Assert.Null(ChaCha20Poly1305.Open(key, nonce, new byte[ChaCha20Poly1305.TagSize - 1], null));
            Assert.Null(ChaCha20Poly1305.Open(key, nonce, Array.Empty<byte>(), null));
            Assert.Null(ChaCha20Poly1305.Open(key, nonce, null, null));
        }

        [Fact]
        public void SealRejectsBadKeyOrNonceLength()
        {
            Assert.Throws<ArgumentException>(() =>
                ChaCha20Poly1305.Seal(new byte[31], Hex(RfcNonce), Array.Empty<byte>(), null));
            Assert.Throws<ArgumentException>(() =>
                ChaCha20Poly1305.Seal(Hex(RfcKey), new byte[11], Array.Empty<byte>(), null));
        }
    }
}
