package auth_test

import (
	"errors"
	"testing"

	"github.com/zenion/mmokit/pkg/services/auth"
)

func TestErrCapabilityNotFound_Defined(t *testing.T) {
	if auth.ErrCapabilityNotFound == nil {
		t.Fatal("auth.ErrCapabilityNotFound must be a non-nil sentinel")
	}
	if !errors.Is(auth.ErrCapabilityNotFound, auth.ErrCapabilityNotFound) {
		t.Fatal("sentinel must satisfy errors.Is identity")
	}
}
