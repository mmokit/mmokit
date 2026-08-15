using System;
using System.Text;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// <summary>
    /// Cross-language pin for the UDP AEAD layer (CE-005b Tier 2, unit 1).
    ///
    /// Every expected value here was emitted by pkg/net/udpcrypto running under
    /// Go, not by this implementation. That direction matters: the server is
    /// authoritative, so a C# bug must show up as a failure here rather than as
    /// two implementations agreeing on the wrong answer.
    ///
    /// The runtime-availability caveat that used to sit here is gone with the
    /// cipher it applied to. ChaCha20-Poly1305 is implemented in managed C# in
    /// this assembly, so there is no platform backend to be missing: what runs
    /// under net10.0 here is the same code that runs under IL2CPP.
    /// </summary>
    public class UdpCryptoGoldenTests
    {
        // Master key: testKey(0x5a) in pkg/net/udpcrypto — k[i] = 0x5a ^ i.
        private const string MasterHex =
            "5a5b58595e5f5c5d52535051565754554a4b48494e4f4c4d4243404146474445";
        private const string SaltHex = "73657373696f6e2d73616c74"; // "session-salt"
        private const string AadHex = "0001deadbeef";

        private static byte[] Hex(string s)
        {
            var b = new byte[s.Length / 2];
            for (int i = 0; i < b.Length; i++)
                b[i] = Convert.ToByte(s.Substring(i * 2, 2), 16);
            return b;
        }

        private static string ToHex(byte[] b) =>
            BitConverter.ToString(b).Replace("-", "").ToLowerInvariant();

        [Fact]
        public void HkdfMatchesGo()
        {
            var master = Hex(MasterHex);
            var salt = Hex(SaltHex);

            Assert.Equal(
                "3e409b08142ab65722a44b73839c67d987a612a4ce4f7115801affd0603278b0",
                ToHex(UdpCrypto.DeriveKey(master, salt, UdpCrypto.LabelC2S)));

            Assert.Equal(
                "f7efdf12c4bfdb42ea7da0a698c8a39d51ca41f6ffc63157aa094ec6078be933",
                ToHex(UdpCrypto.DeriveKey(master, salt, UdpCrypto.LabelS2C)));
        }

        [Theory]
        // counter, plaintext, expected ciphertext||tag — all from Go.
        [InlineData(1UL, "", "7f0557263d6343535481ad5537e1060b")]
        [InlineData(2UL, "hello", "7ee59228932ce5332e8db65574c5a3ee000dd0cfd7")]
        [InlineData(3UL, "client to server",
            "83ba29e957b1e780c2a99e4658f93b5ba82283e21129bd3baee9edef1a81bcdf")]
        public void SealMatchesGo(ulong counter, string plaintext, string expectedHex)
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            var sealedBytes = UdpCrypto.Seal(key, counter, Encoding.UTF8.GetBytes(plaintext), Hex(AadHex));
            Assert.Equal(expectedHex, ToHex(sealedBytes));
        }

        [Theory]
        [InlineData(1UL, "", "7f0557263d6343535481ad5537e1060b")]
        [InlineData(2UL, "hello", "7ee59228932ce5332e8db65574c5a3ee000dd0cfd7")]
        [InlineData(3UL, "client to server",
            "83ba29e957b1e780c2a99e4658f93b5ba82283e21129bd3baee9edef1a81bcdf")]
        public void OpenRoundTripsGoCiphertext(ulong counter, string plaintext, string sealedHex)
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            var pt = UdpCrypto.Open(key, counter, Hex(sealedHex), Hex(AadHex));
            Assert.NotNull(pt);
            Assert.Equal(plaintext, Encoding.UTF8.GetString(pt!));
        }

        [Fact]
        public void NonceLayoutMatchesGo()
        {
            Assert.Equal("000000000000000000000001", ToHex(UdpCrypto.NonceFor(1)));
            Assert.Equal("0000000000000000000000ff", ToHex(UdpCrypto.NonceFor(255)));
            Assert.Equal("000000000123456789abcdef", ToHex(UdpCrypto.NonceFor(0x0123456789abcdef)));
        }

        [Fact]
        public void WrongCounterFailsToOpen()
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            var sealedBytes = Hex("7ee59228932ce5332e8db65574c5a3ee000dd0cfd7");
            Assert.Null(UdpCrypto.Open(key, 3, sealedBytes, Hex(AadHex)));
        }

        [Fact]
        public void WrongAadFailsToOpen()
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            var sealedBytes = Hex("7ee59228932ce5332e8db65574c5a3ee000dd0cfd7");
            Assert.Null(UdpCrypto.Open(key, 2, sealedBytes, Hex("0002deadbeef")));
        }

        [Fact]
        public void WrongDirectionKeyFailsToOpen()
        {
            // A client-sealed packet must not open under the server-send key.
            var s2c = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelS2C);
            var sealedBytes = Hex("7ee59228932ce5332e8db65574c5a3ee000dd0cfd7");
            Assert.Null(UdpCrypto.Open(s2c, 2, sealedBytes, Hex(AadHex)));
        }

        [Fact]
        public void TamperedCiphertextFailsToOpen()
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            var sealedBytes = Hex("7ee59228932ce5332e8db65574c5a3ee000dd0cfd7");
            for (int i = 0; i < sealedBytes.Length; i++)
            {
                var bad = (byte[])sealedBytes.Clone();
                bad[i] ^= 0x01;
                Assert.Null(UdpCrypto.Open(key, 2, bad, Hex(AadHex)));
            }
        }

        [Fact]
        public void ShortInputFailsToOpen()
        {
            var key = UdpCrypto.DeriveKey(Hex(MasterHex), Hex(SaltHex), UdpCrypto.LabelC2S);
            Assert.Null(UdpCrypto.Open(key, 1, new byte[UdpCrypto.TagSize - 1], Hex(AadHex)));
            Assert.Null(UdpCrypto.Open(key, 1, Array.Empty<byte>(), Hex(AadHex)));
        }
    }
}
