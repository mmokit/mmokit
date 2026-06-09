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
