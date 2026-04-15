package cmdsys

import "strings"

// matchScore returns the specificity score of pattern matching c.
// Returns -1 if the pattern does not match.
//
// Scoring:
//   - Literal match: 1_000_000 (always beats any wildcard)
//   - Namespace wildcard "foo.*": length of the prefix before ".*"
//   - Global wildcard "*.*": 0
//   - No match: -1
func matchScore(pattern string, c Capability) int {
	cs := string(c)
	if pattern == "*.*" {
		// global wildcard matches everything
		return 0
	}
	if pattern == cs {
		return 1_000_000
	}
	if after, ok := strings.CutSuffix(pattern, ".*"); ok {
		// namespace wildcard: "foo.*" matches "foo.anything"
		prefix := after + "."
		if strings.HasPrefix(cs, prefix) {
			return len(after)
		}
		return -1
	}
	return -1
}

// Check reports whether caller is permitted to invoke the given capability.
//
// Precedence: highest-scoring matching grant wins; on a tie, deny wins.
// No matching grant → deny.
//
// Example: grants [{"entity.*", true}, {"entity.wipe", false}] for
// capability "entity.wipe" → false (literal deny beats wildcard allow).
func Check(caller Caller, c Capability) bool {
	bestScore := -1
	bestAllow := false

	for _, g := range caller.Grants {
		score := matchScore(g.Pattern, c)
		if score < 0 {
			continue
		}
		if score > bestScore {
			bestScore = score
			bestAllow = g.Allow
		} else if score == bestScore {
			// tie: deny wins
			if !g.Allow {
				bestAllow = false
			}
		}
	}

	return bestScore >= 0 && bestAllow
}
