package universe

import (
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
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

// HandleLogin builds a LoginHandler that walks the incoming framed messages,
// decodes each as enginepb.ClientEvent, and — when it finds one whose Code
// matches the given login code — unmarshals the payload into a fresh value
// of the parameter type and invokes extract to produce the username and
// optional session data.
//
// The engine part (envelope walk, code match, payload unmarshal, ErrLoginPending
// fall-through) is boilerplate every game would otherwise re-write. Games only
// supply the field extraction + validation via extract.
//
// Type params: M is the message struct type (e.g. basicpb.LoginMsg). PM is the
// pointer form that Go infers automatically from the extract callback's
// parameter, so call sites look like:
//
//	cfg.LoginHandler = mmokit.HandleLogin(
//	    uint32(basicpb.ClientEventCode_BCE_LOGIN),
//	    func(m *basicpb.LoginMsg) (string, any, error) {
//	        name, err := mmokit.ValidateUsername(m.Name, 20)
//	        return name, nil, err
//	    },
//	)
//
// extract runs exactly once per matching login message; its return values are
// propagated as-is. Non-matching codes and malformed frames are skipped
// silently — the handler returns ErrLoginPending if none of the messages in
// the batch are a valid login, which keeps the connection in the pending
// queue for the next tick.
func HandleLogin[M any, PM interface {
	*M
	proto.Message
}](code uint32, extract func(PM) (string, any, error)) LoginHandler {
	return func(connID uint32, messages [][]byte) (string, any, error) {
		for _, data := range messages {
			var evt enginepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}
			if evt.Code != code {
				continue
			}
			var msg M
			pm := PM(&msg)
			if err := proto.Unmarshal(evt.Data, pm); err != nil {
				continue
			}
			return extract(pm)
		}
		return "", nil, ErrLoginPending
	}
}

// ValidateUsername normalizes and bounds-checks a raw username: trims
// leading/trailing whitespace, lowercases, and rejects empty names as well
// as names longer than maxLen when maxLen > 0. On failure returns
// ErrLoginPending so the engine keeps waiting for a valid message instead of
// disconnecting the client — matches the "continue" behavior every game's
// hand-rolled handler used before this helper existed.
//
// The engine forces usernames to lowercase everywhere; games that don't want
// normalization can skip this helper entirely and do their own check.
func ValidateUsername(raw string, maxLen int) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", ErrLoginPending
	}
	if maxLen > 0 && len(name) > maxLen {
		return "", ErrLoginPending
	}
	return name, nil
}

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
