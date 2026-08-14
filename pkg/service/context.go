package service

import (
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/persist/postgres"
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

	// Bus is the per-process typed pub/sub bus. Services subscribe to
	// framework events and to sibling-service events here at Init time.
	// See pkg/service/bus.go.
	//
	// Always non-nil — Process.New constructs the Bus before any
	// service.Context is built.
	Bus *Bus
}
