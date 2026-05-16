package spatial

import "testing"

func TestLayerBits(t *testing.T) {
	if LayerStatic == 0 {
		t.Fatal("LayerStatic must be non-zero")
	}
	if LayerStatic == LayerProp {
		t.Fatal("LayerStatic and LayerProp must be distinct")
	}
	if LayerProp == LayerEntity {
		t.Fatal("LayerProp and LayerEntity must be distinct")
	}
	// mask must allow OR-combination
	mask := LayerStatic | LayerProp
	if mask&LayerStatic == 0 || mask&LayerProp == 0 {
		t.Fatal("masks must be combinable with OR")
	}
}
