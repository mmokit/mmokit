package wasmhost

import (
	"context"
	"testing"
)

func TestParamsSchemaAndSet(t *testing.T) {
	ctx := context.Background()
	rt := New(ctx)
	defer rt.Close(ctx)
	m, err := Load(ctx, rt, buildModule(t, "internal/parammod"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)

	fields, err := m.ParamsSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].Name != "Gain" || fields[0].Max != 10 {
		t.Fatalf("bad schema: %+v", fields)
	}
	// Gain default 2 → column doubles.
	col := []float32{1, 2, 3}
	in := f32sToBytes(col)
	out, err := m.Update(ctx, 3, 0.1, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	if bytesToF32s(out)[0] != 2 || bytesToF32s(out)[2] != 6 {
		t.Fatalf("default gain not applied: %v", out)
	}
	// Push Gain=3, Enable=true → triples.
	if err := m.ParamsSet(ctx, []float64{3, 1}); err != nil {
		t.Fatal(err)
	}
	out, err = m.Update(ctx, 3, 0.1, 0, f32sToBytes(col))
	if err != nil {
		t.Fatal(err)
	}
	if bytesToF32s(out)[1] != 6 {
		t.Fatalf("pushed gain not applied: %v", out)
	}
}
