package ops

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/net"
)

// EventCode is any integer type usable as an op code (proto enums are int32).
type EventCode interface{ ~int32 | ~uint32 }

// OperationSchema describes one request/response operation for schema export.
// Mirrors mmokit.OperationSchema (parallel struct to avoid pkg/ops importing
// pkg/mmokit). Identical layout — castable.
type OperationSchema struct {
	Code          uint32 `json:"code"`
	Name          string `json:"name"`
	RequestProto  string `json:"requestProto"`
	ResponseProto string `json:"responseProto"`
}

// OpContext provides identity and connection info to operation handlers.
type OpContext struct {
	ConnID   uint32
	Username string
}

// OperationHandler processes an operation request and returns the serialized
// response payload (or error). The handler is responsible for marshaling its
// own response format (e.g. protobuf, JSON).
type OperationHandler func(ctx *OpContext, payload []byte) ([]byte, error)

// ParsedRequest holds the decoded fields of an operation request.
type ParsedRequest struct {
	Code      uint32
	RequestID uint32
	Data      []byte
}

// RequestParser decodes a raw operation message into a ParsedRequest.
type RequestParser func(raw []byte) (ParsedRequest, error)

// ResponseFrameBuilder builds a channel-0x01 wire frame from response fields.
// The payload is already serialized bytes (nil if no payload).
type ResponseFrameBuilder func(code, reqID uint32, returnCode int32, errorMsg string, payload []byte) []byte

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
	schemas    map[uint32]OperationSchema  // only typed registrations populate this
	connMgr    *net.ConnManager // concrete type intentional: uses gateway-only ActiveConnIDs()
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
		schemas:    make(map[uint32]OperationSchema),
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

// Codes returns the op codes currently registered, sorted ascending.
// Used by the service framework to cross-check Kind.OpCodes against
// actual handler registrations after Service.RegisterOps runs.
func (r *Router) Codes() []uint32 {
	out := make([]uint32, 0, len(r.handlers))
	for c := range r.handlers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Invoke runs the handler for opCode synchronously, bypassing the queue
// and the wire-format response builder. The caller supplies the
// OpContext (typically with a synthetic username for console-driven
// testing or RPC-from-system call sites). Returns the raw response bytes
// (still wire-format payload, not yet wrapped in OperationResponse) and
// the handler error.
//
// Returns an error if no handler is registered at opCode.
func (r *Router) Invoke(opCode uint32, ctx *OpContext, payload []byte) ([]byte, error) {
	handler, ok := r.handlers[opCode]
	if !ok {
		return nil, fmt.Errorf("ops.Router.Invoke: no handler for op code %d", opCode)
	}
	return handler(ctx, payload)
}

// Wrap replaces the handler at opCode with mw(existing). Returns false
// if no handler is registered at opCode. Used by the service framework's
// metric auto-wrapper to instrument handlers post-registration.
func (r *Router) Wrap(opCode uint32, mw HandlerMiddleware) bool {
	h, ok := r.handlers[opCode]
	if !ok {
		return false
	}
	r.handlers[opCode] = mw(h)
	return true
}

// HandlerMiddleware wraps an OperationHandler with cross-cutting
// behavior (metrics, tracing, etc.). The returned handler should call
// next at most once.
type HandlerMiddleware func(next OperationHandler) OperationHandler

// ProtoMessage is a constraint satisfied by *T where T is a proto message struct.
// This lets Register infer the pointer type from the value type parameter.
type ProtoMessage[T any] interface {
	*T
	proto.Message
}

// Register registers a typed operation handler. It captures the request and
// response proto type names for schema export via Router.Schema(). Panics on
// duplicate code.
//
// Because Go does not allow generic methods on non-generic types, Register is a
// free function. The untyped Router.Register method is unchanged and still
// works; only calls through this function appear in Schema().
//
// Specify only the value types Req and Res; pointer types are inferred:
//
//	ops.Register[MarketBrowseRequest, MarketOrderBookResponse](r, code, "name", handler)
//
// `code` accepts any `~int32` or `~uint32` (proto enums are int32; raw
// op-code constants are typically uint32) so callers don't have to write
// a uint32 cast at every site.
func Register[Req any, Res any, Code EventCode, ReqP ProtoMessage[Req], ResP ProtoMessage[Res]](
	r *Router, code Code, name string,
	handler func(ctx *OpContext, req ReqP) (ResP, error)) {

	op := uint32(code)
	reqZero := ReqP(new(Req))
	resZero := ResP(new(Res))

	if _, exists := r.handlers[op]; exists {
		panic(fmt.Sprintf("ops.Router: duplicate handler for code %d", op))
	}

	r.handlers[op] = func(ctx *OpContext, payload []byte) ([]byte, error) {
		req := ReqP(new(Req))
		if err := proto.Unmarshal(payload, req); err != nil {
			return nil, fmt.Errorf("op %d: unmarshal request: %w", op, err)
		}
		resp, err := handler(ctx, req)
		if err != nil {
			return nil, err
		}
		return proto.Marshal(resp)
	}
	r.schemas[op] = OperationSchema{
		Code:          op,
		Name:          name,
		RequestProto:  string(proto.MessageName(reqZero)),
		ResponseProto: string(proto.MessageName(resZero)),
	}
}

// Schema returns typed-registered operations as a deterministically-ordered
// slice sorted by code. Untyped Router.Register calls are NOT included — they
// have no proto-type metadata to export.
func (r *Router) Schema() []OperationSchema {
	out := make([]OperationSchema, 0, len(r.schemas))
	for _, s := range r.schemas {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
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
// The payload must already be serialized.
func (r *Router) SendPush(connID uint32, code uint32, payload []byte) {
	frame := r.buildFrame(code, 0, 0, "", payload)
	if frame != nil {
		r.connMgr.SendReliable(connID, frame)
	}
}

// ConnIDForUsername returns the connID for a given username, or 0 if not found.
func (r *Router) ConnIDForUsername(username string) uint32 {
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
	_ = log.Println
}
