package mmokit

import (
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/pkg/engine"
)

var eventCodePrefixes = []string{"SE_", "GSE_", "CE_", "GCE_", "SSE_", "BCE_"}

// enumConstantName returns the proto enum constant name (e.g. "SE_PLAYER_SPAWNED")
// for a typed enum value. Falls back to the numeric form if the type doesn't
// implement the Stringer interface (which all proto enums do via their generated
// String() method).
func enumConstantName[C engine.EventCode](code C) string {
	type stringer interface{ String() string }
	if s, ok := any(code).(stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%d", code)
}

// deriveEventName converts a proto enum constant name like SE_PLAYER_SPAWNED
// into a camelCase SDK method name like "playerSpawned". Strips known event-
// code prefixes and lowercases the first word.
func deriveEventName(constName string) string {
	s := constName
	for _, p := range eventCodePrefixes {
		if after, ok := strings.CutPrefix(s, p); ok {
			s = after
			break
		}
	}
	parts := strings.Split(s, "_")
	var b strings.Builder
	first := true
	for _, part := range parts {
		if part == "" {
			continue
		}
		if first {
			b.WriteString(strings.ToLower(part))
			first = false
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(strings.ToLower(part[1:]))
	}
	return b.String()
}
