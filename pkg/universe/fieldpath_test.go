package universe

import "testing"

type fpInner struct {
	Max int32
}

type fpEnum uint8

type fpProbe struct {
	Current int32
	Speed   float32
	Alive   bool
	Label   string
	Mode    fpEnum
	Nested  fpInner
	Tags    []string       // read-only
	Counts  map[string]int // read-only
	hidden  int            // unexported — skipped
}

func TestListFields_EnumeratesScalarLeaves(t *testing.T) {
	p := &fpProbe{Current: 5, Speed: 1.5, Alive: true, Label: "hi", Mode: 2,
		Nested: fpInner{Max: 9}, Tags: []string{"a"}, Counts: map[string]int{"x": 1}}
	fields := ListFields(p)

	byPath := map[string]FieldInfo{}
	for _, f := range fields {
		byPath[f.Path] = f
	}

	for _, want := range []string{"Current", "Speed", "Alive", "Label", "Mode", "Nested.Max"} {
		f, ok := byPath[want]
		if !ok {
			t.Fatalf("missing scalar leaf %q; got %v", want, byPath)
		}
		if !f.Editable {
			t.Errorf("%q Editable = false, want true", want)
		}
	}
	if byPath["Current"].Value != "5" {
		t.Errorf("Current value = %q, want 5", byPath["Current"].Value)
	}
	// Slices/maps are surfaced read-only.
	if f, ok := byPath["Tags"]; !ok || f.Editable {
		t.Errorf("Tags should be present and read-only, got %+v ok=%v", f, ok)
	}
	if f, ok := byPath["Counts"]; !ok || f.Editable {
		t.Errorf("Counts should be present and read-only, got %+v ok=%v", f, ok)
	}
	// Unexported fields are skipped.
	if _, ok := byPath["hidden"]; ok {
		t.Error("unexported field 'hidden' must not appear")
	}
}

func TestSetFieldByPath_Scalars(t *testing.T) {
	cases := []struct {
		path, val, wantNew string
	}{
		{"Current", "42", "42"},
		{"Speed", "2.5", "2.5"},
		{"Alive", "false", "false"},
		{"Label", "world", "world"},
		{"Mode", "3", "3"},
		{"Nested.Max", "100", "100"},
	}
	for _, c := range cases {
		p := &fpProbe{Current: 5, Speed: 1.5, Alive: true, Label: "hi", Mode: 1, Nested: fpInner{Max: 9}}
		_, gotNew, err := SetFieldByPath(p, c.path, c.val)
		if err != nil {
			t.Fatalf("SetFieldByPath(%q,%q): %v", c.path, c.val, err)
		}
		if gotNew != c.wantNew {
			t.Errorf("%q new = %q, want %q", c.path, gotNew, c.wantNew)
		}
	}
	// Verify the int actually landed.
	p := &fpProbe{}
	if _, _, err := SetFieldByPath(p, "Current", "7"); err != nil || p.Current != 7 {
		t.Fatalf("Current not set: p.Current=%d err=%v", p.Current, err)
	}
	if _, _, err := SetFieldByPath(p, "Nested.Max", "11"); err != nil || p.Nested.Max != 11 {
		t.Fatalf("Nested.Max not set: %d err=%v", p.Nested.Max, err)
	}
}

func TestSetFieldByPath_Errors(t *testing.T) {
	p := &fpProbe{}
	if _, _, err := SetFieldByPath(p, "Nope", "1"); err == nil {
		t.Error("expected error for unknown field")
	}
	if _, _, err := SetFieldByPath(p, "Tags", "x"); err == nil {
		t.Error("expected error for read-only slice field")
	}
	if _, _, err := SetFieldByPath(p, "Current", "abc"); err == nil {
		t.Error("expected error for uncoercible int")
	}
}
