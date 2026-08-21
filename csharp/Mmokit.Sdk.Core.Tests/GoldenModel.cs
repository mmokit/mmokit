using System;
using System.IO;
using System.Text.Json;

namespace Mmokit.Sdk.Core.Tests
{
    public class Manifest
    {
        public DequantCase[] Dequant { get; set; } = Array.Empty<DequantCase>();
        public FrameCase Frame { get; set; } = new();
        public ApplyCase[] ApplyDelta { get; set; } = Array.Empty<ApplyCase>();
        public StringCase[] Strings { get; set; } = Array.Empty<StringCase>();
        public UdpCases Udp { get; set; } = new();
        public ReflectCase Reflect { get; set; } = new();
        public ReflectNestedCase ReflectNested { get; set; } = new();
        public ClockSyncCase ClockSync { get; set; } = new();
        public FrameCase InputAckFrame { get; set; } = new();
        public PlaybackCase Playback { get; set; } = new();
        public PredictionCase Prediction { get; set; } = new();
        public QuatCase[] Quat { get; set; } = Array.Empty<QuatCase>();
        public SlerpCase[] Slerp { get; set; } = Array.Empty<SlerpCase>();
    }

    /// Cross-language pin for AdaptivePlaybackController. Every step feeds one
    /// frame observation and optionally samples renderTime.
    public class PlaybackCase
    {
        public double TickIntervalMs { get; set; }
        public double MinDelayMs { get; set; }
        public double MaxDelayMs { get; set; }
        public double MinPlaybackRate { get; set; }
        public double MaxPlaybackRate { get; set; }
        public double ConvergenceWindowMs { get; set; }
        public double AttackFactor { get; set; }
        public double DecayFactor { get; set; }
        public double JitterFactor { get; set; }
        public PlaybackStep[] Steps { get; set; } = Array.Empty<PlaybackStep>();
    }

    public class PlaybackStep
    {
        public string Note { get; set; } = "";
        public uint Seq { get; set; }
        public bool FreshSnapshot { get; set; }
        public bool HasStreamChanged { get; set; }
        public bool StreamChanged { get; set; }
        public double ArrivalTimeMs { get; set; }
        public bool HasProducedAt { get; set; }
        public double ProducedAtMs { get; set; }

        public double ExpectedTargetDelayMs { get; set; }
        public double ExpectedJitterMs { get; set; }
        public double ExpectedExcessDelayMs { get; set; }
        public double ExpectedLossRate { get; set; }
        public int ExpectedReceivedFrames { get; set; }
        public int ExpectedLostFrames { get; set; }
        public int ExpectedDuplicateFrames { get; set; }
        public int ExpectedOutOfOrderFrames { get; set; }

        public bool HasRender { get; set; }
        public double RenderClientNowMs { get; set; }
        public bool ExpectedRenderNull { get; set; }
        public double ExpectedRenderTimeMs { get; set; }
        public double ExpectedPlaybackRate { get; set; }
        public double ExpectedCurrentDelayMs { get; set; }
    }

    /// Cross-language pin for PredictionBuffer, including the uint32 wrap and
    /// capacity overflow.
    /// <summary>
    /// One smallest-three quaternion decode. Bits carries the exact float32
    /// bit patterns of the reference output, so a port is compared on exact
    /// identity rather than a tolerance — a tolerance would hide precisely
    /// the rounding disagreement this corpus exists to catch.
    /// </summary>
    public class QuatCase
    {
        public string Name { get; set; } = "";
        /// <summary>The 7 wire bytes, big-endian, as the server emits them.</summary>
        public string Hex { get; set; } = "";
        /// <summary>The same value as a 56-bit integer. Documentation only.</summary>
        public ulong Packed { get; set; }
        public double X { get; set; }
        public double Y { get; set; }
        public double Z { get; set; }
        public double W { get; set; }
        public uint[] Bits { get; set; } = Array.Empty<uint>();
    }

    /// <summary>One orientation interpolation; a and b are {x,y,z,w}.</summary>
    public class SlerpCase
    {
        public string Name { get; set; } = "";
        public double[] A { get; set; } = Array.Empty<double>();
        public double[] B { get; set; } = Array.Empty<double>();
        public double T { get; set; }
        public double[] Out { get; set; } = Array.Empty<double>();
    }

    public class PredictionCase
    {
        public int MaxPending { get; set; }
        public PredictionStep[] Steps { get; set; } = Array.Empty<PredictionStep>();
    }

    public class PredictionStep
    {
        public string Note { get; set; } = "";
        public string Op { get; set; } = "";
        public uint Seq { get; set; }
        public int Input { get; set; }
        public bool HasSeq { get; set; }
        public int State { get; set; }

        public bool ExpectedAccepted { get; set; }
        public bool ExpectedDropped { get; set; }
        public uint ExpectedDroppedSeq { get; set; }
        public bool ExpectedAdvanced { get; set; }
        public int ExpectedAcknowledgedCount { get; set; }
        public int ExpectedPendingCount { get; set; }
        public int ExpectedOverflowCount { get; set; }
        public int ExpectedState { get; set; }
        public bool ExpectedHasLastAck { get; set; }
        public uint ExpectedLastAck { get; set; }
    }

    public class ClockSyncCase
    {
        public int Window { get; set; }
        public ClockSyncObs[] Observations { get; set; } = Array.Empty<ClockSyncObs>();
    }

    public class ClockSyncObs
    {
        public double ServerMs { get; set; }
        public double ClientNowMs { get; set; }
        public double ExpectedOffsetMs { get; set; }
    }

    public class ReflectCase
    {
        public string HexBytes { get; set; } = "";
        public float A { get; set; }
        public uint B { get; set; }
        public string C { get; set; } = "";
        public bool D { get; set; }
        public long E { get; set; }
        public uint[] F { get; set; } = Array.Empty<uint>();
    }

    public class ReflectNestedCase
    {
        public string HexBytes { get; set; } = "";
        public NestedChannel Channel { get; set; } = new();
        public NestedMember[] Members { get; set; } = Array.Empty<NestedMember>();
        public uint Tick { get; set; }
    }

    public class NestedChannel
    {
        public string Slug { get; set; } = "";
        public int MemberCount { get; set; }
    }

    public class NestedMember
    {
        public string UserID { get; set; } = "";
        public string Role { get; set; } = "";
    }

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

    public class DequantCase { public string Kind { get; set; } = ""; public long Q { get; set; } public double Scale { get; set; } public double Expected { get; set; } }
    public class FrameCase
    {
        public string HexBytes { get; set; } = "";
        public uint Tick { get; set; } public uint Seq { get; set; } public uint Flags { get; set; }
        public ushort FullCount { get; set; } public ushort DeltaCount { get; set; }
        public ushort RemovedCount { get; set; } public ushort ExitedCount { get; set; }
        public FullEntry[] Full { get; set; } = Array.Empty<FullEntry>();
        public DeltaEntry[] Delta { get; set; } = Array.Empty<DeltaEntry>();
        public uint[] RemovedIDs { get; set; } = Array.Empty<uint>();
        public uint[] ExitedIDs { get; set; } = Array.Empty<uint>();
        public bool HasInputAck { get; set; }
        public uint ExpectedInputAck { get; set; }
    }
    public class FullEntry { public uint NetID { get; set; } public uint Epoch { get; set; } public byte EntityType { get; set; } public ulong ProducedAtMs { get; set; } public string SnapshotHex { get; set; } = ""; public string InitialHex { get; set; } = ""; }
    public class DeltaEntry { public uint NetID { get; set; } public uint Epoch { get; set; } public byte EntityType { get; set; } public ulong ProducedAtMs { get; set; } public string DeltaHex { get; set; } = ""; }
    public class ApplyCase { public int[] FieldSizes { get; set; } = Array.Empty<int>(); public bool HasVarTail { get; set; } public string BaselineHex { get; set; } = ""; public string DeltaHex { get; set; } = ""; public string ExpectedHex { get; set; } = ""; }
    public class StringCase { public string Kind { get; set; } = ""; public string HexBytes { get; set; } = ""; public string Expected { get; set; } = ""; }

    public static class Golden
    {
        public static Manifest Load()
        {
            string path = Path.Combine(AppContext.BaseDirectory, "testdata", "delta_golden.json");
            string json = File.ReadAllText(path);
            var opts = new JsonSerializerOptions { PropertyNameCaseInsensitive = true };
            return JsonSerializer.Deserialize<Manifest>(json, opts)!;
        }

        public static byte[] Hex(string s)
        {
            if (string.IsNullOrEmpty(s)) return Array.Empty<byte>();
            var b = new byte[s.Length / 2];
            for (int i = 0; i < b.Length; i++)
                b[i] = Convert.ToByte(s.Substring(i * 2, 2), 16);
            return b;
        }
    }
}
