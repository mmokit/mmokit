package universe

import (
	"errors"
	"reflect"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// Resolver-specific sentinel errors.
var (
	ErrRouteMissingField = errors.New("cmdsys: route resolver: required field missing in args")
	ErrRouteNoOwner      = errors.New("cmdsys: route resolver: no owner found for target")
)

// meshRouteResolver implements cmdsys.RouteResolver using live Process
// state for all route kinds.
type meshRouteResolver struct {
	coord *Process
}

func newMeshRouteResolver(coord *Process) *meshRouteResolver {
	return &meshRouteResolver{coord: coord}
}

// Resolve maps a RouteKind to a slice of Targets using live coordinator state.
// Context-sensitive routes (RoutePlayerOwner, RouteEntityOwner,
// RouteSpecificHost, RouteSpecificCell) extract the target identifier
// from args by field name via reflection.
func (r *meshRouteResolver) Resolve(route cmdsys.RouteKind, verb string, args any) ([]cmdsys.Target, error) {
	switch route {
	case cmdsys.RouteLocal:
		return []cmdsys.Target{{Kind: cmdsys.RouteLocal, ID: "local"}}, nil

	case cmdsys.RouteCoordinator:
		// The coordinator is always in-process (this resolver only runs on the
		// coordinator process). Route to "local" so the dispatcher executes directly.
		return []cmdsys.Target{{Kind: cmdsys.RouteLocal, ID: "local"}}, nil

	case cmdsys.RouteAllHosts:
		ids := r.coord.LiveHostIDs()
		if len(ids) == 0 {
			// Fall back to direct local execution when no remote hosts are
			// registered (single-process `all` preset with no remote nodes).
			// Keeps cluster.overview + *.live working in simple-dev mode
			// instead of hard-failing on ErrRouteNoOwner.
			return []cmdsys.Target{{Kind: cmdsys.RouteLocal, ID: "local"}}, nil
		}
		out := make([]cmdsys.Target, len(ids))
		for i, id := range ids {
			out[i] = cmdsys.Target{Kind: cmdsys.RouteAllHosts, ID: id}
		}
		return out, nil

	case cmdsys.RouteAllGateways:
		ids := r.coord.LiveGatewayIDs()
		if len(ids) == 0 {
			return nil, ErrRouteNoOwner
		}
		out := make([]cmdsys.Target, len(ids))
		for i, id := range ids {
			out[i] = cmdsys.Target{Kind: cmdsys.RouteAllGateways, ID: id}
		}
		return out, nil

	case cmdsys.RoutePlayerOwner:
		username := extractStringField(args, "Username")
		if username == "" {
			return nil, ErrRouteMissingField
		}
		hostID := r.coord.ActiveUserHost(username)
		if hostID == "" {
			return nil, ErrRouteNoOwner
		}
		return []cmdsys.Target{{Kind: cmdsys.RoutePlayerOwner, ID: hostID}}, nil

	case cmdsys.RouteEntityOwner:
		netID := extractUint32Field(args, "NetID")
		hostID := r.coord.EntityHostForNetID(netID)
		if hostID == "" {
			return nil, ErrRouteNoOwner
		}
		return []cmdsys.Target{{Kind: cmdsys.RouteEntityOwner, ID: hostID}}, nil

	case cmdsys.RouteSpecificHost:
		hostID := extractStringField(args, "HostID")
		if hostID == "" {
			return nil, ErrRouteMissingField
		}
		return []cmdsys.Target{{Kind: cmdsys.RouteSpecificHost, ID: hostID}}, nil

	case cmdsys.RouteSpecificCell:
		cellID := extractStringField(args, "CellID")
		if cellID == "" {
			return nil, ErrRouteMissingField
		}
		// Accept both "0_0" and "cell_0_0" (and dN_X_Y / cell_dN_X_Y
		// variants). ParseCellID handles all four forms; MeshCellID
		// canonicalizes back to the cell_* key used by HostForCellID.
		// Keeps bot.spawn / future RouteSpecificCell commands consistent
		// with cell.split / cell.merge / cell.migrate, which already
		// normalize this way.
		lookup := cellID
		if parsed, err := ParseCellID(cellID); err == nil {
			lookup = MeshCellID(parsed)
		}
		hostID := r.coord.HostForCellID(lookup)
		if hostID == "" {
			return nil, ErrRouteNoOwner
		}
		return []cmdsys.Target{{Kind: cmdsys.RouteSpecificCell, ID: hostID}}, nil

	default:
		return nil, cmdsys.ErrNotYetWired
	}
}

// ── reflection helpers ────────────────────────────────────────────────────────

// extractStringField walks the struct (by value or pointer) looking for a
// string field with the given name. Returns "" if not found or not a string.
func extractStringField(args any, name string) string {
	if args == nil {
		return ""
	}
	rv := reflect.ValueOf(args)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	f := rv.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

// extractUint32Field walks the struct looking for a uint32 field with the
// given name. Returns 0 if not found.
func extractUint32Field(args any, name string) uint32 {
	if args == nil {
		return 0
	}
	rv := reflect.ValueOf(args)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0
	}
	f := rv.FieldByName(name)
	if !f.IsValid() {
		return 0
	}
	switch f.Kind() {
	case reflect.Uint32:
		return uint32(f.Uint())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint64:
		return uint32(f.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint32(f.Int())
	}
	return 0
}
