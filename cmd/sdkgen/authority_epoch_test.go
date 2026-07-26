package main

import (
	"strings"
	"testing"
)

func TestTSGeneratedDecoderFencesAuthorityEpochs(t *testing.T) {
	g := &Generator{schema: sampleEntitySchema()}

	entities := g.genEntities()
	if !strings.Contains(entities, "authorityEpoch: number;") {
		t.Fatalf("generated TS entity is missing authorityEpoch:\n%s", entities)
	}

	decoder := g.genDeltaDecoder()
	for _, want := range []string{
		"return { netID: 0, authorityEpoch: 0, producedAtMs: 0, entityType:",
		"const distance = (candidate - current) >>> 0;",
		"distance !== 0 && distance < 0x80000000",
		"BaselineStore<{ epoch: number; type: number; lastEntity?: AnyEntity }>",
		"private authorityEpochs = new Map<number, number>();",
		"private streamEpoch: number | null = null;",
		"private lastFrameSequence: number | null = null;",
		"decode(data: Uint8Array, streamEpoch?: number): DeltaWorldUpdate | null",
		"const frameOrder = this.acceptFrame(streamEpoch, header.seq);",
		"if (!frameOrder.accepted) return null;",
		"if (!isNewerAuthorityEpoch(epoch, this.streamEpoch)) return { accepted: false, streamChanged: false };",
		"this.baselines.clear();\n      this.authorityEpochs.clear();",
		"!isNewerFrameSequence(seq, this.lastFrameSequence)",
		"this.streamEpoch = null;",
		"this.lastFrameSequence = null;",
		"entry.epoch !== highestEpoch && !isNewerAuthorityEpoch(entry.epoch, highestEpoch)",
		"this.authorityEpochs.set(entry.netID, entry.epoch)",
		"prevMeta?.epoch === entry.epoch && prevMeta.type === entry.entityType ? prevMeta.lastEntity : undefined",
		"!bl?.meta || bl.meta.epoch !== entry.epoch || bl.meta.type !== entry.entityType",
		"{ epoch: entry.epoch, type: entry.entityType, lastEntity: entity ?? undefined }",
		"e.authorityEpoch = authorityEpoch;",
		"for (const id of removed) this.authorityEpochs.delete(id);",
		"const freshAuthorityIDs = freshSnapshot && this.streamEpoch !== null ? new Set<number>() : null;",
		"freshAuthorityIDs?.add(entry.netID);",
		"if (!freshAuthorityIDs.has(id)) this.authorityEpochs.delete(id);",
	} {
		if !strings.Contains(decoder, want) {
			t.Fatalf("generated TS decoder is missing authority-epoch safeguard %q:\n%s", want, decoder)
		}
	}
	accept := strings.Index(decoder, "if (!frameOrder.accepted) return null;")
	freshClear := strings.Index(decoder, "if (freshSnapshot) {\n      this.baselines.clear();")
	if accept < 0 || freshClear < 0 || accept > freshClear {
		t.Fatalf("generated TS decoder must reject stale frames before FreshSnapshot clears baselines:\n%s", decoder)
	}
}

func TestCsharpGeneratedDecoderFencesAuthorityEpochs(t *testing.T) {
	b := csharpBackend{namespace: "Mmokit.Sdk"}

	entities := b.genEntities(sampleEntitySchema())
	if !strings.Contains(entities, "public uint AuthorityEpoch;") {
		t.Fatalf("generated C# EntityBase is missing AuthorityEpoch:\n%s", entities)
	}

	decoder := b.genDeltaDecoder(sampleEntitySchema())
	for _, want := range []string{
		"sealed class BaselineMeta { public uint Epoch; public byte Type; public EntityBase? LastEntity; }",
		"uint distance = unchecked(candidate - current);",
		"distance != 0u && distance < 0x80000000u",
		"readonly Dictionary<uint, uint> _authorityEpochs = new();",
		"_baselines.Clear(); _authorityEpochs.Clear();",
		"entry.Epoch != highestEpoch && !IsNewerAuthorityEpoch(entry.Epoch, highestEpoch)",
		"_authorityEpochs[entry.NetID] = entry.Epoch",
		"_baselines.Clear();\n                // A new stream generation supersedes all entity-authority",
		"_authorityEpochs.Clear();\n                streamChanged = true;\n                return true;",
		"prevMeta.Epoch == entry.Epoch && prevMeta.Type == entry.EntityType ? prevMeta.LastEntity : null",
		"meta == null || meta.Epoch != entry.Epoch || meta.Type != entry.EntityType",
		"new BaselineMeta { Epoch = entry.Epoch, Type = entry.EntityType, LastEntity = entity }",
		"e.AuthorityEpoch = authorityEpoch;",
	} {
		if !strings.Contains(decoder, want) {
			t.Fatalf("generated C# decoder is missing authority-epoch safeguard %q:\n%s", want, decoder)
		}
	}
}
