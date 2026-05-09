package admin

import (
	"testing"
	"time"
)

func TestMemorySessionStore_CreateLookup(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	rec := SessionRecord{
		Username:  "josh",
		Grants:    []string{"*.*"},
		IP:        "127.0.0.1",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	token, hash, err := s.Create(rec)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(hash) == 0 {
		t.Fatal("empty token or hash")
	}
	got, ok := s.Lookup(hash)
	if !ok {
		t.Fatal("lookup failed")
	}
	if got.Username != "josh" {
		t.Fatalf("user mismatch: %q", got.Username)
	}
}

func TestMemorySessionStore_Expired(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	rec := SessionRecord{Username: "x", ExpiresAt: time.Now().Add(-time.Second)}
	_, hash, _ := s.Create(rec)
	if _, ok := s.Lookup(hash); ok {
		t.Fatal("expired session looked up successfully")
	}
}

func TestMemorySessionStore_DeleteExpired(t *testing.T) {
	t.Parallel()
	s := NewMemorySessionStore()
	old := SessionRecord{Username: "old", ExpiresAt: time.Now().Add(-time.Hour)}
	cur := SessionRecord{Username: "cur", ExpiresAt: time.Now().Add(time.Hour)}
	_, hOld, _ := s.Create(old)
	_, hCur, _ := s.Create(cur)
	if err := s.DeleteExpired(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(hOld); ok {
		t.Fatal("old still present")
	}
	if _, ok := s.Lookup(hCur); !ok {
		t.Fatal("cur missing")
	}
}
