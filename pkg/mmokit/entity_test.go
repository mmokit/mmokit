package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestEntity_ZeroValueIsNotAlive(t *testing.T) {
	var e mmokit.Entity
	if e.Alive() {
		t.Fatal("zero-value Entity should not be Alive")
	}
	if e.NetID() != 0 {
		t.Fatalf("zero-value NetID = %d, want 0", e.NetID())
	}
}
