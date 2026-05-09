package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type fakeView struct{}

func (fakeView) Cluster() ClusterInfo {
	return ClusterInfo{HostCount: 1, CellCount: 4}
}
func (fakeView) Cells() []CellInfo { return []CellInfo{{ID: "0_0"}} }
func (fakeView) Cell(id string) (CellInfo, error) {
	if id == "0_0" {
		return CellInfo{ID: "0_0"}, nil
	}
	return CellInfo{}, ErrCellNotFound
}
func (fakeView) Hosts() []HostInfo                   { return []HostInfo{{ID: "host-a"}} }
func (fakeView) Gateways() []GatewayInfo             { return nil }
func (fakeView) Players(PlayerFilter) []PlayerInfo   { return []PlayerInfo{{Username: "josh"}} }
func (fakeView) Player(string) (PlayerInfo, error)   { return PlayerInfo{Username: "josh"}, nil }
func (fakeView) CommitLog(CommitQuery) []CommitEvent { return nil }
func (fakeView) Perf(string) (PerfSnapshot, error)   { return PerfSnapshot{}, ErrUnavailable }

func TestHandleCells_List(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}, panels: NewPanelRegistry()}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cells", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "josh"}))
	w := httptest.NewRecorder()
	s.handleCells(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleCells_Detail_NotFound(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cells/missing", nil)
	w := httptest.NewRecorder()
	s.handleCells(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleCells_Detail_Found(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cells/0_0", nil)
	w := httptest.NewRecorder()
	s.handleCells(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHandleCluster(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/cluster", nil)
	w := httptest.NewRecorder()
	s.handleCluster(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleHosts(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/hosts", nil)
	w := httptest.NewRecorder()
	s.handleHosts(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleGateways(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/gateways", nil)
	w := httptest.NewRecorder()
	s.handleGateways(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandlePlayers_List(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/players?status=online&limit=50", nil)
	w := httptest.NewRecorder()
	s.handlePlayers(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandlePlayers_Detail(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/players/josh", nil)
	w := httptest.NewRecorder()
	s.handlePlayers(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleEvents(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/events?n=10&since=5m", nil)
	w := httptest.NewRecorder()
	s.handleEvents(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandlePerf_MissingID(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/perf/", nil)
	w := httptest.NewRecorder()
	s.handlePerf(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandlePerf_Unavailable_Returns200(t *testing.T) {
	t.Parallel()
	s := &Server{view: fakeView{}}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/perf/0_0", nil)
	w := httptest.NewRecorder()
	s.handlePerf(w, r)
	// fakeView.Perf returns ErrUnavailable; handler should still 200 with empty snapshot.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (best-effort), got %d", w.Code)
	}
}

func TestHandleAudit_Forbidden(t *testing.T) {
	t.Parallel()
	s := &Server{audit: NewAuditLog(8)}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/audit", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "lowpriv", Grants: []cmdsys.Grant{{Pattern: "cell.*", Allow: true}}}))
	w := httptest.NewRecorder()
	s.handleAudit(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestHandleAudit_Allowed(t *testing.T) {
	t.Parallel()
	s := &Server{audit: NewAuditLog(8)}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/audit", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "josh", Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}}}))
	w := httptest.NewRecorder()
	s.handleAudit(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAudit_AllowedExact(t *testing.T) {
	t.Parallel()
	s := &Server{audit: NewAuditLog(8)}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/audit", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "auditor", Grants: []cmdsys.Grant{{Pattern: "admin.audit", Allow: true}}}))
	w := httptest.NewRecorder()
	s.handleAudit(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandleAudit_AllowedNamespaceWildcard(t *testing.T) {
	t.Parallel()
	s := &Server{audit: NewAuditLog(8)}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/audit", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{ID: "admin", Grants: []cmdsys.Grant{{Pattern: "admin.*", Allow: true}}}))
	w := httptest.NewRecorder()
	s.handleAudit(w, r)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandlePanels(t *testing.T) {
	t.Parallel()
	pr := NewPanelRegistry()
	_ = pr.Register(PanelDef{ID: "x", Label: "X", Group: "Cluster"})
	s := &Server{panels: pr}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/panels", nil)
	w := httptest.NewRecorder()
	s.handlePanels(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}
