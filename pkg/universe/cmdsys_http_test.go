package universe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

// testRegistry builds a registry with two well-known test commands.
func testRegistry(t *testing.T) *cmdsys.Registry {
	t.Helper()
	reg := cmdsys.NewRegistry()

	type EchoArgs struct {
		Message string `cmd:"help=message to echo"`
	}
	type EchoResult struct {
		Reply string
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "test.echo",
		Capability:  "test.echo",
		Description: "Echo a message back",
		Route:       cmdsys.RouteLocal,
		Args:        EchoArgs{},
		Result:      EchoResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			return EchoResult{Reply: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("register test.echo: %v", err)
	}

	type TpArgs struct {
		Username string  `cmd:"help=target username"`
		X        float32 `cmd:"help=x coordinate"`
		Y        float32 `cmd:"help=y coordinate"`
	}
	type TpResult struct {
		OK bool
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "test.tp",
		Capability:  "test.tp",
		Description: "Teleport a player to the given coordinates",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        TpArgs{},
		Result:      TpResult{},
		Handler: func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
			return TpResult{OK: true}, nil
		},
	}); err != nil {
		t.Fatalf("register test.tp: %v", err)
	}

	return reg
}

func TestCommands_List(t *testing.T) {
	reg := testRegistry(t)
	h := handleCommandList(reg)

	req := httptest.NewRequest(http.MethodGet, "/commands", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var resp struct {
		Commands []commandSummary `json:"commands"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(resp.Commands))
	}
	// Sorted alphabetically: test.echo first, then test.tp.
	if resp.Commands[0].Verb != "test.echo" {
		t.Errorf("expected test.echo first, got %s", resp.Commands[0].Verb)
	}
	if resp.Commands[1].Verb != "test.tp" {
		t.Errorf("expected test.tp second, got %s", resp.Commands[1].Verb)
	}
}

func TestCommands_Describe_Found(t *testing.T) {
	reg := testRegistry(t)
	h := handleCommandDescribe(reg)

	req := httptest.NewRequest(http.MethodGet, "/commands/test.echo", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	var s commandSummary
	if err := json.NewDecoder(rr.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Verb != "test.echo" {
		t.Errorf("verb: got %q want %q", s.Verb, "test.echo")
	}
	if s.Route != "local" {
		t.Errorf("route: got %q want %q", s.Route, "local")
	}
	if len(s.ArgsSchema.Fields) == 0 {
		t.Error("expected ArgsSchema fields")
	}
	if s.ArgsSchema.Fields[0].Help != "message to echo" {
		t.Errorf("help: got %q want %q", s.ArgsSchema.Fields[0].Help, "message to echo")
	}
}

func TestCommands_Describe_Unknown(t *testing.T) {
	reg := testRegistry(t)
	h := handleCommandDescribe(reg)

	req := httptest.NewRequest(http.MethodGet, "/commands/no.such", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unknown_verb" {
		t.Errorf("error body: %v", body)
	}
}

func TestCommands_DescribeHandlesDottedVerbs(t *testing.T) {
	reg := cmdsys.NewRegistry()
	type SubArgs struct{ N int32 }
	_ = reg.Register(cmdsys.Command{
		Verb:       "test.tp.sub",
		Capability: "test.tp.sub",
		Route:      cmdsys.RouteLocal,
		Args:       SubArgs{},
		Handler:    func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) { return nil, nil },
	})

	h := handleCommandDescribe(reg)

	req := httptest.NewRequest(http.MethodGet, "/commands/test.tp.sub", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for three-level dotted verb, got %d; body: %s", rr.Code, rr.Body.String())
	}
	var s commandSummary
	if err := json.NewDecoder(rr.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Verb != "test.tp.sub" {
		t.Errorf("verb: got %q want %q", s.Verb, "test.tp.sub")
	}
}

func TestCommands_JSONStability(t *testing.T) {
	reg := testRegistry(t)
	h := handleCommandList(reg)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/commands", nil)
		rr := httptest.NewRecorder()
		h(rr, req)
		return rr.Body.String()
	}

	first := get()
	second := get()
	if first != second {
		t.Errorf("JSON output is not stable:\nfirst:  %s\nsecond: %s", first, second)
	}
}
