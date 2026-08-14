package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mmokit/mmokit/pkg/logger"
)

func TestLogCategories_Empty(t *testing.T) {
	t.Parallel()
	s := &Server{logger: logger.New()}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/logs/categories", nil)
	w := httptest.NewRecorder()
	s.handleLogCategories(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp logCategoriesResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Groups) != 0 {
		t.Fatalf("expected 0 groups, got %d: %+v", len(resp.Groups), resp.Groups)
	}
}

func TestLogCategories_GroupedAndSorted(t *testing.T) {
	t.Parallel()
	l := logger.New("mesh:cell")
	l.RegisterCategories("events:split", "events:merge", "mesh:cell", "mesh:transfer", "admin")
	s := &Server{logger: l}

	r := httptest.NewRequest(http.MethodGet, "/admin/api/logs/categories", nil)
	w := httptest.NewRecorder()
	s.handleLogCategories(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp logCategoriesResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d: %+v", len(resp.Groups), resp.Groups)
	}

	// Alphabetical: "" (admin), "events", "mesh"
	wantOrder := []string{"", "events", "mesh"}
	for i, g := range resp.Groups {
		if g.Name != wantOrder[i] {
			t.Fatalf("group[%d]: want name=%q got %q", i, wantOrder[i], g.Name)
		}
	}

	// Group "" → admin
	g0 := resp.Groups[0]
	if len(g0.Categories) != 1 || g0.Categories[0].Name != "admin" {
		t.Fatalf("group[0] (\"\"): want [admin], got %+v", g0.Categories)
	}
	if g0.Categories[0].Enabled {
		t.Fatalf("admin should be disabled")
	}

	// Group "events" → merge, split (sorted)
	g1 := resp.Groups[1]
	if len(g1.Categories) != 2 {
		t.Fatalf("events group: want 2 entries, got %d", len(g1.Categories))
	}
	if g1.Categories[0].Name != "events:merge" || g1.Categories[1].Name != "events:split" {
		t.Fatalf("events group order wrong: %+v", g1.Categories)
	}

	// Group "mesh" → mesh:cell (enabled), mesh:transfer (disabled)
	g2 := resp.Groups[2]
	if len(g2.Categories) != 2 {
		t.Fatalf("mesh group: want 2 entries, got %d", len(g2.Categories))
	}
	if g2.Categories[0].Name != "mesh:cell" {
		t.Fatalf("mesh[0]: want mesh:cell, got %q", g2.Categories[0].Name)
	}
	if !g2.Categories[0].Enabled {
		t.Fatalf("mesh:cell should be enabled")
	}
	if g2.Categories[1].Name != "mesh:transfer" {
		t.Fatalf("mesh[1]: want mesh:transfer, got %q", g2.Categories[1].Name)
	}
	if g2.Categories[1].Enabled {
		t.Fatalf("mesh:transfer should be disabled")
	}
}

func TestLogToggle_EnableThenDisable(t *testing.T) {
	t.Parallel()
	l := logger.New()
	l.RegisterCategories("mesh:cell")
	s := &Server{logger: l}

	// Enable
	body, _ := json.Marshal(map[string]any{"enabled": true})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/logs/categories/mesh:cell", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleLogToggle(w, r)
	if w.Code != 200 {
		t.Fatalf("enable status=%d body=%s", w.Code, w.Body.String())
	}
	if !l.IsEnabled("mesh:cell") {
		t.Fatalf("mesh:cell should be enabled after toggle on")
	}

	// Disable
	body, _ = json.Marshal(map[string]any{"enabled": false})
	r = httptest.NewRequest(http.MethodPost, "/admin/api/logs/categories/mesh:cell", bytes.NewReader(body))
	w = httptest.NewRecorder()
	s.handleLogToggle(w, r)
	if w.Code != 200 {
		t.Fatalf("disable status=%d body=%s", w.Code, w.Body.String())
	}
	if l.IsEnabled("mesh:cell") {
		t.Fatalf("mesh:cell should be disabled after toggle off")
	}
}

func TestLogToggle_MissingCategoryReturns400(t *testing.T) {
	t.Parallel()
	s := &Server{logger: logger.New()}
	r := httptest.NewRequest(http.MethodPost, "/admin/api/logs/categories/", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	s.handleLogToggle(w, r)
	if w.Code != 400 {
		t.Fatalf("want 400, got status=%d body=%s", w.Code, w.Body.String())
	}
}
