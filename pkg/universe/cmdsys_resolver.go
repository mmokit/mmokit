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

// meshRouteResolver implements cmdsys.RouteResolver using live Coordinator
// state for all route kinds.
type meshRouteResolver struct {
	coord *Coordinator
}

func newMeshRouteResolver(coord *Coordinator) *meshRouteResolver {
	return &meshRouteResolver{coord: coord}
}

// Resolve maps a RouteKind to a slice of Targets using live coordinator state.
func (r *meshRouteResolver) Resolve(route cmdsys.RouteKind, verb string) ([]cmdsys.Target, error) {
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
			return nil, ErrRouteNoOwner
		}
		out := make([]cmdsys.Target, len(ids))
		for i, id := range ids {
			out[i] = cmdsys.Target{Kind: cmdsys.RouteAllHosts, ID: id}
		}
		return out, nil

	case cmdsys.RoutePlayerOwner:
		// Caller must provide Username in their args.
		return nil, cmdsys.ErrNotYetWired // resolved at Invoke time via args — handled in Invoke

	case cmdsys.RouteEntityOwner:
		return nil, cmdsys.ErrNotYetWired // resolved at Invoke time via args — handled in Invoke

	case cmdsys.RouteSpecificHost:
		return nil, cmdsys.ErrNotYetWired // resolved at Invoke time via args — handled in Invoke

	case cmdsys.RouteSpecificCell:
		return nil, cmdsys.ErrNotYetWired // resolved at Invoke time via args — handled in Invoke

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

	default:
		return nil, cmdsys.ErrNotYetWired
	}
}

// ResolveWithArgs is like Resolve but uses the concrete args struct to extract
// routing fields (Username, HostID, CellID, NetID) for context-sensitive routes.
// Called by the Coordinator's InvokeCmd helper.
func (r *meshRouteResolver) ResolveWithArgs(route cmdsys.RouteKind, args any) ([]cmdsys.Target, error) {
	switch route {
	case cmdsys.RoutePlayerOwner:
		username := extractStringField(args, "Username")
		if username == "" {
			return nil, ErrRouteMissingField
		}
		hostID := r.coord.ActiveUserNode(username)
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
		hostID := r.coord.HostForCellID(cellID)
		if hostID == "" {
			return nil, ErrRouteNoOwner
		}
		return []cmdsys.Target{{Kind: cmdsys.RouteSpecificCell, ID: hostID}}, nil

	default:
		return r.Resolve(route, "")
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
