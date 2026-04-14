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

	// RoleHost creates in-process cells with static (pre-Build) assignment.
	// Requires RoleCoordinator — a host needs a control plane.
	RoleHost

	// RoleGateway terminates WebSocket connections and proxies client I/O.
	// Can stand alone (standalone gateway binary) or pair with RoleCoordinator
	// (embedded gateway).
	RoleGateway

	// RoleNode dials a remote coordinator, registers via MeshControl, and
	// receives cell assignments dynamically. Cannot combine with any other role.
	RoleNode
)

// PresetAllInOne is the default role set: coordinator + host + gateway.
// Equivalent to the classic "all-in-one" mode: single-process dev server.
const PresetAllInOne Roles = Roles(RoleCoordinator | RoleHost | RoleGateway)

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
	if r.Has(RoleNode) {
		parts = append(parts, "node")
	}
	return strings.Join(parts, ",")
}

// ParseRoles turns a CLI string into a Roles bitmask and validates the
// combination. Accepts:
//   - "" → PresetAllInOne (coordinator|host|gateway)
//   - "all-in-one" → PresetAllInOne
//   - comma-separated list of role names (whitespace-tolerant): "coordinator",
//     "coordinator,gateway", "coordinator,host,gateway", "node", "gateway"
//
// Combination rules:
//   - "node" cannot combine with any other role
//   - "host" requires "coordinator"
//   - "gateway" can stand alone or pair with "coordinator"
//   - "coordinator" can stand alone
//
// Returns an error for unknown tokens or invalid combinations.
func ParseRoles(s string) (Roles, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "all-in-one" {
		return PresetAllInOne, nil
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
			roles |= Roles(RoleNode)
		case "":
			// ignore empty tokens (trailing comma etc.)
		default:
			return 0, fmt.Errorf("unknown role %q (valid: coordinator, host, gateway, node, all-in-one)", token)
		}
	}

	if roles.IsEmpty() {
		return PresetAllInOne, nil
	}

	// Validate combination rules.
	if roles.Has(RoleNode) && roles != Roles(RoleNode) {
		return 0, fmt.Errorf("role %q cannot combine with other roles", "node")
	}
	if roles.Has(RoleHost) && !roles.Has(RoleCoordinator) {
		return 0, fmt.Errorf("role %q requires %q", "host", "coordinator")
	}

	return roles, nil
}
