package main

import (
	"os"

	"github.com/mlange-42/ark/ecs"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func dumpProtocolSchema() {
	proto := mmokit.NewProtocol("basic")

	// Client → Server events.
	mmokit.ClientEvent(proto, basicpb.BasicClientEventCode_BCE_LOGIN, "basicpb.BasicLoginMsg")
	mmokit.ClientEvent(proto, basicpb.BasicClientEventCode_BCE_MOVE_TARGET, "basicpb.BasicMoveTargetMsg")

	// Server → Client events.
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_PLAYER_SPAWNED, "playerSpawned", "basicpb.BasicSpawnedMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_CELL_TOPOLOGY, "cellTopology", "basicpb.BasicCellTopologyMsg")
	mmokit.ServerEvent(proto, enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE, "deltaWorldUpdate", "")

	// Entity replication schema — uses the same setupReplication with a throwaway world.
	proto.EntityName(KindPlayer, "Player")
	proto.SetReplicators(setupReplication(ecs.NewWorld(), mmokit.CellSize))

	proto.WriteSchema(os.Stdout)
}
