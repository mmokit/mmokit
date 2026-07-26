package main

import (
	"strings"
	"testing"
)

func TestCsharpGeneratedDecoderFencesFrameStreamAndOrder(t *testing.T) {
	decoder := (csharpBackend{namespace: "Mmokit.Sdk"}).genDeltaDecoder(sampleEntitySchema())

	for _, want := range []string{
		"uint? _streamEpoch;",
		"uint? _lastFrameSequence;",
		"bool AcceptFrame(uint? streamEpoch, uint sequence, out bool streamChanged)",
		"if (!streamEpoch.HasValue) return true;",
		"if (!IsNewerSerial(candidateEpoch, _streamEpoch.Value)) return false;",
		"if (_lastFrameSequence.HasValue && !IsNewerSerial(sequence, _lastFrameSequence.Value)) return false;",
		"public DeltaWorldUpdate? Decode(byte[] data, uint? streamEpoch = null)",
		"if (!AcceptFrame(streamEpoch, header.Seq, out bool streamChanged)) return null;",
		"StreamChanged = streamChanged",
		"_streamEpoch = null; _lastFrameSequence = null;",
		"HashSet<uint>? freshAuthorityIDs = freshSnapshot && _streamEpoch.HasValue ? new HashSet<uint>() : null;",
		"freshAuthorityIDs?.Add(entry.NetID);",
		"foreach (var id in removed) _authorityEpochs.Remove(id);",
		"if (!freshAuthorityIDs.Contains(id)) staleAuthorityIDs.Add(id);",
	} {
		if !strings.Contains(decoder, want) {
			t.Fatalf("generated C# decoder is missing frame-stream safeguard %q:\n%s", want, decoder)
		}
	}

	accept := strings.Index(decoder, "if (!AcceptFrame(streamEpoch, header.Seq, out bool streamChanged)) return null;")
	freshClear := strings.Index(decoder, "if (freshSnapshot) _baselines.Clear();")
	if accept < 0 || freshClear < 0 || accept > freshClear {
		t.Fatalf("generated C# decoder must reject stale frames before a FreshSnapshot can clear baselines:\n%s", decoder)
	}
}
