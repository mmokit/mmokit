package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// handleCommandsList — GET /admin/api/commands
func (s *Server) handleCommandsList(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Verb        string   `json:"verb"`
		Capability  string   `json:"capability"`
		Description string   `json:"description"`
		Route       string   `json:"route"`
		Hidden      bool     `json:"hidden,omitempty"`
		Aliases     []string `json:"aliases,omitempty"`
	}
	verbs := s.registry.List() // returns []string
	out := make([]entry, 0, len(verbs))
	for _, v := range verbs {
		c, ok := s.registry.Lookup(v)
		if !ok {
			continue
		}
		out = append(out, entry{
			Verb:        c.Verb,
			Capability:  string(c.Capability),
			Description: c.Description,
			Route:       c.Route.String(),
			Hidden:      c.Hidden,
			Aliases:     c.Aliases,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCommandDescribe — GET /admin/api/commands/<verb>
func (s *Server) handleCommandDescribe(w http.ResponseWriter, r *http.Request) {
	verb := strings.TrimPrefix(r.URL.Path, "/admin/api/commands/")
	if verb == "" {
		writeJSONError(w, http.StatusBadRequest, "verb required")
		return
	}
	c, ok := s.registry.Lookup(verb)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown verb")
		return
	}
	argsSchema, _ := cmdsys.SchemaOf(c.Args)
	resultSchema, _ := cmdsys.SchemaOfResult(c.Result)
	writeJSON(w, http.StatusOK, map[string]any{
		"verb":         c.Verb,
		"capability":   c.Capability,
		"description":  c.Description,
		"route":        c.Route.String(),
		"argsSchema":   argsSchema,
		"resultSchema": resultSchema,
		"usage":        c.Usage,
		"examples":     c.Examples,
	})
}

// handleCommandInvoke — POST /admin/api/commands/<verb>
func (s *Server) handleCommandInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	verb := strings.TrimPrefix(r.URL.Path, "/admin/api/commands/")
	if verb == "" {
		writeJSONError(w, http.StatusBadRequest, "verb required")
		return
	}
	cmd, ok := s.registry.Lookup(verb)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown verb")
		return
	}

	args, err := cmdsys.NewArgs(cmd)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "args schema unavailable")
		return
	}
	if args != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if derr := dec.Decode(args); derr != nil && !errors.Is(derr, io.EOF) {
			writeJSONError(w, http.StatusBadRequest, "invalid args: "+derr.Error())
			return
		}
	}

	caller, _ := callerFrom(r)
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, invokeErr := s.dispatcher.Invoke(ctx, caller, verb, args)
	finishedAt := time.Now()

	entry := AuditEntry{
		TraceID:    res.TraceID,
		Username:   caller.ID,
		IP:         remoteAddr(r).String(),
		Verb:       verb,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	argsJSON, _ := json.Marshal(args)
	entry.ArgsJSON = string(argsJSON)

	switch {
	case errors.Is(invokeErr, cmdsys.ErrRBACDenied):
		entry.OK = false
		entry.Error = invokeErr.Error()
		s.audit.Append(entry)
		s.logger.Log("admin", "cmd verb=%s user=%s ok=false err=%s", verb, caller.ID, invokeErr)
		writeJSONError(w, http.StatusForbidden, invokeErr.Error())
		return
	case errors.Is(invokeErr, cmdsys.ErrUnknownVerb):
		entry.OK = false
		entry.Error = invokeErr.Error()
		s.audit.Append(entry)
		writeJSONError(w, http.StatusNotFound, invokeErr.Error())
		return
	case invokeErr != nil:
		entry.OK = false
		entry.Error = invokeErr.Error()
		s.audit.Append(entry)
		s.logger.Log("admin", "cmd verb=%s user=%s ok=false dur=%s err=%s", verb, caller.ID, finishedAt.Sub(startedAt), invokeErr)
		writeJSONError(w, http.StatusInternalServerError, invokeErr.Error())
		return
	}

	type invokeResp struct {
		OK      bool                  `json:"ok"`
		Result  any                   `json:"result,omitempty"`
		Targets []cmdsys.TargetResult `json:"targets,omitempty"`
		TraceID string                `json:"traceId"`
	}
	resp := invokeResp{OK: true, TraceID: res.TraceID}
	if len(res.PerTarget) == 1 {
		t := res.PerTarget[0]
		if !t.OK {
			entry.OK = false
			entry.Error = t.Error
			s.audit.Append(entry)
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": t.Error, "traceId": res.TraceID})
			return
		}
		resp.Result = t.Result
	} else {
		resp.Targets = res.PerTarget
	}
	entry.OK = true
	s.audit.Append(entry)
	s.logger.Log("admin", "cmd verb=%s user=%s ok=true dur=%s", verb, caller.ID, finishedAt.Sub(startedAt))
	writeJSON(w, http.StatusOK, resp)
}
