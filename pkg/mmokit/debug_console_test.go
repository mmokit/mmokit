package mmokit

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/persist"
)

// ── test fakes ──────────────────────────────────────────────────────────────

// fakeDebugRepo is an in-memory PlayerRepository.LoadDebugFlags +
// SaveDebugFlags pair, used to verify console handlers persist
// changes correctly.
type fakeDebugRepo struct {
	mu       sync.Mutex
	flags    map[string][]string
	notFound map[string]bool // usernames to surface as ErrNotFound
}

func newFakeDebugRepo() *fakeDebugRepo {
	return &fakeDebugRepo{
		flags:    make(map[string][]string),
		notFound: make(map[string]bool),
	}
}

func (f *fakeDebugRepo) LoadDebugFlags(_ context.Context, username string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notFound[username] {
		return nil, persist.ErrNotFound
	}
	cur := f.flags[username]
	if cur == nil {
		// no row exists at all
		return nil, persist.ErrNotFound
	}
	out := make([]string, len(cur))
	copy(out, cur)
	return out, nil
}

func (f *fakeDebugRepo) SaveDebugFlags(_ context.Context, username string, flags []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if flags == nil {
		f.flags[username] = []string{}
	} else {
		copyFlags := make([]string, len(flags))
		copy(copyFlags, flags)
		f.flags[username] = copyFlags
	}
	delete(f.notFound, username)
	return nil
}

// fakeSessionResolver tracks active sessions by username + records
// SendDebugClear calls for assertion.
type fakeSessionResolver struct {
	sessions   map[string]*engine.PlayerSession
	clearCalls []uint32
}

func newFakeSessionResolver() *fakeSessionResolver {
	return &fakeSessionResolver{sessions: make(map[string]*engine.PlayerSession)}
}

func (r *fakeSessionResolver) addSession(username string, connID uint32, flags engine.DebugFlag) *engine.PlayerSession {
	s := &engine.PlayerSession{
		Username:   username,
		ConnID:     connID,
		DebugFlags: flags,
	}
	r.sessions[username] = s
	return s
}

func (r *fakeSessionResolver) SessionByUsername(username string) *engine.PlayerSession {
	return r.sessions[username]
}

func (r *fakeSessionResolver) SendDebugClear(connID uint32) {
	r.clearCalls = append(r.clearCalls, connID)
}

// ── test-flag setup ─────────────────────────────────────────────────────────

// debug_console_test relies on at least two registered flags so the
// "all" path can be exercised. Topology is always registered by
// engine.init(); perf is registered once on first test run with a
// recover guard for re-runs in the same process.
const testPerfFlag = "debug_console_test_perf"

var registerTestFlagsOnce sync.Once

func ensureTestFlagsRegistered(t *testing.T) {
	t.Helper()
	registerTestFlagsOnce.Do(func() {
		defer func() {
			// If perf is already registered (e.g. another test in the
			// same package registered it first), swallow the panic —
			// we just need it present, not freshly registered.
			_ = recover()
		}()
		engine.RegisterDebugFlag(testPerfFlag, 6, "test-only debug flag for the debug_console suite")
	})
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestDebugGrant_AddsFlagToDBAndSession(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()
	resolver.addSession("alice", 42, 0)

	err := debugGrantHandler(context.Background(), repo, resolver, DebugGrantArgs{
		Username: "alice",
		Flag:     "topology",
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	if len(got) != 1 || got[0] != "topology" {
		t.Errorf("repo: got %v, want [topology]", got)
	}
	sess := resolver.SessionByUsername("alice")
	if !sess.HasDebug(engine.DebugTopology) {
		t.Errorf("session DebugFlags should include DebugTopology, got 0b%b", sess.DebugFlags)
	}
}

func TestDebugGrant_AllAddsEveryRegisteredFlag(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()
	sess := resolver.addSession("alice", 42, 0)

	err := debugGrantHandler(context.Background(), repo, resolver, DebugGrantArgs{
		Username: "alice",
		Flag:     "all",
	})
	if err != nil {
		t.Fatalf("grant all: %v", err)
	}

	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	want := append([]string(nil), engine.ListDebugFlags()...)
	sort.Strings(want)
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	if !sliceEqual(gotSorted, want) {
		t.Errorf("repo: got %v, want %v (every registered flag)", gotSorted, want)
	}

	// Every registered flag bit should now be set on the session.
	for _, name := range engine.ListDebugFlags() {
		bit, _ := engine.DebugFlagByName(name)
		if sess.DebugFlags&bit == 0 {
			t.Errorf("session: bit for %q not set", name)
		}
	}
}

func TestDebugGrant_UnknownFlagReturnsError(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()

	err := debugGrantHandler(context.Background(), repo, resolver, DebugGrantArgs{
		Username: "alice",
		Flag:     "no_such_flag",
	})
	if err == nil {
		t.Fatalf("expected error for unknown flag")
	}
	if !errors.Is(err, ErrUnknownDebugFlag) {
		t.Errorf("error should wrap ErrUnknownDebugFlag, got %v", err)
	}

	// Repo must remain untouched (still ErrNotFound, no save happened).
	if _, err := repo.LoadDebugFlags(context.Background(), "alice"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("repo should still be empty after rejected grant, got err=%v", err)
	}
}

func TestDebugGrant_OfflineUserPersistsToDB(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver() // no sessions

	err := debugGrantHandler(context.Background(), repo, resolver, DebugGrantArgs{
		Username: "alice",
		Flag:     "topology",
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	if len(got) != 1 || got[0] != "topology" {
		t.Errorf("repo: got %v, want [topology]", got)
	}
	// No session, no clear calls, no panic.
	if len(resolver.clearCalls) != 0 {
		t.Errorf("no clear should fire on grant, got %v", resolver.clearCalls)
	}
}

func TestDebugRevoke_RemovesFlag(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	// Pre-seed alice with both flags.
	_ = repo.SaveDebugFlags(context.Background(), "alice", []string{"topology", testPerfFlag})

	resolver := newFakeSessionResolver()
	topoBit, _ := engine.DebugFlagByName("topology")
	perfBit, _ := engine.DebugFlagByName(testPerfFlag)
	sess := resolver.addSession("alice", 42, topoBit|perfBit)

	err := debugRevokeHandler(context.Background(), repo, resolver, DebugRevokeArgs{
		Username: "alice",
		Flag:     "topology",
	})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}

	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	if len(got) != 1 || got[0] != testPerfFlag {
		t.Errorf("repo: got %v, want [%s]", got, testPerfFlag)
	}
	if sess.DebugFlags&topoBit != 0 {
		t.Errorf("session topology bit should be cleared, got 0b%b", sess.DebugFlags)
	}
	if sess.DebugFlags&perfBit == 0 {
		t.Errorf("session perf bit should remain set, got 0b%b", sess.DebugFlags)
	}
	if len(resolver.clearCalls) != 0 {
		t.Errorf("clear should not fire while flags remain, got %v", resolver.clearCalls)
	}
}

func TestDebugRevoke_AllClearsSet(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	_ = repo.SaveDebugFlags(context.Background(), "alice", []string{"topology", testPerfFlag})

	resolver := newFakeSessionResolver()
	topoBit, _ := engine.DebugFlagByName("topology")
	perfBit, _ := engine.DebugFlagByName(testPerfFlag)
	sess := resolver.addSession("alice", 42, topoBit|perfBit)

	err := debugRevokeHandler(context.Background(), repo, resolver, DebugRevokeArgs{
		Username: "alice",
		Flag:     "all",
	})
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}

	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	if len(got) != 0 {
		t.Errorf("repo: got %v, want empty", got)
	}
	if sess.DebugFlags != 0 {
		t.Errorf("session should be zeroed, got 0b%b", sess.DebugFlags)
	}
	if len(resolver.clearCalls) != 1 || resolver.clearCalls[0] != 42 {
		t.Errorf("expected single SendDebugClear(42), got %v", resolver.clearCalls)
	}
}

func TestDebugRevoke_DoesNotSendClearIfFlagsRemain(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	_ = repo.SaveDebugFlags(context.Background(), "alice", []string{"topology", testPerfFlag})

	resolver := newFakeSessionResolver()
	topoBit, _ := engine.DebugFlagByName("topology")
	perfBit, _ := engine.DebugFlagByName(testPerfFlag)
	sess := resolver.addSession("alice", 42, topoBit|perfBit)

	err := debugRevokeHandler(context.Background(), repo, resolver, DebugRevokeArgs{
		Username: "alice",
		Flag:     "topology",
	})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if sess.DebugFlags == 0 {
		t.Errorf("session should still have perf bit, got 0b%b", sess.DebugFlags)
	}
	if len(resolver.clearCalls) != 0 {
		t.Errorf("no clear should fire when flags remain, got %v", resolver.clearCalls)
	}
}

func TestDebugRevoke_UnknownFlagReturnsError(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()

	err := debugRevokeHandler(context.Background(), repo, resolver, DebugRevokeArgs{
		Username: "alice",
		Flag:     "no_such_flag",
	})
	if err == nil {
		t.Fatalf("expected error for unknown flag")
	}
	if !errors.Is(err, ErrUnknownDebugFlag) {
		t.Errorf("error should wrap ErrUnknownDebugFlag, got %v", err)
	}
}

func TestDebugFeatures_ListsRegisteredFlags(t *testing.T) {
	ensureTestFlagsRegistered(t)
	got, err := debugFeaturesHandler()
	if err != nil {
		t.Fatalf("features: %v", err)
	}
	res := got.(DebugFeaturesResult)
	if len(res.Features) < 2 {
		t.Fatalf("expected at least 2 registered flags, got %d", len(res.Features))
	}
	// Topology must be present with its production description.
	var foundTopo, foundPerf bool
	for _, row := range res.Features {
		if row.Name == "topology" {
			foundTopo = true
			if row.Description == "" {
				t.Errorf("topology description should not be empty")
			}
		}
		if row.Name == testPerfFlag {
			foundPerf = true
		}
	}
	if !foundTopo {
		t.Errorf("topology row missing from features")
	}
	if !foundPerf {
		t.Errorf("%s row missing from features", testPerfFlag)
	}
}

func TestDebugList_ReturnsNotImplemented(t *testing.T) {
	ensureTestFlagsRegistered(t)
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()
	_, err := debugListHandler(context.Background(), repo, resolver)
	if err == nil {
		t.Errorf("expected not-yet-implemented error from debug.list")
	}
}

func TestRegisterDebugCommands_RegistersAllFourVerbs(t *testing.T) {
	ensureTestFlagsRegistered(t)
	reg := cmdsys.NewRegistry()
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()

	if err := registerDebugCommandsWithDeps(reg, repo, resolver); err != nil {
		t.Fatalf("RegisterDebugCommands: %v", err)
	}
	for _, verb := range []string{"debug.grant", "debug.revoke", "debug.list", "debug.features"} {
		if _, ok := reg.Lookup(verb); !ok {
			t.Errorf("verb %q not registered", verb)
		}
	}
}

func TestRegisterDebugCommands_GrantDispatchesThroughHandler(t *testing.T) {
	ensureTestFlagsRegistered(t)
	reg := cmdsys.NewRegistry()
	repo := newFakeDebugRepo()
	resolver := newFakeSessionResolver()
	resolver.addSession("alice", 42, 0)

	if err := registerDebugCommandsWithDeps(reg, repo, resolver); err != nil {
		t.Fatalf("RegisterDebugCommands: %v", err)
	}
	cmd, ok := reg.Lookup("debug.grant")
	if !ok {
		t.Fatalf("debug.grant not registered")
	}
	_, err := cmd.Handler(context.Background(), &cmdsys.Env{}, DebugGrantArgs{
		Username: "alice",
		Flag:     "topology",
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got, _ := repo.LoadDebugFlags(context.Background(), "alice")
	if len(got) != 1 || got[0] != "topology" {
		t.Errorf("repo: got %v, want [topology]", got)
	}
}

func TestMergeFlagSet_SortsAndDedups(t *testing.T) {
	got := mergeFlagSet([]string{"b", "a"}, []string{"c", "a"})
	want := []string{"a", "b", "c"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRemoveFromSet_RemovesAndPreservesOrder(t *testing.T) {
	got := removeFromSet([]string{"a", "b", "c"}, "b")
	want := []string{"a", "c"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSliceEqual(t *testing.T) {
	if !sliceEqual(nil, nil) {
		t.Errorf("nil/nil should be equal")
	}
	if !sliceEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Errorf("equal slices should compare equal")
	}
	if sliceEqual([]string{"a"}, []string{"a", "b"}) {
		t.Errorf("different lengths should compare unequal")
	}
	if sliceEqual([]string{"a", "b"}, []string{"b", "a"}) {
		t.Errorf("different order should compare unequal")
	}
}
