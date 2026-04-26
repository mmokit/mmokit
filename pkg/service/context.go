package service

import (
	"google.golang.org/protobuf/proto"

	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/persist/postgres"
)

// Context bundles the runtime dependencies handed to a Service at Init.
//
// Reserved field-additions for v2 (SyncStream, ShardKey, etc.) will be
// added only when the corresponding feature lands. Game devs receive
// a *Context — fields may be added without breaking compilation.
type Context struct {
	// KindName is the registered service kind running on this instance.
	KindName string

	// InstanceID is the cluster-unique ID for this running instance.
	// Format: "<hostID>-<kindName>-<n>".
	InstanceID string

	// Logger is the process-shared logger. Categories "services:<kind>"
	// are auto-registered for every instantiated kind.
	Logger *logger.Logger

	// DB is the cluster's Postgres handle. Nil if Config.PostgresURL
	// was empty. Kinds with RequiresDB=true are guaranteed non-nil DB
	// when their Init runs (Build validates this).
	DB *postgres.Store

	// Roles is the role set this process is running. Lets services
	// inspect their colocation environment if needed.
	Roles map[string]struct{}

	// SendEvent forwards a server event to a connected client through
	// the gateway path. Goes through the same mesh path as cell-
	// originated events. Returns an error if the connection has gone
	// away or the gateway role isn't available on this process.
	//
	// Game-side typed-event helpers may wrap this; raw uses build the
	// proto message and pass the engine event-code constant.
	SendEvent func(connID uint32, code uint16, msg proto.Message) error
}
