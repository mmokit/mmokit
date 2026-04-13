package persist

import "errors"

// ErrNotFound is returned when a Load call cannot find a record.
// Callers should test with errors.Is(err, persist.ErrNotFound).
var ErrNotFound = errors.New("persist: record not found")
