// Package universe — debug_console.go
//
// Console commands `debug grant/revoke/list/features` for runtime
// per-player debug-flag administration. Each command is wired through
// cmdsys; grant/revoke route to the host owning the player (because
// they mutate the in-memory PlayerSession) while features/list are
// local lookups.
//
// All four commands sit behind the "admin" capability. Mutations are
// persisted to the players.debug_flags JSONB column synchronously and
// pushed onto the live session in the same call. On revoke-to-zero,
// the resolver sends a sentinel empty DebugInfoMsg so client overlays
// clear immediately.
//
// Auto-wired: registerDebugCommands is invoked from Process.Build()
// when DBStore is configured. Games never call this directly — the
// engine owns the lifecycle.

package universe

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/persist"
)

// ErrUnknownDebugFlag is returned by grant/revoke when the supplied
// flag name isn't registered. Callers can check via errors.Is.
var ErrUnknownDebugFlag = errors.New("unknown debug flag")

// DebugGrantArgs is the input to `debug.grant`. Pass "all" as Flag
// to enable every registered flag in one shot.
type DebugGrantArgs struct {
	Username string `cmd:"help=player username,complete=players"`
	Flag     string `cmd:"help=flag name to enable, or 'all' for every registered flag,complete=debug-flags"`
}

// DebugRevokeArgs is the input to `debug.revoke`. Pass "all" as Flag
// to clear every flag for the user.
type DebugRevokeArgs struct {
	Username string `cmd:"help=player username,complete=players"`
	Flag     string `cmd:"help=flag name to disable, or 'all' to clear every flag,complete=debug-flags"`
}

// DebugListArgs is the input to `debug.list` (no args).
type DebugListArgs struct{}

// DebugFeaturesArgs is the input to `debug.features` (no args).
type DebugFeaturesArgs struct{}

// DebugListResult enumerates users with active debug grants.
type DebugListResult struct {
	Users []DebugListUser `cmd:"table"`
}

// DebugListUser is one row of DebugListResult: a user, whether they
// currently have an active session, and the names of their enabled
// flags.
type DebugListUser struct {
	Username string
	Online   bool
	Flags    []string
}

// DebugFeaturesResult is the response to `debug.features` — every
// registered flag's name + description in bit-ascending order.
type DebugFeaturesResult struct {
	Features []DebugFeatureRow `cmd:"table"`
}

// DebugFeatureRow is one row of DebugFeaturesResult.
type DebugFeatureRow struct {
	Name        string
	Description string
}

// debugRepo is the subset of persist.PlayerRepository that the debug
// console handlers need. Defined here so tests can pass an in-memory
// fake without implementing the full PlayerRepository surface.
type debugRepo interface {
	LoadDebugFlags(ctx context.Context, username string) ([]string, error)
	SaveDebugFlags(ctx context.Context, username string, flags []string) error
	LoadAllDebugFlags(ctx context.Context) (map[string][]string, error)
}

// debugSessionResolver locates a live PlayerSession by username so
// grant/revoke handlers can mutate sess.DebugFlags in place. The
// per-tick debug broadcaster handles the revoke-to-zero overlay clear
// reactively (sends an empty typed DebugInfo on the transition).
type debugSessionResolver interface {
	// SessionByUsername returns the active session for the given
	// username, or nil if the user is offline.
	SessionByUsername(username string) *engine.PlayerSession
}

// GrantDebug enables a debug flag for a session both in-memory and
// in the persistent players row. Idempotent — skips the DB write
// when the flag is already in the persisted set. Use from
// OnPlayerJoin to default-grant a flag to every player (e.g. demos
// that want the topology overlay on for everyone).
//
// Returns the error from the repo write if one occurred; the
// in-memory mutation always succeeds. Safe to call when DBStore is
// nil — the in-memory bit is set and no DB call is attempted.
func GrantDebug(p *Process, sess *engine.PlayerSession, flagName string) error {
	bit, ok := engine.DebugFlagByName(flagName)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownDebugFlag, flagName)
	}
	sess.DebugFlags |= bit

	store := p.Cfg().DBStore
	if store == nil {
		return nil
	}
	repo := store.Players()
	current, err := repo.LoadDebugFlags(context.Background(), sess.Username)
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		return err
	}
	for _, n := range current {
		if n == flagName {
			return nil // already persisted
		}
	}
	return repo.SaveDebugFlags(context.Background(), sess.Username, append(current, flagName))
}

// registerDebugCommands wires `debug.grant`, `debug.revoke`,
// `debug.list`, and `debug.features` onto the process's command
// registry. Repo + resolver are sourced internally from the
// Process: the repo is `p.Cfg().DBStore.Players()`, and the resolver
// walks `p.Cells` to find live sessions and dispatches the
// sentinel-clear event via per-cell SendEvent.
//
// All four commands sit behind the "admin" capability. Auto-invoked
// from Build() when DBStore is configured.
func registerDebugCommands(p *Process) error {
	if p.Cfg().DBStore == nil {
		return errors.New("universe.registerDebugCommands: Process.Cfg().DBStore is nil")
	}
	repo := p.Cfg().DBStore.Players()
	resolver := &processDebugResolver{coord: p}
	return registerDebugCommandsWithDeps(p.CmdRegistry(), repo, resolver)
}

// registerDebugCommandsWithDeps is the dependency-injected entry
// point used by tests to pass in-memory fakes for repo and resolver.
// Production callers go through registerDebugCommands(p *Process),
// which derives both from the live Process.
func registerDebugCommandsWithDeps(reg *cmdsys.Registry, repo debugRepo, resolver debugSessionResolver) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "debug.grant",
		Capability:  "admin",
		Description: "Enable a debug flag for a player (or all flags via 'all')",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        DebugGrantArgs{},
		Result:      struct{}{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DebugGrantArgs)
			return nil, debugGrantHandler(ctx, repo, resolver, args)
		},
	}); err != nil {
		return fmt.Errorf("debug.grant: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "debug.revoke",
		Capability:  "admin",
		Description: "Disable a debug flag for a player (or clear all via 'all')",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        DebugRevokeArgs{},
		Result:      struct{}{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(DebugRevokeArgs)
			return nil, debugRevokeHandler(ctx, repo, resolver, args)
		},
	}); err != nil {
		return fmt.Errorf("debug.revoke: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "debug.list",
		Capability:  "admin",
		Description: "List players with active debug grants",
		Route:       cmdsys.RouteLocal,
		Args:        DebugListArgs{},
		Result:      DebugListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return debugListHandler(ctx, repo, resolver)
		},
	}); err != nil {
		return fmt.Errorf("debug.list: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "debug.features",
		Capability:  "admin",
		Description: "List registered debug flags with their descriptions",
		Route:       cmdsys.RouteLocal,
		Args:        DebugFeaturesArgs{},
		Result:      DebugFeaturesResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return debugFeaturesHandler()
		},
	}); err != nil {
		return fmt.Errorf("debug.features: %w", err)
	}

	return nil
}

// debugGrantHandler enables one flag (or every registered flag if
// args.Flag == "all") for the given user. Persists to DB then ORs
// the bits into the active session if one exists.
func debugGrantHandler(ctx context.Context, repo debugRepo, resolver debugSessionResolver, args DebugGrantArgs) error {
	current, err := repo.LoadDebugFlags(ctx, args.Username)
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		return err
	}

	var toAdd []string
	if args.Flag == "all" {
		toAdd = engine.ListDebugFlags()
	} else {
		if _, ok := engine.DebugFlagByName(args.Flag); !ok {
			return fmt.Errorf("%w: %q", ErrUnknownDebugFlag, args.Flag)
		}
		toAdd = []string{args.Flag}
	}

	updated := mergeFlagSet(current, toAdd)
	if !sliceEqual(updated, current) {
		if err := repo.SaveDebugFlags(ctx, args.Username, updated); err != nil {
			return err
		}
	}

	if sess := resolver.SessionByUsername(args.Username); sess != nil {
		for _, name := range toAdd {
			if bit, ok := engine.DebugFlagByName(name); ok {
				sess.DebugFlags |= bit
			}
		}
	}
	return nil
}

// debugRevokeHandler disables one flag (or clears every flag if
// args.Flag == "all") for the given user. Rebuilds sess.DebugFlags
// from the new persisted set; sends a sentinel empty DebugInfoMsg
// when the session transitions from non-zero to zero so client
// overlays clear immediately.
func debugRevokeHandler(ctx context.Context, repo debugRepo, resolver debugSessionResolver, args DebugRevokeArgs) error {
	current, err := repo.LoadDebugFlags(ctx, args.Username)
	if err != nil && !errors.Is(err, persist.ErrNotFound) {
		return err
	}

	var updated []string
	if args.Flag == "all" {
		updated = nil
	} else {
		if _, ok := engine.DebugFlagByName(args.Flag); !ok {
			return fmt.Errorf("%w: %q", ErrUnknownDebugFlag, args.Flag)
		}
		updated = removeFromSet(current, args.Flag)
	}

	if !sliceEqual(updated, current) {
		if err := repo.SaveDebugFlags(ctx, args.Username, updated); err != nil {
			return err
		}
	}

	if sess := resolver.SessionByUsername(args.Username); sess != nil {
		var newBits engine.DebugFlag
		for _, name := range updated {
			if bit, ok := engine.DebugFlagByName(name); ok {
				newBits |= bit
			}
		}
		sess.DebugFlags = newBits
		// Revoke-to-zero clear is sent by the per-tick debug broadcaster
		// when it observes the transition (see debug_broadcaster.go).
	}
	return nil
}

// debugListHandler enumerates every player with at least one debug
// flag set, sorted alphabetically by username, with each row's online
// status resolved against the live session resolver.
func debugListHandler(ctx context.Context, repo debugRepo, resolver debugSessionResolver) (any, error) {
	all, err := repo.LoadAllDebugFlags(ctx)
	if err != nil {
		return nil, err
	}
	usernames := make([]string, 0, len(all))
	for u := range all {
		usernames = append(usernames, u)
	}
	sort.Strings(usernames)
	users := make([]DebugListUser, 0, len(usernames))
	for _, u := range usernames {
		users = append(users, DebugListUser{
			Username: u,
			Online:   resolver.SessionByUsername(u) != nil,
			Flags:    all[u],
		})
	}
	return DebugListResult{Users: users}, nil
}

// debugFeaturesHandler returns every registered flag's name +
// description in bit-ascending order (matching engine.ListDebugFlags
// output).
func debugFeaturesHandler() (any, error) {
	names := engine.ListDebugFlags()
	rows := make([]DebugFeatureRow, 0, len(names))
	for _, n := range names {
		desc, _ := engine.DebugFlagDescription(n)
		rows = append(rows, DebugFeatureRow{Name: n, Description: desc})
	}
	return DebugFeaturesResult{Features: rows}, nil
}

// mergeFlagSet returns the dedup-and-sorted union of existing + toAdd.
// Used by grant to compute the new persistent flag list.
func mergeFlagSet(existing, toAdd []string) []string {
	set := make(map[string]struct{}, len(existing)+len(toAdd))
	for _, e := range existing {
		set[e] = struct{}{}
	}
	for _, a := range toAdd {
		set[a] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// removeFromSet returns existing minus target, preserving the input
// order. Used by revoke to compute the new persistent flag list.
func removeFromSet(existing []string, target string) []string {
	out := make([]string, 0, len(existing))
	for _, e := range existing {
		if e != target {
			out = append(out, e)
		}
	}
	return out
}

// sliceEqual reports whether two string slices contain the same
// elements in the same order. Used to skip a no-op SaveDebugFlags
// when grant/revoke produces no change.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// processDebugResolver is the production debugSessionResolver
// implementation that walks the Process's cells to find live sessions.
// Revoke-to-zero overlay clears are sent reactively by the per-tick
// debug broadcaster (debug_broadcaster.go) — no per-event
// sentinel push needed here.
type processDebugResolver struct {
	coord *Process
}

// SessionByUsername walks every cell looking for an active session
// with the given username. Multi-cell, single-process: O(cells × players).
func (r *processDebugResolver) SessionByUsername(username string) *engine.PlayerSession {
	for _, cell := range r.coord.Cells {
		if cell.Engine == nil || cell.Engine.Players == nil {
			continue
		}
		if s := cell.Engine.Players.ByUsername(username); s != nil {
			return s
		}
	}
	return nil
}
