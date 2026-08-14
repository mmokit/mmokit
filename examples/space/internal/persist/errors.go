package persist

import "errors"

// ErrNotFound is returned by repository Load* methods when the
// requested key does not exist. Identical sentinel semantics to
// pkg/persist.ErrNotFound but kept separate so engine and game
// callers don't have to share an import.
var ErrNotFound = errors.New("persist: not found")
