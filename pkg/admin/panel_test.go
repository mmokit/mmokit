package admin

import (
	"reflect"
	"testing"
)

func TestPanelRegistry_RegisterAndList(t *testing.T) {
	t.Parallel()
	r := NewPanelRegistry()
	r.Register(PanelDef{ID: "cluster", Label: "Cluster", Group: "Cluster"})
	r.Register(PanelDef{ID: "marketplace", Label: "Marketplace", Group: "Game"})

	got := r.List()
	if len(got) != 2 {
		t.Fatalf("got %d panels, want 2", len(got))
	}
	if got[0].ID != "cluster" {
		t.Fatalf("expected stable order — first should be 'cluster', got %q", got[0].ID)
	}
}

func TestPanelRegistry_DuplicateRejected(t *testing.T) {
	t.Parallel()
	r := NewPanelRegistry()
	if err := r.Register(PanelDef{ID: "x", Label: "X"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(PanelDef{ID: "x", Label: "X2"}); err == nil {
		t.Fatalf("expected duplicate-ID error")
	}
}

func TestPanelDef_Roundtrip(t *testing.T) {
	t.Parallel()
	def := PanelDef{
		ID:           "cluster",
		Label:        "Cluster",
		Icon:         "globe",
		Group:        "Cluster",
		Topics:       []string{"cells", "topology"},
		Commands:     []string{"cell.split", "cell.merge"},
		InitialFetch: "/admin/api/cluster",
	}
	if !reflect.DeepEqual(def.Topics, []string{"cells", "topology"}) {
		t.Fatalf("Topics not preserved")
	}
}
