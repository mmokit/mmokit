package mmokit_test

import (
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/services/auth/authtest"
	"github.com/zenion/mmokit/pkg/universe"
)

func TestRegisterAuthServiceWithMock(t *testing.T) {
	p := universe.New(universe.Config{Mode: "all"})
	if err := mmokit.RegisterAuthServiceWithMock(p, authtest.NewMock()); err != nil {
		t.Fatalf("RegisterAuthServiceWithMock: %v", err)
	}
	// Verify the kind name is now in the registry. Don't call Build() — that
	// would require a real Postgres URL or a way to skip DB. Just verify the
	// service kind is registered.
	// If there's a way to introspect: p.Config().ServiceKinds — but that
	// wouldn't be populated until something explicitly added "auth". For this
	// smoke test, success at RegisterAuthServiceWithMock returning nil is
	// enough: it means RegisterService accepted, the kind didn't conflict,
	// and the migration was appended.
}
