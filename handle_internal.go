package mmokit

import (
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// HandleAllInternal registers fn as the handler for messages of type M on
// every Stage owned by world, marking M as server-internal — the framework
// will NOT auto-broadcast post-handler.
//
// Use for messages that are pure server-side accounting (e.g. KillCredit:
// currency rewards routed to the killer's authoritative cell with no
// client visibility intended). Same Send semantics as HandleAll —
// target.Send(&msg) routes to the authoritative cell, handler runs there,
// post-handler the framework skips the broadcast queue push.
//
// Compare HandleAll (broadcasts to AoI viewers) and HandleClient
// (validates client-origin connection ownership; Plan G Phase 2).
func HandleAllInternal[M any](world *pkguniverse.Process, fn func(target Entity, msg *M)) {
	// Note: explicitly does NOT call RegisterBroadcastType. Handler still
	// registers in the dispatcher (via Handle), so target.Send() routes
	// and dispatches normally; only the broadcast leg is suppressed.
	world.OnStageInit(func(stage *pkguniverse.Stage) {
		Handle(stage, fn)
	})
}
