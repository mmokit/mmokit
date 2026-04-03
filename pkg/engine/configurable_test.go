package engine

import (
	"testing"
)

type testConfig struct {
	Name    string
	Count   int
	ID      uint32
	Rate    float32
	Precise float64
	Enabled bool
}

func newTestReflectConfig() *ReflectConfig {
	return NewReflectConfig(&testConfig{
		Name:    "default",
		Count:   42,
		Rate:    1.5,
		Precise: 3.14159,
		Enabled: true,
	})
}

func TestReflectConfig_Fields(t *testing.T) {
	rc := newTestReflectConfig()
	fields := rc.Fields()
	expected := []string{"Name", "Count", "ID", "Rate", "Precise", "Enabled"}
	if len(fields) != len(expected) {
		t.Fatalf("Fields() returned %d fields, want %d", len(fields), len(expected))
	}
	for i, f := range fields {
		if f != expected[i] {
			t.Errorf("Fields()[%d] = %q, want %q", i, f, expected[i])
		}
	}
}

func TestReflectConfig_GetField_Values(t *testing.T) {
	rc := newTestReflectConfig()

	if v, err := rc.GetField("Name"); err != nil || v != "default" {
		t.Errorf("GetField(Name) = %q, %v; want %q, nil", v, err, "default")
	}
	if v, err := rc.GetField("Count"); err != nil || v != "42" {
		t.Errorf("GetField(Count) = %q, %v; want %q, nil", v, err, "42")
	}
	if v, err := rc.GetField("Enabled"); err != nil || v != "true" {
		t.Errorf("GetField(Enabled) = %q, %v; want %q, nil", v, err, "true")
	}
}

func TestReflectConfig_GetField_CaseInsensitive(t *testing.T) {
	rc := newTestReflectConfig()

	cases := []string{"name", "NAME", "nAmE", "Name"}
	for _, c := range cases {
		v, err := rc.GetField(c)
		if err != nil {
			t.Errorf("GetField(%q) returned unexpected error: %v", c, err)
		}
		if v != "default" {
			t.Errorf("GetField(%q) = %q, want %q", c, v, "default")
		}
	}
}

func TestReflectConfig_GetField_UnknownField(t *testing.T) {
	rc := newTestReflectConfig()
	_, err := rc.GetField("nonexistent")
	if err == nil {
		t.Error("GetField(nonexistent) expected error, got nil")
	}
}

func TestReflectConfig_SetField_String(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Name", "hello"); err != nil {
		t.Fatalf("SetField(Name) error: %v", err)
	}
	v, _ := rc.GetField("Name")
	if v != "hello" {
		t.Errorf("after SetField(Name, hello): got %q, want %q", v, "hello")
	}
}

func TestReflectConfig_SetField_Int(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Count", "99"); err != nil {
		t.Fatalf("SetField(Count) error: %v", err)
	}
	v, _ := rc.GetField("Count")
	if v != "99" {
		t.Errorf("after SetField(Count, 99): got %q, want %q", v, "99")
	}
}

func TestReflectConfig_SetField_Float32(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Rate", "2.5"); err != nil {
		t.Fatalf("SetField(Rate) error: %v", err)
	}
	// GetField will return the float32 formatted value; just ensure no error and value changed
	v, err := rc.GetField("Rate")
	if err != nil {
		t.Fatalf("GetField(Rate) error: %v", err)
	}
	if v == "1.5" {
		t.Errorf("SetField(Rate, 2.5) did not change value; still %q", v)
	}
}

func TestReflectConfig_SetField_Float64(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Precise", "2.71828"); err != nil {
		t.Fatalf("SetField(Precise) error: %v", err)
	}
	v, err := rc.GetField("Precise")
	if err != nil {
		t.Fatalf("GetField(Precise) error: %v", err)
	}
	if v == "3.14159" {
		t.Errorf("SetField(Precise, 2.71828) did not change value; still %q", v)
	}
}

func TestReflectConfig_SetField_Bool(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Enabled", "false"); err != nil {
		t.Fatalf("SetField(Enabled) error: %v", err)
	}
	v, _ := rc.GetField("Enabled")
	if v != "false" {
		t.Errorf("after SetField(Enabled, false): got %q, want %q", v, "false")
	}
}

func TestReflectConfig_SetField_Uint32(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("ID", "999"); err != nil {
		t.Fatalf("SetField(ID) error: %v", err)
	}
	v, _ := rc.GetField("ID")
	if v != "999" {
		t.Errorf("after SetField(ID, 999): got %q, want %q", v, "999")
	}
}

func TestReflectConfig_SetField_UnknownField(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("nonexistent", "value"); err == nil {
		t.Error("SetField(nonexistent) expected error, got nil")
	}
}

func TestReflectConfig_SetField_InvalidInt(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Count", "notanint"); err == nil {
		t.Error("SetField(Count, notanint) expected error, got nil")
	}
}

func TestReflectConfig_SetField_InvalidFloat(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Rate", "notafloat"); err == nil {
		t.Error("SetField(Rate, notafloat) expected error, got nil")
	}
}

func TestReflectConfig_SetField_InvalidBool(t *testing.T) {
	rc := newTestReflectConfig()
	if err := rc.SetField("Enabled", "notabool"); err == nil {
		t.Error("SetField(Enabled, notabool) expected error, got nil")
	}
}
