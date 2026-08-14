package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
)

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Cluster())
}

func (s *Server) handleCells(w http.ResponseWriter, r *http.Request) {
	if id := strings.TrimPrefix(r.URL.Path, "/admin/api/cells/"); id != "" && id != r.URL.Path {
		c, err := s.view.Cell(id)
		if err == ErrCellNotFound {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)
		return
	}
	writeJSON(w, http.StatusOK, s.view.Cells())
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Hosts())
}

func (s *Server) handleGateways(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.view.Gateways())
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	if name := strings.TrimPrefix(r.URL.Path, "/admin/api/players/"); name != "" && name != r.URL.Path {
		p, err := s.view.Player(name)
		if err == ErrPlayerNotFound {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	q := r.URL.Query()
	filter := PlayerFilter{
		Status: q.Get("status"),
		Search: q.Get("search"),
		Limit:  atoiDefault(q.Get("limit"), 100),
		Offset: atoiDefault(q.Get("offset"), 0),
	}
	writeJSON(w, http.StatusOK, s.view.Players(filter))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cq := CommitQuery{
		N:      atoiDefault(q.Get("n"), 200),
		Cell:   q.Get("cell"),
		Commit: q.Get("commit"),
	}
	if since := q.Get("since"); since != "" {
		if d, err := time.ParseDuration(since); err == nil {
			cq.Since = d
		}
	}
	writeJSON(w, http.StatusOK, s.view.CommitLog(cq))
}

func (s *Server) handlePerf(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/perf/")
	if id == "" || id == r.URL.Path {
		writeJSONError(w, http.StatusBadRequest, "cellID required")
		return
	}
	ps, err := s.view.Perf(id)
	switch err {
	case nil:
		writeJSON(w, http.StatusOK, ps)
	case ErrCellNotFound:
		writeJSONError(w, http.StatusNotFound, err.Error())
	case ErrUnavailable:
		// v1 best-effort: return the empty snapshot rather than 500.
		writeJSON(w, http.StatusOK, ps)
	default:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	caller, _ := callerFrom(r)
	if !cmdsys.Check(caller, "admin.audit") {
		writeJSONError(w, http.StatusForbidden, "missing admin.audit grant")
		return
	}
	q := r.URL.Query()
	n := atoiDefault(q.Get("n"), 200)
	writeJSON(w, http.StatusOK, s.audit.Recent(n))
}

func (s *Server) handlePanels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.panels.List())
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
