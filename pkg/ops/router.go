package ops

import (
	"context"
	"log"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/net"
)

// OpContext provides identity and connection info to operation handlers.
type OpContext struct {
	ConnID   uint32
	Username string
}

// OperationHandler processes an operation request and returns the response payload (or error).
type OperationHandler func(ctx *OpContext, payload []byte) (proto.Message, error)

// ParsedRequest holds the decoded fields of an operation request.
type ParsedRequest struct {
	Code      uint32
	RequestID uint32
	Data      []byte
}

// RequestParser decodes a raw operation message into a ParsedRequest.
type RequestParser func(raw []byte) (ParsedRequest, error)

// ResponseFrameBuilder builds a channel-0x01 wire frame from response fields.
type ResponseFrameBuilder func(code, reqID uint32, returnCode int32, errorMsg string, payload proto.Message) []byte

type routedRequest struct {
	connID    uint32
	code      uint32
	requestID uint32
	data      []byte
}

// Router polls all connections for channel-0x01 messages, parses requests
// via an injected parser, resolves player identity, and dispatches to handlers.
type Router struct {
	handlers   map[uint32]OperationHandler
	connMgr    *net.ConnManager
	sessions   *PlayerSessions
	workers    int
	reqCh      chan routedRequest
	parser     RequestParser
	buildFrame ResponseFrameBuilder
}

// NewRouter creates a new operation router.
func NewRouter(connMgr *net.ConnManager, sessions *PlayerSessions, workers int, parser RequestParser, buildFrame ResponseFrameBuilder) *Router {
	if workers < 1 {
		workers = 2
	}
	return &Router{
		handlers:   make(map[uint32]OperationHandler),
		connMgr:    connMgr,
		sessions:   sessions,
		workers:    workers,
		reqCh:      make(chan routedRequest, 256),
		parser:     parser,
		buildFrame: buildFrame,
	}
}

// Register adds a handler for an operation code.
func (r *Router) Register(opCode uint32, handler OperationHandler) {
	r.handlers[opCode] = handler
}

// Run starts the poll loop and worker goroutines. Blocks until ctx is done.
func (r *Router) Run(ctx context.Context) {
	// Start workers
	for i := 0; i < r.workers; i++ {
		go r.worker(ctx)
	}

	// Poll loop: drain operation input from all active connections
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.poll()
		}
	}
}

func (r *Router) poll() {
	for _, connID := range r.connMgr.ActiveConnIDs() {
		msgs := r.connMgr.DrainOpInput(connID)
		for _, raw := range msgs {
			parsed, err := r.parser(raw)
			if err != nil {
				continue
			}
			r.reqCh <- routedRequest{
				connID:    connID,
				code:      parsed.Code,
				requestID: parsed.RequestID,
				data:      parsed.Data,
			}
		}
	}
}

func (r *Router) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-r.reqCh:
			r.handleRequest(req)
		}
	}
}

func (r *Router) handleRequest(req routedRequest) {
	handler, ok := r.handlers[req.code]
	if !ok {
		r.sendError(req.connID, req.code, req.requestID, 1, "unknown operation code")
		return
	}

	username := r.sessions.Get(req.connID)
	if username == "" {
		r.sendError(req.connID, req.code, req.requestID, 2, "not authenticated")
		return
	}

	opCtx := &OpContext{
		ConnID:   req.connID,
		Username: username,
	}

	resp, err := handler(opCtx, req.data)
	if err != nil {
		r.sendError(req.connID, req.code, req.requestID, 3, err.Error())
		return
	}

	frame := r.buildFrame(req.code, req.requestID, 0, "", resp)
	if frame != nil {
		r.connMgr.SendReliable(req.connID, frame)
	}
}

func (r *Router) sendError(connID, code, reqID uint32, returnCode int32, msg string) {
	frame := r.buildFrame(code, reqID, returnCode, msg, nil)
	if frame != nil {
		r.connMgr.SendReliable(connID, frame)
	}
}

// SendPush sends a server-pushed notification (request_id=0) to a specific connection.
func (r *Router) SendPush(connID uint32, code uint32, payload proto.Message) {
	frame := r.buildFrame(code, 0, 0, "", payload)
	if frame != nil {
		r.connMgr.SendReliable(connID, frame)
	}
}

// ConnIDForUsername returns the connID for a given username, or 0 if not found.
func (r *Router) ConnIDForUsername(username string) uint32 {
	// This is a reverse lookup; for now we iterate sessions.
	// If performance matters, add a reverse map later.
	r.sessions.mu.RLock()
	defer r.sessions.mu.RUnlock()
	for connID, name := range r.sessions.sessions {
		if name == username {
			return connID
		}
	}
	return 0
}

func init() {
	// Suppress "imported and not used" if no handlers registered yet
	_ = log.Println
}
