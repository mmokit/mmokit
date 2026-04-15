package cmdsys

import (
	"context"
	"testing"
)

type regArgs struct{ Target string `cmd:"required"` }
type regResult struct{ Message string }

func makeTestCmd(verb string) Command {
	return Command{
		Verb:        verb,
		Capability:  Capability(verb),
		Description: "test command " + verb,
		Route:       RouteLocal,
		Args:        regArgs{},
		Result:      regResult{},
		Handler: func(_ context.Context, _ *Env, _ any) (any, error) {
			return regResult{Message: "ok"}, nil
		},
	}
}

func TestRegistry_RegisterLookupList(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(makeTestCmd("foo.run")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register(makeTestCmd("bar.run")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cmd := r.Lookup("foo.run")
	if cmd == nil {
		t.Fatal("Lookup returned nil for registered verb")
	}
	if cmd.Verb != "foo.run" {
		t.Errorf("Verb: got %q want foo.run", cmd.Verb)
	}

	if r.Lookup("missing") != nil {
		t.Error("Lookup should return nil for unknown verb")
	}

	list := r.List()
	if len(list) != 2 {
		t.Errorf("List len: got %d want 2", len(list))
	}
}

func TestRegistry_DuplicateVerb(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(makeTestCmd("dupe")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(makeTestCmd("dupe")); err == nil {
		t.Fatal("expected error on duplicate verb")
	}
}

func TestRegistry_SchemaHashesPopulated(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(makeTestCmd("hash.test")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cmd := r.Lookup("hash.test")
	if cmd.ArgsSchemaHash == 0 {
		t.Error("ArgsSchemaHash should be non-zero after registration")
	}
	if cmd.ResultSchemaHash == 0 {
		t.Error("ResultSchemaHash should be non-zero after registration")
	}
}
