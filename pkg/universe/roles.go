package universe

import (
	"fmt"
	"strings"
)

// Role identifies an individual responsibility a process can run.
// A process has a set of roles (Roles) expressed as a bitmask.
type Role uint8

const (
	// RoleCoordinator runs the MeshControl gRPC server, HostRegistry,
	// AssignmentEngine, and admin console. Holds no local cells by itself;
	// pair with RoleHost to also own cells.
	RoleCoordinator Role = 1 << iota

	// RoleHost owns cells. In-process when paired with RoleCoordinator;
	// remote when used alone with Config.CoordinatorAddr set — the host
	// dials that coordinator, registers via MeshControl, and receives
	// cell assignments dynamically.
	RoleHost

	// RoleGateway terminates WebSocket connections and proxies client I/O.
	// Can stand alone (standalone gateway binary) or pair with RoleCoordinator
	// (embedded gateway).
	RoleGateway
)

// PresetAll is the default role set: coordinator + host + gateway.
// Expressed on the CLI as `--mode=all` (or omitted, since `all` is the
// default). Single-process dev server running every role in one binary.
const PresetAll Roles = Roles(RoleCoordinator | RoleHost | RoleGateway)

// Roles is a bitmask set of Role values.
type Roles uint8

// Has reports whether r contains the given role.
func (r Roles) Has(role Role) bool { return uint8(r)&uint8(role) != 0 }

// IsEmpty reports whether the set contains no roles.
func (r Roles) IsEmpty() bool { return r == 0 }

// String returns a human-readable comma-separated role list, e.g. "coordinator,host,gateway".
func (r Roles) String() string {
	if r.IsEmpty() {
		return "(empty)"
	}
	var parts []string
	if r.Has(RoleCoordinator) {
		parts = append(parts, "coordinator")
	}
	if r.Has(RoleHost) {
		parts = append(parts, "host")
	}
	if r.Has(RoleGateway) {
		parts = append(parts, "gateway")
	}
	return strings.Join(parts, ",")
}

// ParseRoles turns a CLI string into a Roles bitmask. Accepts:
//   - "" → PresetAll (coordinator|host|gateway) — default when --mode is omitted
//   - "all" → PresetAll
//   - comma-separated list of role names (whitespace-tolerant): "coordinator",
//     "coordinator,gateway", "coordinator,host,gateway", "host", "gateway"
//
// Bare "host" parses successfully here — it represents a remote host that
// dials a coordinator. Process.Build() enforces that bare "host"
// requires Config.CoordinatorAddr.
//
// Returns an error only for unknown tokens.
func ParseRoles(s string) (Roles, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all" {
		return PresetAll, nil
	}

	var roles Roles
	for _, token := range strings.Split(s, ",") {
		token = strings.TrimSpace(token)
		switch token {
		case "coordinator":
			roles |= Roles(RoleCoordinator)
		case "host":
			roles |= Roles(RoleHost)
		case "gateway":
			roles |= Roles(RoleGateway)
		case "node":
			return 0, fmt.Errorf(`"--mode=node" is removed; use "--mode=host --coordinator-addr=HOST:PORT"`)
		case "":
			// ignore empty tokens (trailing comma etc.)
		default:
			return 0, fmt.Errorf("unknown role %q (valid: coordinator, host, gateway, all)", token)
		}
	}

	if roles.IsEmpty() {
		return PresetAll, nil
	}

	return roles, nil
}
