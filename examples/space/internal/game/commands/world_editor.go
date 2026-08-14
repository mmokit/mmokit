package commands

import (
	"sync"

	"github.com/mmokit/mmokit/examples/space/internal/game"
	"github.com/mmokit/mmokit/examples/space/internal/world"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// worldEditor holds the coord-level WorldRepo + cached Snapshot for the
// world.* cmdsys verbs. Decoupled from *game.GameWorld so the verbs work
// on a pure coordinator process (no local cells, hence no GameWorld).
//
// snap is a pointer reference that's swapped wholesale on every reload —
// readers must call getSnap() to grab a stable pointer per call. Mutations
// go through reload(coord *mmokit.Process) which rebuilds from disk AND
// propagates to every local GameWorld so the cell-bootstrap path stays
// consistent.
type worldEditor struct {
	repo world.Repository
	mu   sync.RWMutex
	snap *world.Snapshot
}

func newWorldEditor(repo world.Repository, initial *world.Snapshot) *worldEditor {
	return &worldEditor{repo: repo, snap: initial}
}

// getSnap returns the current in-memory snapshot. The returned pointer is
// safe to read; if you need to mutate, call reload() afterwards to pick
// up the new state.
func (we *worldEditor) getSnap() *world.Snapshot {
	we.mu.RLock()
	defer we.mu.RUnlock()
	return we.snap
}

// reload re-reads the manifest from disk, installs it on this worldEditor,
// and propagates to every locally-hosted GameWorld so the cell-bootstrap
// + per-cell read paths see the new state. Silent no-op on read failure;
// the on-disk mutation has already landed and a partial in-memory rollback
// is more confusing than a stale snapshot.
func (we *worldEditor) reload(coord *mmokit.Process) {
	snap, err := we.repo.LoadAll()
	if err != nil {
		return
	}
	we.mu.Lock()
	we.snap = snap
	we.mu.Unlock()
	for _, cell := range coord.Cells {
		if cell == nil || cell.Stage == nil {
			continue
		}
		if other := mmokit.State[game.GameWorld](cell.Stage); other != nil {
			other.WorldSnapshot = snap
		}
	}
}
