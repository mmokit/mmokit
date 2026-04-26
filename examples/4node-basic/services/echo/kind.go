// Package echo is the v1 demo service for the pluggable services
// framework. It exists primarily to validate the framework end-to-end:
//   - PING returns the handling instance ID (visualizes routing affinity)
//   - PERSIST writes a (key, value, ts) row to demo_echo
//   - FETCH reads it back
//
// PERSIST + FETCH together validate cross-instance DB consistency: a
// PERSIST handled by instance A and a FETCH handled by instance B
// still find each other's writes because state lives in Postgres.
package echo

import (
	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/service"
)

// Kind is the registration descriptor passed to coord.RegisterService.
var Kind = service.Kind{
	Name: "echo",
	OpCodes: []uint32{
		uint32(basicpb.EchoOpCode_BOP_ECHO_PING),
		uint32(basicpb.EchoOpCode_BOP_ECHO_PERSIST),
		uint32(basicpb.EchoOpCode_BOP_ECHO_FETCH),
	},
	Factory:     New,
	RequiresDB:  true,
	Description: "demo: ping returns instanceID; persist/fetch round-trip a row through Postgres",
}
