# C# SDK — Plan 4: C# UDP Transport Port Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the custom UDP transport (handshake + reliability + ACK + keepalive/timeout) to C#, so the C# SDK can connect to the mmoserver UDP listener — proven correct by golden wire-parity tests, hermetic state-machine unit tests, and a localhost loopback integration test.

**Architecture:** Three layers in `Mmokit.Sdk.Core`. (1) `UdpProto.cs` — a stateless **little-endian** packet codec, faithful port of `pkg/net/udpproto/proto.go`, golden-tested against Go. (2) `UdpTransport.cs` protocol core — the reliability/ACK/retransmit state machine from `pkg/net/udpclient/client.go`, constructed with an injected `sendRaw` sink + clock so it is unit-testable with **no sockets or threads**. (3) The same `UdpTransport.cs` gains a real-socket `Connect` factory + receive/tick loops over `System.Net.Sockets.UdpClient`, validated by a localhost loopback test. The **encryption seam** (spec §B) is realized as exactly two chokepoints: the `sendRaw` delegate (outbound) and the `HandlePacket` entry (inbound) — a future encryption layer wraps only these two points; the protocol logic above operates on decrypted bytes.

**Tech Stack:** C# (`Mmokit.Sdk.Core` netstandard2.1 lib, `net10.0` xUnit tests), Go (`cmd/csharp-golden` extension), `dotnet test`.

**Spec:** [docs/superpowers/specs/2026-06-06-csharp-sdk-unity-design.md](../specs/2026-06-06-csharp-sdk-unity-design.md) §C (UdpTransport.cs), §B (encryption seam), §F.3 (golden).

**Prerequisites:** Plan 3 merged (the `csharp/` workspace + golden infra exist; `cmd/csharp-golden` emits `delta_golden.json`).

---

## Background facts (verified in current source)

- `pkg/net/udpproto/proto.go` — **all multi-byte fields little-endian.** Packet types: Unreliable `0x00`, Reliable `0x01`, ACK `0x02`, ConnReq `0x03`, ConnAccept `0x04`, Disconnect `0x05`. `ProtocolID = 0x47414D45`. Layouts: ConnReq `[type][protocolID u32][clientSalt u64]` (13B); ConnAccept `[type][protocolID u32][clientSalt u64][serverSalt u64]` (21B); Unreliable `[type][token u32][payload]`; Reliable `[type][token u32][seq u16][payload]`; ACK `[type][token u32][ackSeq u16][ackBits u32]` (11B); Disconnect `[type][token u32]` (5B). `MakeToken(cs,ss) = uint32(cs^ss) ^ uint32((cs^ss)>>32)`. `SeqGreaterThan(s1,s2) = (s1>s2 && s1-s2<=32768) || (s1<s2 && s2-s1>32768)`.
- `pkg/net/udpclient/client.go` — constants: `reliableBufSize=256`, `retransmitInterval=100ms`, `reliableTimeout=5s`, `keepaliveInterval=1s`, `connectionTimeout=10s`, `handshakeTimeout=5s`, `maxPacket=1400`. Handshake: send ConnReq(clientSalt), await ConnAccept, verify echoed clientSalt, `token=MakeToken(cs,ss)`. Reliability: `sendSeq` increments per reliable send; `sendBuf[seq%256]` holds `{payload,sentAt,acked,used}`. Inbound: `updateRecvTracking(seq)` maintains `recvSeq`/`recvBits` (32-bit ACK bitfield); `processACK(ackSeq,ackBits)` marks buffer entries acked. Tick (100ms): connection-timeout→close; retransmit unacked entries older than `retransmitInterval` (close if older than `reliableTimeout`); send ACK if `ackDirty`; keepalive (empty unreliable) if idle > `keepaliveInterval`.
- **Known latent bug in the Go reference** (`client.go:332`): retransmit reconstructs `seq := uint16(i)` from the buffer index, correct only for the first 256 reliable sends (after wrap, `seq%256==i` no longer implies `seq==i`). The C# port replicates this faithfully (parity) and documents it; fixing it belongs in a separate both-languages change, not this port.

---

## File Structure

- **Create:** `csharp/Mmokit.Sdk.Core/UdpProto.cs` — stateless little-endian packet codec.
- **Create:** `csharp/Mmokit.Sdk.Core/UdpTransport.cs` — protocol state machine (Task 2) + real-socket factory & loops (Task 3).
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/UdpProtoGoldenTests.cs` — codec golden tests.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/UdpTransportCoreTests.cs` — hermetic state-machine tests.
- **Create:** `csharp/Mmokit.Sdk.Core.Tests/UdpTransportLoopbackTests.cs` — localhost integration test.
- **Modify:** `cmd/csharp-golden/main.go` — emit a `udp` golden section.
- **Modify:** `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs` — add `udp` DTOs.

---

### Task 1: UdpProto codec + golden parity

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/UdpProto.cs`
- Modify: `cmd/csharp-golden/main.go`, `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/UdpProtoGoldenTests.cs`

- [ ] **Step 1: Create the codec** `csharp/Mmokit.Sdk.Core/UdpProto.cs`:

```csharp
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
        static uint GetU32(byte[] b, int o) => (uint)(b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | ((uint)b[o + 3] << 24));
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
```

- [ ] **Step 2: Extend the Go golden generator** — in `cmd/csharp-golden/main.go`, add a `Udp` section. Add to the `Manifest` struct a field `Udp UdpCases \`json:"udp"\``, define the DTOs, and populate them. Add these type definitions (near the other DTO types):

```go
type UdpCases struct {
	Tokens  []TokenCase  `json:"tokens"`
	Packets []PacketCase `json:"packets"`
	SeqCmp  []SeqCase    `json:"seqCmp"`
}

type TokenCase struct {
	ClientSalt uint64 `json:"clientSalt"`
	ServerSalt uint64 `json:"serverSalt"`
	Token      uint32 `json:"token"`
}

type PacketCase struct {
	Kind     string `json:"kind"` // connReq|connAccept|unreliable|reliable|ack|disconnect
	HexBytes string `json:"hexBytes"`
	Token    uint32 `json:"token"`
	Seq      uint16 `json:"seq"`
	AckSeq   uint16 `json:"ackSeq"`
	AckBits  uint32 `json:"ackBits"`
	ClientSalt uint64 `json:"clientSalt"`
	ServerSalt uint64 `json:"serverSalt"`
	PayloadHex string `json:"payloadHex"`
}

type SeqCase struct {
	S1      uint16 `json:"s1"`
	S2      uint16 `json:"s2"`
	Greater bool   `json:"greater"`
}
```

Add `Udp UdpCases \`json:"udp"\`` to the `Manifest` struct definition. Then, just before the `out := filepath.Join(...)` line in `main()`, populate it:

```go
	// --- UDP protocol golden cases (udpproto is little-endian) ---
	var u UdpCases
	cs, ss := uint64(0x1122334455667788), uint64(0x99AABBCCDDEEFF00)
	u.Tokens = append(u.Tokens, TokenCase{ClientSalt: cs, ServerSalt: ss, Token: udpproto.MakeToken(cs, ss)})

	pl := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	u.Packets = append(u.Packets,
		PacketCase{Kind: "connReq", HexBytes: hex.EncodeToString(udpproto.EncodeConnReq(cs)), ClientSalt: cs},
		PacketCase{Kind: "connAccept", HexBytes: hex.EncodeToString(udpproto.EncodeConnAccept(cs, ss)), ClientSalt: cs, ServerSalt: ss},
		PacketCase{Kind: "unreliable", HexBytes: hex.EncodeToString(udpproto.EncodeUnreliable(0xCAFEBABE, pl)), Token: 0xCAFEBABE, PayloadHex: hex.EncodeToString(pl)},
		PacketCase{Kind: "reliable", HexBytes: hex.EncodeToString(udpproto.EncodeReliable(0xCAFEBABE, 7, pl)), Token: 0xCAFEBABE, Seq: 7, PayloadHex: hex.EncodeToString(pl)},
		PacketCase{Kind: "ack", HexBytes: hex.EncodeToString(udpproto.EncodeACK(0xCAFEBABE, 12, 0x0000000B)), Token: 0xCAFEBABE, AckSeq: 12, AckBits: 0x0000000B},
		PacketCase{Kind: "disconnect", HexBytes: hex.EncodeToString(udpproto.EncodeDisconnect(0xCAFEBABE)), Token: 0xCAFEBABE},
	)

	u.SeqCmp = append(u.SeqCmp,
		SeqCase{S1: 5, S2: 3, Greater: udpproto.SeqGreaterThan(5, 3)},
		SeqCase{S1: 3, S2: 5, Greater: udpproto.SeqGreaterThan(3, 5)},
		SeqCase{S1: 1, S2: 65535, Greater: udpproto.SeqGreaterThan(1, 65535)},   // wrap: 1 > 65535
		SeqCase{S1: 65535, S2: 1, Greater: udpproto.SeqGreaterThan(65535, 1)},
	)
	m.Udp = u
```

Add the import `"github.com/zenion/mmoserver/pkg/net/udpproto"` to the file's import block.

- [ ] **Step 3: Regenerate the manifest**

Run: `go vet ./cmd/csharp-golden/... && go run ./cmd/csharp-golden`
Expected: vet clean; prints the written-manifest line. Confirm the JSON now has a top-level `udp` key: `grep -c '"udp"' csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json` → ≥1.

- [ ] **Step 4: Add the C# golden DTOs** — append to `csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs`'s `Manifest` class a property `public UdpCases Udp { get; set; } = new();` and add these classes in the same namespace:

```csharp
    public class UdpCases
    {
        public TokenCase[] Tokens { get; set; } = Array.Empty<TokenCase>();
        public PacketCase[] Packets { get; set; } = Array.Empty<PacketCase>();
        public SeqCase[] SeqCmp { get; set; } = Array.Empty<SeqCase>();
    }
    public class TokenCase { public ulong ClientSalt { get; set; } public ulong ServerSalt { get; set; } public uint Token { get; set; } }
    public class PacketCase
    {
        public string Kind { get; set; } = "";
        public string HexBytes { get; set; } = "";
        public uint Token { get; set; } public ushort Seq { get; set; }
        public ushort AckSeq { get; set; } public uint AckBits { get; set; }
        public ulong ClientSalt { get; set; } public ulong ServerSalt { get; set; }
        public string PayloadHex { get; set; } = "";
    }
    public class SeqCase { public ushort S1 { get; set; } public ushort S2 { get; set; } public bool Greater { get; set; } }
```

- [ ] **Step 5: Write the golden tests** `csharp/Mmokit.Sdk.Core.Tests/UdpProtoGoldenTests.cs`:

```csharp
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpProtoGoldenTests
    {
        readonly Manifest g = Golden.Load();

        [Fact]
        public void MakeToken_MatchesGo()
        {
            foreach (var t in g.Udp.Tokens)
                Assert.Equal(t.Token, UdpProto.MakeToken(t.ClientSalt, t.ServerSalt));
        }

        [Fact]
        public void Encode_MatchesGoBytes()
        {
            foreach (var p in g.Udp.Packets)
            {
                byte[] got = p.Kind switch
                {
                    "connReq" => UdpProto.EncodeConnReq(p.ClientSalt),
                    "connAccept" => UdpProto.EncodeConnAccept(p.ClientSalt, p.ServerSalt),
                    "unreliable" => UdpProto.EncodeUnreliable(p.Token, Golden.Hex(p.PayloadHex)),
                    "reliable" => UdpProto.EncodeReliable(p.Token, p.Seq, Golden.Hex(p.PayloadHex)),
                    "ack" => UdpProto.EncodeAck(p.Token, p.AckSeq, p.AckBits),
                    "disconnect" => UdpProto.EncodeDisconnect(p.Token),
                    _ => throw new Xunit.Sdk.XunitException($"unknown kind {p.Kind}"),
                };
                Assert.Equal(Golden.Hex(p.HexBytes), got);
            }
        }

        [Fact]
        public void Decode_MatchesGoFields()
        {
            foreach (var p in g.Udp.Packets)
            {
                byte[] data = Golden.Hex(p.HexBytes);
                switch (p.Kind)
                {
                    case "connReq":
                        Assert.True(UdpProto.TryDecodeConnReq(data, out ulong cs));
                        Assert.Equal(p.ClientSalt, cs);
                        break;
                    case "connAccept":
                        Assert.True(UdpProto.TryDecodeConnAccept(data, out ulong cs2, out ulong ss2));
                        Assert.Equal(p.ClientSalt, cs2);
                        Assert.Equal(p.ServerSalt, ss2);
                        break;
                    case "unreliable":
                        Assert.True(UdpProto.TryDecodeUnreliable(data, out uint tok, out byte[] pay));
                        Assert.Equal(p.Token, tok);
                        Assert.Equal(Golden.Hex(p.PayloadHex), pay);
                        break;
                    case "reliable":
                        Assert.True(UdpProto.TryDecodeReliable(data, out uint tok2, out ushort seq, out byte[] pay2));
                        Assert.Equal(p.Token, tok2);
                        Assert.Equal(p.Seq, seq);
                        Assert.Equal(Golden.Hex(p.PayloadHex), pay2);
                        break;
                    case "ack":
                        Assert.True(UdpProto.TryDecodeAck(data, out uint tok3, out ushort aseq, out uint abits));
                        Assert.Equal(p.Token, tok3);
                        Assert.Equal(p.AckSeq, aseq);
                        Assert.Equal(p.AckBits, abits);
                        break;
                    case "disconnect":
                        Assert.True(UdpProto.TryDecodeDisconnect(data, out uint tok4));
                        Assert.Equal(p.Token, tok4);
                        break;
                }
            }
        }

        [Fact]
        public void SeqGreaterThan_MatchesGo()
        {
            foreach (var s in g.Udp.SeqCmp)
                Assert.Equal(s.Greater, UdpProto.SeqGreaterThan(s.S1, s.S2));
        }
    }
}
```

- [ ] **Step 6: Run + commit**

Run: `cd csharp && dotnet test --filter UdpProtoGoldenTests 2>&1 | tail -8`
Expected: `Passed!  - Failed: 0, Passed: 4`.

```bash
git add csharp/Mmokit.Sdk.Core/UdpProto.cs cmd/csharp-golden/main.go csharp/Mmokit.Sdk.Core.Tests/GoldenModel.cs csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json csharp/Mmokit.Sdk.Core.Tests/UdpProtoGoldenTests.cs
git commit -m "feat(csharp): port udpproto codec to UdpProto.cs (little-endian) + golden

Faithful port of pkg/net/udpproto: ConnReq/Accept/Reliable/Unreliable/ACK/
Disconnect encode+decode, MakeToken, SeqGreaterThan. Verified byte-for-byte
against Go via extended golden manifest (token/packet/seq cases).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: UdpTransport protocol core (hermetic state machine)

The reliability/ACK/retransmit logic from `udpclient/client.go`, with an injected `sendRaw` sink and `nowMs` clock — no sockets, no threads. Unit-testable by calling `HandlePacket`/`Tick` directly.

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/UdpTransport.cs`
- Create: `csharp/Mmokit.Sdk.Core.Tests/UdpTransportCoreTests.cs`

- [ ] **Step 1: Write the failing tests** `csharp/Mmokit.Sdk.Core.Tests/UdpTransportCoreTests.cs`:

```csharp
using System;
using System.Collections.Generic;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpTransportCoreTests
    {
        // Build a core with a captured send-sink and a controllable clock.
        static (UdpTransport t, List<byte[]> sent, long[] clock) NewCore(uint token = 0xCAFEBABE)
        {
            var sent = new List<byte[]>();
            var clock = new long[] { 0 };
            var t = new UdpTransport(raw => sent.Add(raw), () => clock[0], token);
            return (t, sent, clock);
        }

        [Fact]
        public void SendReliable_EmitsReliablePacket_AndIncrementsSeq()
        {
            var (t, sent, _) = NewCore();
            t.SendReliable(new byte[] { 1, 2, 3 });
            t.SendReliable(new byte[] { 4 });
            Assert.Equal(2, sent.Count);
            Assert.True(UdpProto.TryDecodeReliable(sent[0], out _, out ushort s0, out _));
            Assert.True(UdpProto.TryDecodeReliable(sent[1], out _, out ushort s1, out _));
            Assert.Equal(0, s0);
            Assert.Equal(1, s1);
        }

        [Fact]
        public void HandlePacket_Reliable_QueuesPayloadForRecv()
        {
            var (t, _, _) = NewCore();
            byte[] pkt = UdpProto.EncodeReliable(0xCAFEBABE, 0, new byte[] { 9, 9 });
            t.HandlePacket(pkt);
            Assert.True(t.TryRecv(out byte[] got, 0));
            Assert.Equal(new byte[] { 9, 9 }, got);
        }

        [Fact]
        public void HandlePacket_Unreliable_EmptyIsKeepalive_NotQueued()
        {
            var (t, _, _) = NewCore();
            t.HandlePacket(UdpProto.EncodeUnreliable(0xCAFEBABE, null));
            Assert.False(t.TryRecv(out _, 0));
        }

        [Fact]
        public void Tick_RetransmitsUnackedAfterInterval()
        {
            var (t, sent, clock) = NewCore();
            t.SendReliable(new byte[] { 7 });
            sent.Clear();
            clock[0] = 150; // > retransmitInterval (100ms)
            t.Tick();
            Assert.Single(sent); // retransmitted
            Assert.Equal(UdpProto.TypeReliable, sent[0][0]);
        }

        [Fact]
        public void Ack_StopsRetransmit()
        {
            var (t, sent, clock) = NewCore();
            t.SendReliable(new byte[] { 7 }); // seq 0
            sent.Clear();
            // ACK seq 0, no prior bits.
            t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, 0, 0));
            clock[0] = 150;
            t.Tick();
            Assert.Empty(sent); // acked → not retransmitted (ackDirty=false here too)
        }

        [Fact]
        public void Tick_SendsAck_WhenInboundReliableReceived()
        {
            var (t, sent, clock) = NewCore();
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 0, new byte[] { 1 }));
            sent.Clear();
            clock[0] = 100;
            t.Tick();
            // First sent packet should include an ACK for the received reliable.
            Assert.Contains(sent, p => p[0] == UdpProto.TypeAck);
        }

        [Fact]
        public void Tick_KeepaliveWhenIdle()
        {
            var (t, sent, clock) = NewCore();
            clock[0] = 1100; // > keepaliveInterval (1000ms), nothing sent yet
            t.Tick();
            Assert.Contains(sent, p => p[0] == UdpProto.TypeUnreliable && p.Length == UdpProto.UnreliableHeaderSize);
        }
    }
}
```

- [ ] **Step 2: Verify failure**

Run: `cd csharp && dotnet test --filter UdpTransportCoreTests 2>&1 | tail -12`
Expected: BUILD FAILS — `UdpTransport` doesn't exist.

- [ ] **Step 3: Create the protocol core** `csharp/Mmokit.Sdk.Core/UdpTransport.cs`:

```csharp
using System;
using System.Collections.Concurrent;
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// UDP transport: handshake + reliable/unreliable channels + ACK-based
    /// reliability, port of pkg/net/udpclient/client.go.
    ///
    /// This file has two faces:
    ///  - The protocol CORE (this ctor + SendReliable/SendUnreliable/
    ///    HandlePacket/Tick/TryRecv/Recv/Close): a pure state machine driven
    ///    by an injected sendRaw sink + nowMs clock. No sockets, no threads —
    ///    unit-testable directly.
    ///  - The real-socket Connect factory + receive/tick loops (added in the
    ///    socket section): wires sendRaw to a UdpClient and pumps HandlePacket.
    ///
    /// ENCRYPTION SEAM (spec §B): the only two byte chokepoints are the
    /// `_sendRaw` delegate (outbound) and the `HandlePacket(byte[])` entry
    /// (inbound). A future encryption layer wraps ONLY these two points; all
    /// logic here operates on decrypted bytes. Keep it that way.
    public sealed partial class UdpTransport
    {
        const int ReliableBufSize = 256;
        const long RetransmitIntervalMs = 100;
        const long ReliableTimeoutMs = 5000;
        const long KeepaliveIntervalMs = 1000;
        const long ConnectionTimeoutMs = 10000;

        struct ReliableEntry { public byte[] Payload; public long SentAtMs; public bool Acked; public bool Used; }

        readonly Action<byte[]> _sendRaw;
        readonly Func<long> _nowMs;
        readonly uint _token;

        readonly object _sendLock = new();
        ushort _sendSeq;
        readonly ReliableEntry[] _sendBuf = new ReliableEntry[ReliableBufSize];

        ushort _recvSeq;
        uint _recvBits;
        bool _ackDirty;

        readonly BlockingCollection<byte[]> _inbound = new(new ConcurrentQueue<byte[]>());

        long _lastRecvMs;
        long _lastSendMs;
        volatile bool _closed;

        /// Core ctor (and the testing entry point). The socket factory below
        /// constructs this with a sink that writes to the UdpClient.
        public UdpTransport(Action<byte[]> sendRaw, Func<long> nowMs, uint token)
        {
            _sendRaw = sendRaw;
            _nowMs = nowMs;
            _token = token;
            _lastRecvMs = nowMs();
            _lastSendMs = nowMs();
        }

        public uint Token => _token;

        public void SendReliable(byte[] data)
        {
            ushort seq;
            byte[] payload = (byte[])data.Clone();
            lock (_sendLock)
            {
                seq = _sendSeq;
                _sendSeq++;
                int idx = seq % ReliableBufSize;
                _sendBuf[idx] = new ReliableEntry { Payload = payload, SentAtMs = _nowMs(), Acked = false, Used = true };
            }
            _sendRaw(UdpProto.EncodeReliable(_token, seq, payload));
            _lastSendMs = _nowMs();
        }

        public void SendUnreliable(byte[] data)
        {
            _sendRaw(UdpProto.EncodeUnreliable(_token, data));
            _lastSendMs = _nowMs();
        }

        /// Inbound chokepoint. `data` is a full, decrypted packet.
        public void HandlePacket(byte[] data)
        {
            if (data.Length == 0) return;
            switch (data[0])
            {
                case UdpProto.TypeUnreliable:
                    if (!UdpProto.TryDecodeUnreliable(data, out _, out byte[] upay)) return;
                    _lastRecvMs = _nowMs();
                    if (upay.Length == 0) return; // keepalive
                    _inbound.Add(upay);
                    break;
                case UdpProto.TypeReliable:
                    if (!UdpProto.TryDecodeReliable(data, out _, out ushort seq, out byte[] rpay)) return;
                    _lastRecvMs = _nowMs();
                    UpdateRecvTracking(seq);
                    if (rpay.Length > 0) _inbound.Add(rpay);
                    break;
                case UdpProto.TypeAck:
                    if (!UdpProto.TryDecodeAck(data, out _, out ushort ackSeq, out uint ackBits)) return;
                    _lastRecvMs = _nowMs();
                    ProcessAck(ackSeq, ackBits);
                    break;
            }
        }

        void UpdateRecvTracking(ushort seq)
        {
            if (_recvSeq == 0 && !_ackDirty)
            {
                _recvSeq = seq;
                _recvBits = 0;
            }
            else if (UdpProto.SeqGreaterThan(seq, _recvSeq))
            {
                int diff = (ushort)(seq - _recvSeq);
                if (diff <= 32)
                {
                    // C# masks shift counts to 5 bits (x << 32 == x << 0), but Go
                    // yields 0 for a shift >= width. At diff==32 we must force 0 to
                    // match the Go reference, hence the explicit guard.
                    uint shifted = diff >= 32 ? 0u : (_recvBits << diff);
                    _recvBits = shifted | (1u << (diff - 1));
                }
                else _recvBits = 0;
                _recvSeq = seq;
            }
            else
            {
                int diff = (ushort)(_recvSeq - seq);
                if (diff > 0 && diff <= 32) _recvBits |= 1u << (diff - 1);
            }
            _ackDirty = true;
        }

        void ProcessAck(ushort ackSeq, uint ackBits)
        {
            lock (_sendLock)
            {
                int idx = ackSeq % ReliableBufSize;
                if (_sendBuf[idx].Used) _sendBuf[idx].Acked = true;
                for (int i = 0; i < 32; i++)
                {
                    if ((ackBits & (1u << i)) != 0)
                    {
                        ushort s = (ushort)(ackSeq - i - 1);
                        int j = s % ReliableBufSize;
                        if (_sendBuf[j].Used) _sendBuf[j].Acked = true;
                    }
                }
            }
        }

        /// Drive periodic work: timeout, retransmit, ACK flush, keepalive.
        /// Returns false if the connection timed out (caller should close).
        public bool Tick()
        {
            long now = _nowMs();
            if (now - _lastRecvMs > ConnectionTimeoutMs) { Close(); return false; }

            lock (_sendLock)
            {
                for (int i = 0; i < _sendBuf.Length; i++)
                {
                    if (!_sendBuf[i].Used || _sendBuf[i].Acked) continue;
                    long age = now - _sendBuf[i].SentAtMs;
                    if (age > ReliableTimeoutMs) { Close(); return false; }
                    if (age >= RetransmitIntervalMs)
                    {
                        // NOTE: reconstructs seq from the buffer index — faithful
                        // to the Go reference; correct only for the first
                        // ReliableBufSize reliable sends (known upstream limitation).
                        ushort seq = (ushort)i;
                        _sendRaw(UdpProto.EncodeReliable(_token, seq, _sendBuf[i].Payload));
                        _sendBuf[i].SentAtMs = now;
                    }
                }
            }

            if (_ackDirty)
            {
                _sendRaw(UdpProto.EncodeAck(_token, _recvSeq, _recvBits));
                _ackDirty = false;
            }

            if (now - _lastSendMs > KeepaliveIntervalMs)
            {
                _sendRaw(UdpProto.EncodeUnreliable(_token, null));
                _lastSendMs = now;
            }
            return true;
        }

        /// Non-blocking receive (used by tests + pollers). timeoutMs=0 → immediate.
        public bool TryRecv(out byte[] msg, int timeoutMs)
            => _inbound.TryTake(out msg!, timeoutMs);

        /// Blocking receive. Returns null when the transport is closed.
        public byte[]? Recv()
        {
            try { return _inbound.Take(); }
            catch (InvalidOperationException) { return null; } // CompleteAdding called
        }

        public void Close()
        {
            if (_closed) return;
            _closed = true;
            try { _sendRaw(UdpProto.EncodeDisconnect(_token)); } catch { /* best effort */ }
            _inbound.CompleteAdding();
            CloseSocket(); // partial-class hook; no-op for the core-only ctor
        }

        // Implemented in the socket section; the core ctor leaves it a no-op.
        partial void CloseSocket();
    }
}
```

- [ ] **Step 4: Run the core tests**

Run: `cd csharp && dotnet test --filter UdpTransportCoreTests 2>&1 | tail -12`
Expected: `Passed!  - Failed: 0, Passed: 7`.

- [ ] **Step 5: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/UdpTransport.cs csharp/Mmokit.Sdk.Core.Tests/UdpTransportCoreTests.cs
git commit -m "feat(csharp): UdpTransport protocol core (reliability state machine)

Port of pkg/net/udpclient reliability: seq-tracked reliable sends, ACK
bitfield recv-tracking, retransmit/keepalive/timeout via injected sendRaw
sink + clock — hermetically unit-tested (no sockets/threads). Encryption
seam = sendRaw (out) + HandlePacket (in) chokepoints only.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Real-socket Connect + receive/tick loops + loopback test

**Files:**
- Create: `csharp/Mmokit.Sdk.Core/UdpTransport.Socket.cs` (the `partial` socket half)
- Create: `csharp/Mmokit.Sdk.Core.Tests/UdpTransportLoopbackTests.cs`

- [ ] **Step 1: Create the socket half** `csharp/Mmokit.Sdk.Core/UdpTransport.Socket.cs`:

```csharp
using System;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Threading;
using System.Threading.Tasks;

namespace Mmokit.Sdk.Core
{
    /// Real-socket half of UdpTransport: the handshake factory + the receive
    /// and tick loops over a System.Net.Sockets.UdpClient. The protocol logic
    /// lives in the core (UdpTransport.cs); this is the I/O shell.
    public sealed partial class UdpTransport
    {
        UdpClient? _socket;
        CancellationTokenSource? _cts;

        /// Connect to host:port: perform the ConnReq/ConnAccept handshake, then
        /// start the receive + tick loops. handshakeTimeoutMs default 5000.
        public static UdpTransport Connect(string host, int port, int handshakeTimeoutMs = 5000)
        {
            var socket = new UdpClient();
            socket.Connect(host, port);

            // Client salt (8 random bytes, little-endian u64 — matches Go Dial).
            Span<byte> saltBuf = stackalloc byte[8];
            RandomNumberGenerator.Fill(saltBuf);
            ulong clientSalt = 0;
            for (int i = 0; i < 8; i++) clientSalt |= (ulong)saltBuf[i] << (8 * i);

            socket.Send(UdpProto.EncodeConnReq(clientSalt));

            socket.Client.ReceiveTimeout = handshakeTimeoutMs;
            IPEndPoint? remote = null;
            byte[] resp;
            try { resp = socket.Receive(ref remote); }
            catch (SocketException ex) { socket.Dispose(); throw new TimeoutException("UDP handshake timed out", ex); }
            socket.Client.ReceiveTimeout = 0;

            if (resp.Length == 0 || resp[0] != UdpProto.TypeConnAccept)
            { socket.Dispose(); throw new InvalidOperationException("unexpected handshake response"); }
            if (!UdpProto.TryDecodeConnAccept(resp, out ulong echoedClientSalt, out ulong serverSalt) || echoedClientSalt != clientSalt)
            { socket.Dispose(); throw new InvalidOperationException("handshake salt mismatch"); }

            uint token = UdpProto.MakeToken(clientSalt, serverSalt);

            // Monotonic-ish clock in ms (Environment.TickCount64 is fine here).
            var t = new UdpTransport(raw => { try { socket.Send(raw); } catch { /* closed */ } },
                                     () => Environment.TickCount64, token);
            t.AttachSocket(socket);
            return t;
        }

        void AttachSocket(UdpClient socket)
        {
            _socket = socket;
            _cts = new CancellationTokenSource();
            _ = Task.Run(() => ReceiveLoop(_cts.Token));
            _ = Task.Run(() => TickLoop(_cts.Token));
        }

        void ReceiveLoop(CancellationToken ct)
        {
            IPEndPoint? remote = null;
            while (!ct.IsCancellationRequested)
            {
                byte[] data;
                try { data = _socket!.Receive(ref remote); }
                catch { return; } // socket closed
                if (data.Length > 0) HandlePacket(data); // inbound chokepoint
            }
        }

        void TickLoop(CancellationToken ct)
        {
            while (!ct.IsCancellationRequested)
            {
                try { Thread.Sleep((int)RetransmitIntervalMs); } catch { return; }
                if (ct.IsCancellationRequested) return;
                if (!Tick()) return; // timed out → Tick already closed
            }
        }

        // partial hook called from core Close().
        partial void CloseSocket()
        {
            try { _cts?.Cancel(); } catch { }
            try { _socket?.Dispose(); } catch { }
        }
    }
}
```

- [ ] **Step 2: Write the loopback integration test** `csharp/Mmokit.Sdk.Core.Tests/UdpTransportLoopbackTests.cs`. The test plays the SERVER role on a localhost UDP socket: it answers the handshake and echoes a reliable message back, validating the C# transport's real socket + handshake + reliable round-trip.

```csharp
using System;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpTransportLoopbackTests
    {
        [Fact]
        public void Connect_Handshake_And_ReliableRoundTrip()
        {
            // Minimal in-test UDP "server": bind a loopback port, answer ConnReq
            // with ConnAccept, then on a reliable packet send back an unreliable
            // payload the client can Recv().
            using var server = new UdpClient(new IPEndPoint(IPAddress.Loopback, 0));
            int port = ((IPEndPoint)server.Client.LocalEndPoint!).Port;
            uint serverToken = 0;
            var serverDone = new CancellationTokenSource();

            var serverTask = Task.Run(() =>
            {
                IPEndPoint? client = null;
                // 1) handshake
                byte[] req = server.Receive(ref client);
                Assert.Equal(UdpProto.TypeConnReq, req[0]);
                Assert.True(UdpProto.TryDecodeConnReq(req, out ulong cs));
                ulong ss = 0xABCDEF0123456789;
                serverToken = UdpProto.MakeToken(cs, ss);
                server.Send(UdpProto.EncodeConnAccept(cs, ss), client!);
                // 2) await a reliable packet, echo a reply as unreliable
                while (!serverDone.IsCancellationRequested)
                {
                    server.Client.ReceiveTimeout = 2000;
                    byte[] pkt;
                    try { pkt = server.Receive(ref client); }
                    catch (SocketException) { return; }
                    if (pkt[0] == UdpProto.TypeReliable &&
                        UdpProto.TryDecodeReliable(pkt, out _, out ushort seq, out byte[] payload) &&
                        payload.Length > 0)
                    {
                        // ack it, then send a server→client reply.
                        server.Send(UdpProto.EncodeAck(serverToken, seq, 0), client!);
                        server.Send(UdpProto.EncodeUnreliable(serverToken, new byte[] { 42, 43 }), client!);
                        return;
                    }
                }
            });

            UdpTransport client2 = UdpTransport.Connect("127.0.0.1", port);
            try
            {
                client2.SendReliable(new byte[] { 1, 2, 3 });
                Assert.True(client2.TryRecv(out byte[] reply, 3000), "expected a server reply within 3s");
                Assert.Equal(new byte[] { 42, 43 }, reply);
            }
            finally
            {
                serverDone.Cancel();
                client2.Close();
                serverTask.Wait(2000);
            }
        }
    }
}
```

- [ ] **Step 3: Run the loopback test**

Run: `cd csharp && dotnet test --filter UdpTransportLoopbackTests 2>&1 | tail -12`
Expected: `Passed!  - Failed: 0, Passed: 1`. (If it flakes on timing, re-run once; the 3s/2s timeouts are generous for loopback.)

- [ ] **Step 4: Run the full suite**

Run: `cd csharp && dotnet test 2>&1 | tail -8`
Expected: all pass (prior 14 + 4 UdpProto golden + 7 core + 1 loopback = 26).

- [ ] **Step 5: Commit**

```bash
git add csharp/Mmokit.Sdk.Core/UdpTransport.Socket.cs csharp/Mmokit.Sdk.Core.Tests/UdpTransportLoopbackTests.cs
git commit -m "feat(csharp): UdpTransport real-socket Connect + receive/tick loops

Handshake factory over System.Net.Sockets.UdpClient + background receive
and tick loops feeding the protocol core. Localhost loopback test exercises
the real socket handshake + reliable round-trip end-to-end.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage (§C UdpTransport.cs, §B encryption seam, §F.3 golden):** little-endian codec `UdpProto.cs` golden-verified (Task 1); reliability state machine hermetically tested (Task 2); real-socket handshake + loops with loopback round-trip (Task 3); encryption seam confined to `_sendRaw` + `HandlePacket` and documented in code (Task 2/3). System.Net.Sockets per the (now superseded) WS answer → the UDP decision. ✅
- **Placeholder scan:** Complete code in every step; the only generated artifact is the regenerated `delta_golden.json` (now with a `udp` section) produced by the fully-specified generator extension. ✅
- **Type/name consistency:** C# `UdpProto` method names used in golden tests + `UdpTransport` (`EncodeReliable/TryDecodeReliable/EncodeAck/...`, `MakeToken`, `SeqGreaterThan`) match the codec definitions. Golden DTOs (`UdpCases/TokenCase/PacketCase/SeqCase`) match the Go generator's JSON tags (case-insensitive). `UdpTransport` core ctor `(Action<byte[]>, Func<long>, uint)` matches both the tests' `NewCore` and the socket factory's construction. `partial` `CloseSocket()` declared in core, implemented in the socket half. ✅
- **Faithfulness:** reliability/ACK/retransmit logic mirrors `udpclient/client.go`; the `seq=(ushort)i` retransmit limitation is replicated and documented, not silently "fixed". ✅

## Open items / known limitations (not blocking)

- The retransmit `seq` reconstruction matches the Go reference's first-256-messages limitation. A proper fix (store seq in the entry) should land in BOTH Go and C# in a separate change — out of scope for a faithful port.
- The loopback test validates C#↔C# at the socket level; the real C#↔Go-server end-to-end (login over the op channel, world frames) is the smoke test in the deploy plan (Plan 6).
- `BlockingCollection`/`Task.Run` receive loop is the straightforward translation of the Go goroutines; if Unity's runtime needs a different threading model (e.g. no `Task.Run` on WebGL), that's addressed when the SDK is consumed there — netstandard2.1 source compiles regardless.
