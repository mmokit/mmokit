package universe

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// commandSummary is the JSON shape returned by /commands and /commands/{verb}.
type commandSummary struct {
	Verb             string        `json:"verb"`
	Capability       string        `json:"capability"`
	Description      string        `json:"description"`
	Route            string        `json:"route"`
	ArgsSchema       cmdsys.Schema `json:"args_schema"`
	ResultSchema     cmdsys.Schema `json:"result_schema"`
	ArgsSchemaHash   uint64        `json:"args_schema_version"`
	ResultSchemaHash uint64        `json:"result_schema_version"`
}

func summarize(cmd cmdsys.Command) (commandSummary, error) {
	s := commandSummary{
		Verb:             cmd.Verb,
		Capability:       string(cmd.Capability),
		Description:      cmd.Description,
		Route:            cmd.Route.String(),
		ArgsSchemaHash:   cmd.ArgsSchemaHash,
		ResultSchemaHash: cmd.ResultSchemaHash,
	}
	if cmd.Args != nil {
		sc, err := cmdsys.SchemaOf(cmd.Args)
		if err != nil {
			return commandSummary{}, err
		}
		s.ArgsSchema = sc
	}
	if cmd.Result != nil {
		sc, err := cmdsys.SchemaOf(cmd.Result)
		if err != nil {
			return commandSummary{}, err
		}
		s.ResultSchema = sc
	}
	return s, nil
}

// handleCommandList serves GET /commands — full sorted catalog.
func handleCommandList(reg *cmdsys.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		verbs := reg.List()
		sort.Strings(verbs)

		summaries := make([]commandSummary, 0, len(verbs))
		for _, v := range verbs {
			cmd, ok := reg.Lookup(v)
			if !ok {
				continue
			}
			s, err := summarize(cmd)
			if err != nil {
				http.Error(w, "schema error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			summaries = append(summaries, s)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"commands": summaries})
	}
}

// handleCommandDescribe serves GET /commands/{verb} — single command schema.
func handleCommandDescribe(reg *cmdsys.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		verb := strings.TrimPrefix(r.URL.Path, "/commands/")
		if verb == "" {
			http.Error(w, "missing verb", http.StatusBadRequest)
			return
		}

		cmd, ok := reg.Lookup(verb)
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown_verb"})
			return
		}

		s, err := summarize(cmd)
		if err != nil {
			http.Error(w, "schema error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(s)
	}
}
