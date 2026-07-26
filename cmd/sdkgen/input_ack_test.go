package main

import (
	"strings"
	"testing"
)

func TestGeneratedDeltaDecoderExposesProcessedInputSequence(t *testing.T) {
	schema := sampleEntitySchema()
	tsEntities := (&Generator{schema: schema}).genEntities()
	tsDecoder := (&Generator{schema: schema}).genDeltaDecoder()
	for _, want := range []string{
		"processedInputSeq: number | null;",
		"decodeInputAck(data, pos3, header.flags)",
		"streamChanged: frameOrder.streamChanged, processedInputSeq",
	} {
		if !strings.Contains(tsEntities+tsDecoder, want) {
			t.Fatalf("generated TypeScript missing %q", want)
		}
	}

	cs := csharpBackend{namespace: "Mmokit.Sdk"}
	csEntities := cs.genEntities(schema)
	csDecoder := cs.genDeltaDecoder(schema)
	for _, want := range []string{
		"public uint? ProcessedInputSeq;",
		"DeltaDecoderCore.DecodeInputAck(data, pos3, header.Flags)",
		"ProcessedInputSeq = processedInputSeq",
	} {
		if !strings.Contains(csEntities+csDecoder, want) {
			t.Fatalf("generated C# missing %q", want)
		}
	}
}
