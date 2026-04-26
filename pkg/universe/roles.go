package universe

import (
	"fmt"
	"sort"
	"strings"
)

// Role identifies an individual responsibility a process can run, by name.
// Roles are open-set string keys (no bitmask cap) so the framework can be
// extended without touching this file. The four built-ins are the only
// engine roles; service kinds plug into the dedicated RoleService role.
type Role = string

const (
	// RoleCoordinator runs the MeshControl gRPC server, HostRegistry,
	// AssignmentEngine, and admin console. Holds no local cells by itself;
	// pair with RoleHost to also own cells.
	RoleCoordinator Role = "coordinator"

	// RoleHost owns cells. In-process when paired with RoleCoordinator;
	// remote when used alone with Config.CoordinatorAddr set — the host
	// dials that coordinator, registers via MeshControl, and receives
	// cell assignments dynamically.
	RoleHost Role = "host"

	// RoleGateway terminates WebSocket connections and proxies client I/O.
	// Can stand alone (standalone gateway binary) or pair with RoleCoordinator
	// (embedded gateway).
	RoleGateway Role = "gateway"

	// RoleService runs game-defined service kinds (chat, market, etc.)
	// declared via Config.ServiceKinds. Kind-agnostic at compile time:
	// the gateway and coordinator never reference a kind by name in code.
	RoleService Role = "service"
)

// Roles is a set of Role values.
type Roles map[string]struct{}

// PresetAll is the default role set: coordinator + host + gateway.
// Service is opt-in (not in the default preset) to keep dev-server
// semantics stable. Expressed on the CLI as `--mode=all` (or omitted).
func PresetAll() Roles {
	return Roles{
		RoleCoordinator: {},
		RoleHost:        {},
		RoleGateway:     {},
	}
}

// Has reports whether r contains the given role.
func (r Roles) Has(role Role) bool {
	if r == nil {
		return false
	}
	_, ok := r[role]
	return ok
}

// Add inserts a role into the set.
func (r Roles) Add(role Role) {
	r[role] = struct{}{}
}

// IsEmpty reports whether the set contains no roles.
func (r Roles) IsEmpty() bool { return len(r) == 0 }

// Equal reports whether two role sets contain exactly the same keys.
func (r Roles) Equal(other Roles) bool {
	if len(r) != len(other) {
		return false
	}
	for k := range r {
		if _, ok := other[k]; !ok {
			return false
		}
	}
	return true
}

// String returns a human-readable comma-separated role list,
// e.g. "coordinator,gateway,host". Sorted for stability.
func (r Roles) String() string {
	if r.IsEmpty() {
		return "(empty)"
	}
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

var validRoles = map[string]struct{}{
	RoleCoordinator: {},
	RoleHost:        {},
	RoleGateway:     {},
	RoleService:     {},
}

// ParseRoles turns a CLI string into a Roles set. Accepts:
//   - "" → PresetAll() — default when --mode is omitted
//   - "all" → PresetAll()
//   - comma-separated list of role names (whitespace-tolerant): "coordinator",
//     "coordinator,gateway", "coordinator,host,gateway", "host", "gateway",
//     "service", or any combination.
//
// Bare "host" parses successfully here — it represents a remote host that
// dials a coordinator. Process.Build() enforces that bare "host"
// requires Config.CoordinatorAddr.
//
// Returns an error only for unknown tokens.
func ParseRoles(s string) (Roles, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return PresetAll(), nil
	}

	roles := Roles{}
	for _, token := range strings.Split(s, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if token == "node" {
			return nil, fmt.Errorf(`"--mode=node" is removed; use "--mode=host --coordinator-addr=HOST:PORT"`)
		}
		if _, ok := validRoles[token]; !ok {
			return nil, fmt.Errorf("unknown role %q (valid: coordinator, host, gateway, service, all)", token)
		}
		roles.Add(token)
	}

	if roles.IsEmpty() {
		return PresetAll(), nil
	}

	return roles, nil
}
