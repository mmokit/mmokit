package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

type logCategoriesResp struct {
	Groups []logGroup `json:"groups"`
}

type logGroup struct {
	Name       string        `json:"name"`
	Categories []logCategory `json:"categories"`
}

type logCategory struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) handleLogCategories(w http.ResponseWriter, r *http.Request) {
	if s.logger == nil {
		writeJSON(w, http.StatusOK, logCategoriesResp{Groups: []logGroup{}})
		return
	}
	cats := s.logger.Categories()
	byGroup := make(map[string][]logCategory)
	for _, c := range cats {
		group := ""
		if i := strings.Index(c, ":"); i > 0 {
			group = c[:i]
		}
		byGroup[group] = append(byGroup[group], logCategory{
			Name:    c,
			Enabled: s.logger.IsEnabled(c),
		})
	}
	groupNames := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	out := logCategoriesResp{Groups: make([]logGroup, 0, len(groupNames))}
	for _, g := range groupNames {
		entries := byGroup[g]
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		out.Groups = append(out.Groups, logGroup{Name: g, Categories: entries})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if s.logger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "logger unavailable")
		return
	}
	cat := strings.TrimPrefix(r.URL.Path, "/admin/api/logs/categories/")
	if cat == "" || cat == r.URL.Path {
		writeJSONError(w, http.StatusBadRequest, "category required")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Enabled {
		s.logger.Enable(cat)
	} else {
		s.logger.Disable(cat)
	}
	writeJSON(w, http.StatusOK, logCategory{Name: cat, Enabled: s.logger.IsEnabled(cat)})
}
