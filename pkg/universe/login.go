package universe

import (
	"errors"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/net"
)

// ErrLoginPending re-exports the engine's ErrLoginPending for use in LoginHandler implementations.
var ErrLoginPending = engine.ErrLoginPending

// LoginHandler parses login messages and returns the username (and optional
// session data). Returns ErrLoginPending if no valid login message found yet.
// Returns other errors for rejected logins (error message sent to client).
// The optional data is stored in PlayerSession.Data on the target node.
type LoginHandler func(connID uint32, messages [][]byte) (username string, data any, err error)

// PlayerRouter determines which node should host a player after login.
// Called with the authenticated username. Returns a nodeID.
type PlayerRouter func(username string) string

// loginService manages pre-node login processing on the coordinator.
type loginService struct {
	handler      LoginHandler
	onRejected   func(connID uint32, reason string)
	loginTimeout time.Duration
	pending      map[uint32]*pendingConn
}

type pendingConn struct {
	connID    uint32
	createdAt time.Time
}

func newLoginService(handler LoginHandler, timeout time.Duration) *loginService {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &loginService{
		handler:      handler,
		loginTimeout: timeout,
		pending:      make(map[uint32]*pendingConn),
	}
}

func (ls *loginService) addPending(connID uint32) {
	ls.pending[connID] = &pendingConn{
		connID:    connID,
		createdAt: time.Now(),
	}
}

func (ls *loginService) removePending(connID uint32) {
	delete(ls.pending, connID)
}

// loginResult is returned by processLogins for each successfully authenticated player.
type loginResult struct {
	connID   uint32
	username string
	data     any // optional session data from LoginHandler
}

// processLogins drains input for all pending connections and attempts login.
// Returns successfully authenticated players. Removes timed-out connections.
func (ls *loginService) processLogins(connMgr *net.ConnManager) (results []loginResult, timedOut []uint32) {
	now := time.Now()
	for connID, pc := range ls.pending {
		// Check timeout
		if now.Sub(pc.createdAt) > ls.loginTimeout {
			timedOut = append(timedOut, connID)
			delete(ls.pending, connID)
			continue
		}

		msgs := connMgr.DrainInput(connID)
		if len(msgs) == 0 {
			continue
		}

		username, data, err := ls.handler(connID, msgs)
		if err != nil {
			if errors.Is(err, ErrLoginPending) {
				continue
			}
			// Login rejected
			if ls.onRejected != nil {
				ls.onRejected(connID, err.Error())
			}
			delete(ls.pending, connID)
			continue
		}

		results = append(results, loginResult{connID: connID, username: username, data: data})
		delete(ls.pending, connID)
	}
	return results, timedOut
}

func (ls *loginService) pendingCount() int {
	return len(ls.pending)
}
