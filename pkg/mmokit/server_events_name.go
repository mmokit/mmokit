package mmokit

import "strings"

var eventCodePrefixes = []string{"SE_", "GSE_", "CE_", "GCE_", "SSE_", "BCE_"}

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
