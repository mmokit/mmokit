package persist

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BoltStore implements Store backed by a BBolt database file.
type BoltStore struct {
	db *bolt.DB
}

// OpenBolt opens (or creates) a BBolt database at the given path.
func OpenBolt(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("bbolt open %s: %w", path, err)
	}
	return &BoltStore{db: db}, nil
}

func (s *BoltStore) Get(collection, key string) ([]byte, error) {
	var result []byte
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(collection))
		if b == nil {
			return ErrNotFound
		}
		v := b.Get([]byte(key))
		if v == nil {
			return ErrNotFound
		}
		result = make([]byte, len(v))
		copy(result, v)
		return nil
	})
	return result, err
}

func (s *BoltStore) Put(collection, key string, value []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(collection))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), value)
	})
}

func (s *BoltStore) Delete(collection, key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(collection))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

func (s *BoltStore) ForEach(collection string, fn func(key string, value []byte) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(collection))
		if b == nil {
			return nil // no bucket = no data, not an error
		}
		return b.ForEach(func(k, v []byte) error {
			return fn(string(k), v)
		})
	})
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}
