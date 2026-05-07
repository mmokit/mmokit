package chat

import "time"

// timeNowMs returns the current time as Unix milliseconds. Indirection
// exists so tests can patch the package-level time source if needed
// (none do today; see Phase 7 rate-limit work for the planned override).
func timeNowMs() int64 { return time.Now().UnixMilli() }
