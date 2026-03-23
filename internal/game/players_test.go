package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestPlayerTracker_NewInitialized(t *testing.T) {
	pt := NewPlayerTracker()
	if pt.Entities == nil {
		t.Error("Entities map is nil")
	}
	if pt.Usernames == nil {
		t.Error("Usernames map is nil")
	}
	if pt.Dead == nil {
		t.Error("Dead map is nil")
	}
	if pt.Docked == nil {
		t.Error("Docked map is nil")
	}
	if pt.Docking == nil {
		t.Error("Docking map is nil")
	}
	if pt.PendingConnections == nil {
		t.Error("PendingConnections map is nil")
	}
	if pt.PendingLogins == nil {
		t.Error("PendingLogins map is nil")
	}
}

func TestPlayerTracker_RemoveClearsAll(t *testing.T) {
	pt := NewPlayerTracker()
	var connID uint32 = 7

	pt.Entities[connID] = ecs.Entity{}
	pt.Usernames[connID] = "alice"
	pt.Dead[connID] = true
	pt.Docked[connID] = true
	pt.Docking[connID] = &DockingState{}
	pt.PendingConnections[connID] = true
	pt.PendingLogins[connID] = "alice"

	pt.Remove(connID)

	if _, ok := pt.Entities[connID]; ok {
		t.Error("Entities not cleared")
	}
	if _, ok := pt.Usernames[connID]; ok {
		t.Error("Usernames not cleared")
	}
	if _, ok := pt.Dead[connID]; ok {
		t.Error("Dead not cleared")
	}
	if _, ok := pt.Docked[connID]; ok {
		t.Error("Docked not cleared")
	}
	if _, ok := pt.Docking[connID]; ok {
		t.Error("Docking not cleared")
	}
	if _, ok := pt.PendingConnections[connID]; ok {
		t.Error("PendingConnections not cleared")
	}
	if _, ok := pt.PendingLogins[connID]; ok {
		t.Error("PendingLogins not cleared")
	}
}

func TestPlayerTracker_RemoveDoesNotAffectOthers(t *testing.T) {
	pt := NewPlayerTracker()
	var id1 uint32 = 1
	var id2 uint32 = 2

	pt.Entities[id1] = ecs.Entity{}
	pt.Usernames[id1] = "alice"
	pt.Entities[id2] = ecs.Entity{}
	pt.Usernames[id2] = "bob"

	pt.Remove(id1)

	if _, ok := pt.Entities[id2]; !ok {
		t.Error("Remove deleted other connID's entity")
	}
	if pt.Usernames[id2] != "bob" {
		t.Error("Remove deleted other connID's username")
	}
}

func TestPlayerTracker_UsernameInUse(t *testing.T) {
	pt := NewPlayerTracker()
	pt.Usernames[1] = "alice"
	pt.Usernames[2] = "bob"

	if !pt.UsernameInUse("alice") {
		t.Error("expected alice to be in use")
	}
	if !pt.UsernameInUse("bob") {
		t.Error("expected bob to be in use")
	}
	if pt.UsernameInUse("charlie") {
		t.Error("expected charlie to not be in use")
	}
}
