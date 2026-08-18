package main

import (
	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/examples/space/internal/game"
)

// schemaFingerprint derives the protocol fingerprint this bot binary was
// compiled against, so the gateway can refuse it if the server has moved on.
//
// Derived rather than fetched, deliberately. Asking the server what it expects
// and echoing it back would make the gate an oracle: a stale bot would read the
// value, present it, pass, and then mis-decode exactly as before. The bot is
// built from the same source tree as the server, so it can compute the same
// answer independently — which is the only version of this check that means
// anything.
//
// game.GameProtocol alone is enough, without systems, cells or a database.
// That is the same property a gateway relies on.
func schemaFingerprint() string {
	p := mmokit.New(mmokit.Config{CellsX: 1, CellsY: 1, TickRate: 20, Headless: true})
	proto := mmokit.NewProtocol(p, "space")
	game.GameProtocol(p)
	proto.AssembleFromProcess(p)
	return mmokit.FormatSchemaFingerprint(proto.SchemaFingerprint())
}
