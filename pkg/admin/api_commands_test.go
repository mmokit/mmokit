package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func TestHandleCommandInvoke_Success(t *testing.T) {
	t.Parallel()
	reg := cmdsys.NewRegistry()
	type echoArgs struct {
		Msg string `json:"msg"`
	}
	type echoResult struct {
		Echo string `json:"echo"`
	}
	if err := reg.Register(cmdsys.Command{
		Verb:       "test.echo",
		Capability: "test.echo",
		Args:       echoArgs{},
		Result:     echoResult{},
		Route:      cmdsys.RouteLocal,
		Handler: func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
			a := args.(echoArgs)
			return echoResult{Echo: a.Msg}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	disp := cmdsys.NewDispatcher(cmdsys.DispatcherConfig{Registry: reg})

	s := &Server{
		registry:   reg,
		dispatcher: disp,
		audit:      NewAuditLog(8),
		log:        testLogger{},
	}

	body, _ := json.Marshal(map[string]string{"msg": "hi"})
	r := httptest.NewRequest(http.MethodPost, "/admin/api/commands/test.echo", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), ctxKeyCaller, cmdsys.Caller{
		ID:     "josh",
		Source: cmdsys.SourceAdminHTTP,
		Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}},
	}))
	w := httptest.NewRecorder()
	s.handleCommandInvoke(w, r)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Echo string `json:"echo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result.Echo != "hi" {
		t.Fatalf("echo=%q", resp.Result.Echo)
	}
}

func TestHandleCommandsList(t *testing.T) {
	t.Parallel()
	reg := cmdsys.NewRegistry()
	_ = reg.Register(cmdsys.Command{
		Verb: "x.y", Capability: "x.y", Route: cmdsys.RouteLocal,
		Handler: func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil },
	})
	s := &Server{registry: reg}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/commands", nil)
	w := httptest.NewRecorder()
	s.handleCommandsList(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHandleCommandDescribe(t *testing.T) {
	t.Parallel()
	reg := cmdsys.NewRegistry()
	type a struct{ X int }
	type b struct{ Y int }
	_ = reg.Register(cmdsys.Command{
		Verb: "x.y", Capability: "x.y", Args: a{}, Result: b{}, Route: cmdsys.RouteLocal,
		Handler: func(context.Context, *cmdsys.Env, any) (any, error) { return nil, nil },
	})
	s := &Server{registry: reg}
	r := httptest.NewRequest(http.MethodGet, "/admin/api/commands/x.y", nil)
	w := httptest.NewRecorder()
	s.handleCommandDescribe(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
