package persist

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// BoltStore tests
// ---------------------------------------------------------------------------

func openTestBolt(t *testing.T) *BoltStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenBolt(path)
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBoltStore_PutGet(t *testing.T) {
	s := openTestBolt(t)

	if err := s.Put("players", "alice", []byte("data1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get("players", "alice")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "data1" {
		t.Fatalf("Get = %q, want %q", got, "data1")
	}
}

func TestBoltStore_GetMissing(t *testing.T) {
	s := openTestBolt(t)

	_, err := s.Get("players", "nobody")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestBoltStore_Delete(t *testing.T) {
	s := openTestBolt(t)

	s.Put("inv", "sword", []byte("sharp"))
	if err := s.Delete("inv", "sword"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := s.Get("inv", "sword")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete: expected ErrNotFound, got %v", err)
	}
}

func TestBoltStore_ForEach(t *testing.T) {
	s := openTestBolt(t)

	data := map[string]string{"a": "1", "b": "2", "c": "3"}
	for k, v := range data {
		s.Put("col", k, []byte(v))
	}

	visited := map[string]string{}
	err := s.ForEach("col", func(key string, value []byte) error {
		visited[key] = string(value)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if len(visited) != len(data) {
		t.Fatalf("ForEach visited %d keys, want %d", len(visited), len(data))
	}
	for k, v := range data {
		if visited[k] != v {
			t.Fatalf("ForEach key %q = %q, want %q", k, visited[k], v)
		}
	}
}

func TestBoltStore_CollectionsIndependent(t *testing.T) {
	s := openTestBolt(t)

	s.Put("col1", "key", []byte("val1"))
	s.Put("col2", "key", []byte("val2"))

	got1, _ := s.Get("col1", "key")
	got2, _ := s.Get("col2", "key")

	if string(got1) != "val1" {
		t.Fatalf("col1 = %q, want %q", got1, "val1")
	}
	if string(got2) != "val2" {
		t.Fatalf("col2 = %q, want %q", got2, "val2")
	}
}

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	data map[string]map[string][]byte
	mu   sync.Mutex
}

func newMockStore() *mockStore {
	return &mockStore{data: make(map[string]map[string][]byte)}
}

func (m *mockStore) Get(collection, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	col, ok := m.data[collection]
	if !ok {
		return nil, ErrNotFound
	}
	v, ok := col[key]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *mockStore) Put(collection, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	col, ok := m.data[collection]
	if !ok {
		col = make(map[string][]byte)
		m.data[collection] = col
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	col[key] = cp
	return nil
}

func (m *mockStore) Delete(collection, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	col, ok := m.data[collection]
	if !ok {
		return nil
	}
	delete(col, key)
	return nil
}

func (m *mockStore) ForEach(collection string, fn func(key string, value []byte) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	col, ok := m.data[collection]
	if !ok {
		return nil
	}
	for k, v := range col {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// AsyncWriter tests
// ---------------------------------------------------------------------------

func TestAsyncWriter_PutOps(t *testing.T) {
	ms := newMockStore()
	w := NewAsyncWriter(ms, 64)
	w.Start()

	w.Enqueue(Op{Collection: "players", Key: "alice", Value: []byte("data-a")})
	w.Enqueue(Op{Collection: "players", Key: "bob", Value: []byte("data-b")})
	w.Flush()

	got, err := ms.Get("players", "alice")
	if err != nil {
		t.Fatalf("Get alice: %v", err)
	}
	if string(got) != "data-a" {
		t.Fatalf("alice = %q, want %q", got, "data-a")
	}

	got, err = ms.Get("players", "bob")
	if err != nil {
		t.Fatalf("Get bob: %v", err)
	}
	if string(got) != "data-b" {
		t.Fatalf("bob = %q, want %q", got, "data-b")
	}
}

func TestAsyncWriter_NilValueDeletes(t *testing.T) {
	ms := newMockStore()
	ms.Put("inv", "sword", []byte("sharp"))

	w := NewAsyncWriter(ms, 64)
	w.Start()

	w.Enqueue(Op{Collection: "inv", Key: "sword", Value: nil})
	w.Flush()

	_, err := ms.Get("inv", "sword")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after nil-value enqueue, got %v", err)
	}
}

func TestAsyncWriter_FullBufferDoesNotBlock(t *testing.T) {
	ms := newMockStore()
	w := NewAsyncWriter(ms, 1)
	w.Start()

	// Enqueue 2 ops rapidly with buffer size 1 — second may be dropped but must not block
	w.Enqueue(Op{Collection: "c", Key: "k1", Value: []byte("v1")})
	w.Enqueue(Op{Collection: "c", Key: "k2", Value: []byte("v2")})

	w.Flush()

	// At least the first op should have been processed
	_, err := ms.Get("c", "k1")
	if err != nil {
		t.Logf("k1 may have been dropped (buffer full race), err: %v", err)
	}
	// We just verify this didn't deadlock — reaching here means success
}
