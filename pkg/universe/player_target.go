package universe

import (
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

// PlayerTarget is the result of ResolvePlayerTarget. Exactly one of Online
// or Offline is non-nil when the player exists; both are nil when the
// player is unknown to this process.
type PlayerTarget struct {
	Username  string
	Stage     *Stage // non-nil iff Online != nil
	Online    *engine.PlayerSession
	Offline   PlayerDataAccessor // non-nil iff persisted state was found here
	DirtyMark func()             // call after mutating Offline (no-op when Online or NotFound)
}

// PlayerDataAccessor is the minimal surface ResolvePlayerTarget exposes
// for offline players. The game's persisted PlayerData satisfies it via
// a thin accessor shim. Universe stays game-agnostic — the game installs
// an implementation via Process.SetPlayerDataLocator.
type PlayerDataAccessor interface {
	GetUsername() string
	GetCellX() int32
	GetCellY() int32
	GetX() float32
	GetY() float32
	SetCell(cellX, cellY int32)
	SetPosition(x, y float32)
}

// PlayerDataLocator is the universe-side hook that game code installs at
// startup. universe never reaches into a concrete PlayerRepo — it only
// uses this interface, which the game's repo adapts to.
//
// Get returns the persisted accessor for username plus a DirtyMark
// closure to call after mutating it. Returning (nil, _, true) is treated
// identically to (_, _, false) — the caller falls through to NotFound.
//
// ListOffline returns every persisted player accessible to this locator.
// Used by the player.list --all internal fan-out (player.list_offline).
// Implementations may return a snapshot; callers must not mutate entries
// without calling the corresponding DirtyMark from Get.
type PlayerDataLocator interface {
	Get(username string) (PlayerDataAccessor, func(), bool)
	ListOffline() []PlayerDataAccessor
}

// ResolvePlayerTarget looks up the named user across local cells (online
// branch) and falls back to the registered PlayerDataLocator (offline
// branch). Returns a NotFound zero-value PlayerTarget when neither
// branch hits. DirtyMark is always non-nil — a no-op closure when there
// is nothing to mark dirty.
func ResolvePlayerTarget(env *cmdsys.Env, username string) PlayerTarget {
	username = strings.ToLower(username)
	t := PlayerTarget{Username: username, DirtyMark: func() {}}
	if env == nil || env.Local == nil || env.Local.Process == nil {
		return t
	}
	proc, ok := env.Local.Process.(*Process)
	if !ok {
		return t
	}
	for _, cell := range proc.Cells {
		if cell == nil || cell.Engine == nil || cell.Engine.Players == nil || cell.Stage == nil {
			continue
		}
		if sess := cell.Engine.Players.ByUsername(username); sess != nil {
			t.Stage = cell.Stage
			t.Online = sess
			return t
		}
	}
	proc.mu.RLock()
	loc := proc.playerDataLocator
	proc.mu.RUnlock()
	if loc != nil {
		if data, mark, ok := loc.Get(username); ok && data != nil {
			t.Offline = data
			if mark != nil {
				t.DirtyMark = mark
			}
			return t
		}
	}
	return t
}

// SetPlayerDataLocator installs the offline-player lookup hook. Called
// by game bootstrap after PlayerRepo is constructed (typically from
// cmd/server/main.go). Idempotent; subsequent calls replace the hook.
// nil is allowed (disables offline lookups).
func (c *Process) SetPlayerDataLocator(loc PlayerDataLocator) {
	c.mu.Lock()
	c.playerDataLocator = loc
	c.mu.Unlock()
}
