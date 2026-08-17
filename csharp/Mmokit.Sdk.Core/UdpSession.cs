using System;

namespace Mmokit.Sdk.Core
{
    /// <summary>
    /// One UDP session's traffic keys, send counter and replay window.
    /// Structural mirror of Go's udpcrypto.Session — deliberately, so the two
    /// ends cannot drift on the parts that are easy to get wrong.
    ///
    /// Nonce uniqueness is structural: Seal allocates the counter itself and no
    /// public method accepts a caller-supplied one. A repeated nonce under a
    /// Poly1305-based AEAD leaks the one-time MAC key, not one plaintext, so
    /// this is not a place for a convenience overload.
    /// </summary>
    public sealed class UdpSession
    {
        /// How far out of order a packet may arrive and still be accepted,
        /// measured in counters behind the highest seen. 64 matches Go.
        public const int ReplayWindowSize = 64;

        const ulong MaxCounter = ulong.MaxValue - 1024;

        readonly byte[] _sendKey;
        readonly byte[] _recvKey;

        ulong _sendCounter;

        readonly object _replayLock = new object();
        ulong _replayHighest;
        ulong _replayBitmap;

        /// <summary>
        /// Derives both directional keys from the master key issued by
        /// POST /auth/udp-key and returns the session for the given role.
        /// </summary>
        public static UdpSession Derive(byte[] master, byte[] salt, bool isClient)
        {
            var c2s = UdpCrypto.DeriveKey(master, salt, UdpCrypto.LabelC2S);
            var s2c = UdpCrypto.DeriveKey(master, salt, UdpCrypto.LabelS2C);
            return isClient ? new UdpSession(c2s, s2c) : new UdpSession(s2c, c2s);
        }

        /// <summary>
        /// Builds the HKDF salt from both handshake salts. Must match Go's
        /// udpproto.SessionSalt byte for byte: clientSalt then serverSalt, both
        /// big-endian.
        /// </summary>
        public static byte[] SessionSalt(ulong clientSalt, ulong serverSalt)
        {
            var salt = new byte[16];
            for (int i = 0; i < 8; i++)
            {
                salt[7 - i] = (byte)(clientSalt >> (8 * i));
                salt[15 - i] = (byte)(serverSalt >> (8 * i));
            }
            return salt;
        }

        UdpSession(byte[] sendKey, byte[] recvKey)
        {
            _sendKey = sendKey;
            _recvKey = recvKey;
        }

        /// <summary>
        /// Seals body where the AAD is a header that must itself contain the
        /// counter. buildHeader runs exactly once with the counter this call
        /// allocated. Returns the complete packet, header first.
        /// </summary>
        public byte[] SealPacket(byte[]? body, Func<ulong, byte[]> buildHeader)
        {
            ulong counter;
            lock (_replayLock)
            {
                counter = ++_sendCounter;
            }
            if (counter > MaxCounter)
                throw new InvalidOperationException("udp send counter exhausted; rekey required");

            var header = buildHeader(counter);
            var sealedBody = UdpCrypto.Seal(_sendKey, counter, body ?? Array.Empty<byte>(), header);

            var packet = new byte[header.Length + sealedBody.Length];
            Buffer.BlockCopy(header, 0, packet, 0, header.Length);
            Buffer.BlockCopy(sealedBody, 0, packet, header.Length, sealedBody.Length);
            return packet;
        }

        /// <summary>
        /// Authenticates and decrypts, returning null on replay or forgery.
        ///
        /// The window is checked, THEN the packet is authenticated, and only
        /// then is the counter recorded. Recording first would let an off-path
        /// attacker fast-forward the window with forged counters and silence the
        /// session while both ends believed they were healthy.
        /// </summary>
        public byte[]? OpenPacket(ulong counter, byte[] sealedBody, byte[] aad)
        {
            if (counter == 0) return null; // counters start at 1
            lock (_replayLock)
            {
                if (!ReplayCheckLocked(counter)) return null;
            }

            var plaintext = UdpCrypto.Open(_recvKey, counter, sealedBody, aad);
            if (plaintext == null) return null;

            lock (_replayLock)
            {
                ReplayCommitLocked(counter);
            }
            return plaintext;
        }

        bool ReplayCheckLocked(ulong counter)
        {
            if (counter > _replayHighest) return true;
            ulong diff = _replayHighest - counter;
            if (diff >= ReplayWindowSize) return false; // too old to prove unseen
            return (_replayBitmap & (1UL << (int)diff)) == 0;
        }

        void ReplayCommitLocked(ulong counter)
        {
            if (counter > _replayHighest)
            {
                ulong shift = counter - _replayHighest;
                _replayBitmap = shift >= ReplayWindowSize ? 0UL : _replayBitmap << (int)shift;
                _replayBitmap |= 1UL;
                _replayHighest = counter;
                return;
            }
            ulong diff = _replayHighest - counter;
            if (diff < ReplayWindowSize) _replayBitmap |= 1UL << (int)diff;
        }
    }
}
