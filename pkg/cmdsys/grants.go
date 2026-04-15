package cmdsys

import (
	"fmt"
	"strings"
	"sync"
)

// Parse parses a single grant line in the form "[+|-]pattern".
// A leading "-" means deny; anything else (including "+") means allow.
// The special pattern "*.*" is rejected — use NewOperatorIdentity for that.
func Parse(line string) (Grant, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Grant{}, fmt.Errorf("cmdsys.Parse: empty grant line")
	}
	allow := true
	if strings.HasPrefix(line, "-") {
		allow = false
		line = line[1:]
	} else if strings.HasPrefix(line, "+") {
		line = line[1:]
	}
	if line == "*.*" {
		return Grant{}, ErrRefuseGlobalWildcard
	}
	return Grant{Pattern: line, Allow: allow}, nil
}

// GrantStore is the interface for looking up grants for a caller.
type GrantStore interface {
	Grants(callerID string) []Grant
	Set(callerID string, grants []Grant)
}

// InMemoryGrantStore is a thread-safe in-memory GrantStore.
type InMemoryGrantStore struct {
	mu     sync.RWMutex
	grants map[string][]Grant
}

// NewInMemoryGrantStore creates an empty InMemoryGrantStore.
func NewInMemoryGrantStore() *InMemoryGrantStore {
	return &InMemoryGrantStore{grants: make(map[string][]Grant)}
}

// Grants returns the grants for callerID. Returns nil if none are stored.
func (s *InMemoryGrantStore) Grants(callerID string) []Grant {
	s.mu.RLock()
	g := s.grants[callerID]
	s.mu.RUnlock()
	// return a copy so callers can't mutate stored state
	if g == nil {
		return nil
	}
	out := make([]Grant, len(g))
	copy(out, g)
	return out
}

// Set replaces the stored grants for callerID.
func (s *InMemoryGrantStore) Set(callerID string, grants []Grant) {
	s.mu.Lock()
	s.grants[callerID] = grants
	s.mu.Unlock()
}

// NewOperatorIdentity returns a Caller with the global wildcard grant
// (*.*  allow). This is the only path to a *.* grant; the grant parser
// rejects *.*  from all other callers.
func NewOperatorIdentity(id string) Caller {
	return Caller{
		ID:     id,
		Source: SourceConsole,
		Grants: []Grant{{Pattern: "*.*", Allow: true}},
	}
}
