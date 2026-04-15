package cmdsys

// Target is a single resolved dispatch destination.
type Target struct {
	Kind RouteKind
	ID   string
}

// RouteResolver maps a RouteKind to a set of Targets for a given command.
type RouteResolver interface {
	Resolve(route RouteKind, verb string) ([]Target, error)
}

// localResolver handles RouteLocal; all other routes return ErrNotYetWired.
type localResolver struct{}

// NewLocalResolver returns a RouteResolver that handles RouteLocal and
// returns ErrNotYetWired for all other routes. Suitable for C1/C2.
func NewLocalResolver() RouteResolver { return localResolver{} }

func (localResolver) Resolve(route RouteKind, _ string) ([]Target, error) {
	if route == RouteLocal {
		return []Target{{Kind: RouteLocal, ID: "local"}}, nil
	}
	return nil, ErrNotYetWired
}

// stubResolver returns ErrNotYetWired for every route including local.
// Used in tests that need to assert the not-yet-wired error path.
type stubResolver struct{}

func (stubResolver) Resolve(_ RouteKind, _ string) ([]Target, error) {
	return nil, ErrNotYetWired
}
