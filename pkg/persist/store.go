package persist

import "errors"

// ErrNotFound is returned when a key does not exist in the store.
var ErrNotFound = errors.New("not found")

// Store is the interface for key-value persistence.
// collection maps to: bbolt bucket, ScyllaDB table, Redis key prefix.
type Store interface {
	Get(collection, key string) ([]byte, error)
	Put(collection, key string, value []byte) error
	Delete(collection, key string) error
	ForEach(collection string, fn func(key string, value []byte) error) error
	Close() error
}

// Op represents a single write or delete operation.
type Op struct {
	Collection string
	Key        string
	Value      []byte // nil = delete
}
