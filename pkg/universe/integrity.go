package universe

import (
	"fmt"
)

// InvariantMode controls how invariant violations are handled.
type InvariantMode uint8

const (
	// InvariantOff disables all invariant checking. Not recommended
	// outside microbenchmarks.
	InvariantOff InvariantMode = iota
	// InvariantLog records a violation via the commit log and the
	// InvariantViolations metric, then continues execution. Production
	// default — one latent inconsistency should not take down a shard.
	InvariantLog
	// InvariantPanic records the violation and then panics. Default for
	// tests and dev — fail loud at the point of violation rather than
	// chasing symptoms hours later.
	InvariantPanic
)

// Invariant is a named predicate over Process state. Check returns nil
// when the invariant holds and a descriptive error when it's been
// violated. The error's Error() value appears in the commit log and in
// any panic message, so it should identify enough of the offending state
// to be debuggable without extra logging.
type Invariant struct {
	Name  string
	Check func(c *Process) error
}

// CatInvariant is the logger category used for invariant-related output.
const CatInvariant = "integrity"

// CheckInvariants runs each invariant in order. On a violation it logs,
// records a commit-log event, bumps the metric, and — when mode is
// InvariantPanic — panics. Callers typically pass the default invariant
// set and a short context string identifying where the check fired
// (e.g. "commit 17 after apply-cell-to-host-map").
func (c *Process) CheckInvariants(invs []Invariant, contextMsg string) {
	if c.invariantMode == InvariantOff {
		return
	}
	for _, inv := range invs {
		if err := inv.Check(c); err != nil {
			msg := fmt.Sprintf("invariant %q violated during %s: %v",
				inv.Name, contextMsg, err)
			c.Log.Log(CatInvariant, "%s", msg)
			// commit log + metric hooks are wired in Phase C; leave
			// stubs here so this file is self-contained for now.
			if c.invariantMode == InvariantPanic {
				panic(msg)
			}
		}
	}
}
