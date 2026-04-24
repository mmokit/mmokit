package main

import (
	"os"

	"github.com/mlange-42/ark/ecs"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func dumpProtocolSchema(cfg mmokit.Config) {
	// Use the Protocol declared on cfg (which carries the ServerEvents registry)
	// so the schema dump reflects the same declaration as the running server.
	proto, _ := cfg.Protocol.(*mmokit.Protocol)
	if proto == nil {
		proto = mmokit.NewProtocol("basic")
	}
	proto.SetClientRenderMode(cfg.ClientRenderMode)

	// Client → Server events.
	mmokit.ClientEvent(proto, basicpb.ClientEventCode_BCE_LOGIN, "basicpb.LoginMsg")
	mmokit.ClientEvent(proto, basicpb.ClientEventCode_BCE_MOVE_TARGET, "basicpb.MoveTargetMsg")

	// Binary-encoded server event (no proto type; not in ServerEvents registry).
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")

	// Entity replication schema — uses shared playerKindDef with a throwaway world.
	w := ecs.NewWorld()
	def := playerKindDef(w)
	proto.EntityName(KindPlayer, def.Name)
	proto.SetReplicators(mmokit.BuildReplicators(w, nil, def))

	proto.WriteSchema(os.Stdout)
}
