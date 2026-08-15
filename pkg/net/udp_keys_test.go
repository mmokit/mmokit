package net

import (
	"errors"
	"testing"
	"time"
)

func TestUDPKeyIssueAndLookup(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)

	id, entry, err := r.Issue("user-1", "alice", now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if id == 0 {
		t.Fatal("issued key id 0; 0 is reserved as absent on the wire")
	}
	if entry.UserID != "user-1" || entry.Username != "alice" {
		t.Fatalf("entry identity = %+v", entry)
	}

	got, err := r.Lookup(id, now)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Key != entry.Key {
		t.Fatal("looked-up key differs from issued key")
	}
}

// Keys must survive being used, or a lost handshake packet costs an HTTPS
// round trip and a roaming client cannot re-handshake under the same identity.
func TestUDPKeyIsMultiUse(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	id, _, err := r.Issue("user-1", "alice", now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	for i := range 5 {
		if _, err := r.Lookup(id, now); err != nil {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
}

func TestUDPKeyExpiry(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	id, _, err := r.Issue("user-1", "alice", now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := r.Lookup(id, now.Add(59*time.Second)); err != nil {
		t.Fatalf("lookup before expiry: %v", err)
	}
	if _, err := r.Lookup(id, now.Add(time.Minute)); !errors.Is(err, ErrUDPKeyUnknown) {
		t.Fatalf("expired key resolved, err=%v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("expired entry not dropped on lookup, len=%d", r.Len())
	}
}

func TestUDPKeyUnknownRejected(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	if _, err := r.Lookup(UDPKeyID(0xdeadbeef), now); !errors.Is(err, ErrUDPKeyUnknown) {
		t.Fatalf("unknown key resolved, err=%v", err)
	}
	if _, err := r.Lookup(0, now); !errors.Is(err, ErrUDPKeyUnknown) {
		t.Fatalf("key id 0 resolved, err=%v", err)
	}
}

func TestUDPKeyRegistryBounded(t *testing.T) {
	const max = 8
	r := NewUDPKeyRegistry(max, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	for i := range max {
		if _, _, err := r.Issue("user", "u", now); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, _, err := r.Issue("user", "u", now); !errors.Is(err, ErrUDPKeyRegistryFull) {
		t.Fatalf("registry exceeded its cap, err=%v", err)
	}
	if _, _, rej := r.Stats(); rej != 1 {
		t.Fatalf("rejected counter = %d want 1", rej)
	}
}

// A full table must recover once its entries age out, rather than staying
// wedged the way the Tier 1 pending table could.
func TestUDPKeyRegistryRecoversAfterExpiry(t *testing.T) {
	const max = 4
	r := NewUDPKeyRegistry(max, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	for i := range max {
		if _, _, err := r.Issue("user", "u", now); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, _, err := r.Issue("user", "u", now); err == nil {
		t.Fatal("expected the table to be full")
	}
	later := now.Add(2 * time.Minute)
	if _, _, err := r.Issue("user", "u", later); err != nil {
		t.Fatalf("issue after expiry should succeed: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("stale entries not swept, len=%d", r.Len())
	}
}

func TestUDPKeyRevoke(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	id, _, err := r.Issue("user-1", "alice", now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	r.Revoke(id)
	if _, err := r.Lookup(id, now); !errors.Is(err, ErrUDPKeyUnknown) {
		t.Fatalf("revoked key still resolves, err=%v", err)
	}
}

func TestUDPKeysAreDistinct(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	seenID := map[UDPKeyID]bool{}
	seenKey := map[[32]byte]bool{}
	for i := range 200 {
		id, entry, err := r.Issue("user", "u", now)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		if seenID[id] {
			t.Fatalf("duplicate key id %d at iteration %d", id, i)
		}
		if seenKey[entry.Key] {
			t.Fatalf("duplicate key material at iteration %d — keys must never repeat", i)
		}
		seenID[id] = true
		seenKey[entry.Key] = true
	}
}

func TestUDPKeyStats(t *testing.T) {
	r := NewUDPKeyRegistry(0, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	id, _, _ := r.Issue("u", "u", now)
	_, _ = r.Lookup(id, now)
	_, _ = r.Lookup(id, now)
	issued, claimed, rejected := r.Stats()
	if issued != 1 || claimed != 2 || rejected != 0 {
		t.Fatalf("stats = (%d,%d,%d) want (1,2,0)", issued, claimed, rejected)
	}
}
