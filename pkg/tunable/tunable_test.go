package tunable

import "testing"

func TestParseTag(t *testing.T) {
	tag, ok := ParseTag("default=220,min=60,max=420,step=10")
	if !ok {
		t.Fatal("expected tag present")
	}
	if tag.Default != "220" || tag.Min != "60" || tag.Max != "420" || tag.Step != "10" {
		t.Fatalf("bad parse: %+v", tag)
	}
	if _, ok := ParseTag(""); ok {
		t.Fatal("empty tag should report absent")
	}
}

func TestKindOfAndF64RoundTrip(t *testing.T) {
	cases := []struct {
		kind Kind
		in   string
		f    float64
	}{
		{KindFloat, "1.5", 1.5},
		{KindInt, "7", 7},
		{KindUint, "9", 9},
		{KindBool, "true", 1},
	}
	for _, c := range cases {
		got, err := ToF64(c.kind, c.in)
		if err != nil || got != c.f {
			t.Fatalf("ToF64(%v,%q)=%v,%v want %v", c.kind, c.in, got, err, c.f)
		}
		back := FromF64(c.kind, c.f)
		if _, err := ToF64(c.kind, back); err != nil {
			t.Fatalf("FromF64 produced unparseable %q", back)
		}
	}
}

func TestValidateRange(t *testing.T) {
	d := Def{Name: "amp", Kind: KindFloat, Min: "0", Max: "10"}
	if err := d.Validate("5"); err != nil {
		t.Fatalf("5 in [0,10] should pass: %v", err)
	}
	if err := d.Validate("11"); err == nil {
		t.Fatal("11 should fail max")
	}
	if err := d.Validate("-1"); err == nil {
		t.Fatal("-1 should fail min")
	}
}
