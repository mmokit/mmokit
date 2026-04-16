package cmdsys

import (
	"context"
	"testing"
)

type regArgs struct{ Target string }
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

	cmd, ok := r.Lookup("foo.run")
	if !ok {
		t.Fatal("Lookup returned ok=false for registered verb")
	}
	if cmd.Verb != "foo.run" {
		t.Errorf("Verb: got %q want foo.run", cmd.Verb)
	}

	_, ok = r.Lookup("missing")
	if ok {
		t.Error("Lookup should return ok=false for unknown verb")
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

func TestRegistry_AllowsDeeplyNestedResult(t *testing.T) {
	type leaf struct {
		A int
	}
	type row struct {
		L leaf
	}
	type result struct {
		Rows []row
	}
	type args struct{}

	r := NewRegistry()
	err := r.Register(Command{
		Verb:       "test.deep",
		Capability: "test",
		Route:      RouteLocal,
		Args:       args{},
		Result:     result{},
		Handler:    func(ctx context.Context, env *Env, raw any) (any, error) { return result{}, nil },
	})
	if err != nil {
		t.Fatalf("registration should succeed: %v", err)
	}
}

func TestRegistry_SchemaHashesPopulated(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(makeTestCmd("hash.test")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cmd, ok := r.Lookup("hash.test")
	if !ok {
		t.Fatal("Lookup returned ok=false for registered verb")
	}
	if cmd.ArgsSchemaHash == 0 {
		t.Error("ArgsSchemaHash should be non-zero after registration")
	}
	if cmd.ResultSchemaHash == 0 {
		t.Error("ResultSchemaHash should be non-zero after registration")
	}
}
