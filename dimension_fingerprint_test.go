package mmokit

import (
	"encoding/json"
	"testing"
)

// fingerprintFor assembles a process's protocol and returns both the hash and
// the schema. It deliberately avoids Build(): the value is reachable with no
// listeners and no database.
func fingerprintFor(t *testing.T, d Dimension, register func(*Process)) (uint32, ProtocolSchema) {
	t.Helper()
	proc := newDimensionTestProcess(t, d)
	if register != nil {
		register(proc)
	}
	p := NewProtocol(proc, "fingerprint-test")
	proc.SetProtocol(p)
	p.AssembleFromProcess(proc)
	return p.SchemaFingerprint(), p.Schema()
}

// TestSchemaFingerprint_RotatesOnDimension_NoKinds is the load-bearing half of
// cluster dimension agreement, and it deliberately registers NO entity kinds.
//
// With kinds registered the fingerprints differ for two independent reasons —
// the hashed Dimension string AND the entity layouts, which are 7 fields in 2D
// and 11 in 3D. A test that registered kinds would therefore keep passing if a
// refactor dropped Dimension from the hash entirely, while the property it
// exists to protect was gone. With zero kinds, the dimension string is the
// only thing that can differ.
func TestSchemaFingerprint_RotatesOnDimension_NoKinds(t *testing.T) {
	fp2d, s2d := fingerprintFor(t, Dimension2D, nil)
	fp3d, s3d := fingerprintFor(t, Dimension3D, nil)

	// Guard the premise: if a helper silently handed back two 2D processes,
	// or a kind leaked in, the inequality below would mean nothing.
	if s2d.Dimension != "2d" || s3d.Dimension != "3d" {
		t.Fatalf("schemas report dimensions %q and %q, want 2d and 3d", s2d.Dimension, s3d.Dimension)
	}
	if len(s2d.Entities) != 0 || len(s3d.Entities) != 0 {
		t.Fatalf("expected no entity kinds, got %d and %d", len(s2d.Entities), len(s3d.Entities))
	}

	if fp2d == fp3d {
		t.Fatalf("2D and 3D fingerprint identically (%08x) with no kinds registered — "+
			"ProtocolSchema.Dimension is no longer hashed, so a 2D peer can join a 3D cluster", fp2d)
	}

	// Upgrade "they differ" to "they differ ONLY because of the dimension":
	// zero the two fields that legitimately vary and require the rest to be
	// byte-identical. Without this the test would pass against two processes
	// that differed in anything at all.
	s2d.Dimension, s3d.Dimension = "", ""
	s2d.Fingerprint, s3d.Fingerprint = "", ""
	a, err := json.Marshal(s2d)
	if err != nil {
		t.Fatalf("marshal 2d: %v", err)
	}
	b, err := json.Marshal(s3d)
	if err != nil {
		t.Fatalf("marshal 3d: %v", err)
	}
	if string(a) != string(b) {
		t.Fatalf("schemas differ beyond the dimension, so the rotation is not attributable to it:\n2d: %s\n3d: %s", a, b)
	}
}

// TestSchemaFingerprint_RotatesOnDimension_WithKinds is the realistic case: a
// process that actually registers a kind, where the entity layout differs too.
func TestSchemaFingerprint_RotatesOnDimension_WithKinds(t *testing.T) {
	reg := func(p *Process) { RegisterKind[kindRegTestBundle](p, 100, "TestKind") }

	fp2d, s2d := fingerprintFor(t, Dimension2D, reg)
	fp3d, s3d := fingerprintFor(t, Dimension3D, reg)

	if fp2d == fp3d {
		t.Fatalf("2D and 3D fingerprint identically (%08x) with a kind registered", fp2d)
	}
	if len(s2d.Entities) != 1 || len(s3d.Entities) != 1 {
		t.Fatalf("expected one entity kind on each side, got %d and %d", len(s2d.Entities), len(s3d.Entities))
	}
	// The engine half of the layout is what phase 2 widened; assert it reached
	// the schema rather than only the binding set.
	if len(s3d.Entities[0].Layout) <= len(s2d.Entities[0].Layout) {
		t.Fatalf("3D layout %v is not wider than 2D %v", s3d.Entities[0].Layout, s2d.Entities[0].Layout)
	}
}

// TestSchemaFingerprint_MatchesTheAssembledSchema asserts that the CACHED
// fingerprint describes the schema the process actually ships.
//
// The two are produced at different times: the hash is cached when
// AssembleFromProcess runs, while the body is serialized live at dump time.
// Assemble before Build has finished registering and they disagree — the dump
// carries a complete body next to a hash of an incomplete one, and every peer
// comparing hashes compares something that describes no schema that exists.
// The body still looks correct, which is what makes it invisible.
//
// Scope, stated honestly: this fixture is headless with no embedded gateway,
// and the registration that actually lands after the control-plane block is
// the gateway's. So this test did NOT fail against the premature-assembly
// ordering that phase 2 unit 5 tried and reverted — `just schema-check` is
// what caught that, by dumping a full example (simple rotated e163ddbf ->
// d1c0d1f0 with a byte-identical body). This is the DB-free lower bound:
// it catches an assembly that never runs, or one that runs before entity
// kinds are registered.
func TestSchemaFingerprint_MatchesTheAssembledSchema(t *testing.T) {
	for _, d := range []Dimension{Dimension2D, Dimension3D} {
		proc := newDimensionTestProcess(t, d)
		RegisterKind[kindRegTestBundle](proc, 101, "MatchKind")

		// Install the protocol BEFORE Build so that BUILD assembles it. A
		// protocol assembled by the test after Build returns is trivially
		// consistent and would pin nothing — which is exactly how an earlier
		// draft of this test passed against the broken ordering.
		p := NewProtocol(proc, "match-test")
		proc.SetProtocol(p)
		proc.Build()
		t.Cleanup(proc.Shutdown)

		cached := p.SchemaFingerprint()
		live := SchemaFingerprint(p.Schema())
		if cached != live {
			t.Errorf("%s: cached fingerprint %08x does not describe the schema it ships with (%08x) — "+
				"the hash was taken before registration finished", d, cached, live)
		}
		if cached == 0 {
			t.Errorf("%s: fingerprint is 0 after Build", d)
		}
	}
}
