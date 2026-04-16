package cmdsys

// Target is a single resolved dispatch destination.
type Target struct {
	Kind RouteKind
	ID   string
}

// RouteResolver maps a RouteKind to a set of Targets for a given command.
// The parsed args struct is passed so context-sensitive routes
// (RoutePlayerOwner, RouteEntityOwner, RouteSpecificHost, RouteSpecificCell)
// can extract the target identifier. Static routes ignore args.
type RouteResolver interface {
	Resolve(route RouteKind, verb string, args any) ([]Target, error)
}

// localResolver handles RouteLocal; all other routes return ErrNotYetWired.
type localResolver struct{}

// NewLocalResolver returns a RouteResolver that handles RouteLocal and
// returns ErrNotYetWired for all other routes. Default for dispatchers
// constructed without an explicit resolver (unit tests).
func NewLocalResolver() RouteResolver { return localResolver{} }

func (localResolver) Resolve(route RouteKind, _ string, _ any) ([]Target, error) {
	if route == RouteLocal {
		return []Target{{Kind: RouteLocal, ID: "local"}}, nil
	}
	return nil, ErrNotYetWired
}

// stubResolver returns ErrNotYetWired for every route including local.
// Used in tests that need to assert the not-yet-wired error path.
type stubResolver struct{}

func (stubResolver) Resolve(_ RouteKind, _ string, _ any) ([]Target, error) {
	return nil, ErrNotYetWired
}
