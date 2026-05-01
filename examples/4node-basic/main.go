package main

import (
	"embed"
	"log"

	basicpb "github.com/zenion/mmoserver/gen/go/basicpb"
	"github.com/zenion/mmoserver/pkg/mmokit"

	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)

// MoveDeps is the deps struct injected into the BCE_MOVE_TARGET handler.
// MoveTarget is required — handler is silently skipped if absent.
type MoveDeps struct {
	MT *mmokit.MoveTarget
}

//go:embed all:web/dist
var webDist embed.FS

func main() {
	mmo := mmokit.New(mmokit.Config{
		InvariantMode:    mmokit.InvariantPanic,
		StrictNetIDIndex: true,
		CellsX:           CellsX,
		CellsY:           CellsY,
		CellSize:         CellSize,
		TickRate:         TickRate,
		AoIRadius:        AoIRadius,
		StaticFS:         webDist,
		StaticFSPrefix:   "web/dist",
		DefaultSpawn:     mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85},
		OnConsoleReady: func(p *mmokit.Process, console *mmokit.Console) {
			if err := registerBotCommands(p, console.Registry()); err != nil {
				log.Printf("4node-basic: failed to register bot commands: %v", err)
			}
		},
		Protocol: mmokit.NewProtocol("basic"),
	})

	if err := mmokit.RegisterAuthService(mmo, mmokit.DefaultAuthOpts()); err != nil {
		log.Fatalf("4node-basic: RegisterAuthService: %v", err)
	}

	mmokit.RegisterKind[PlayerComponents](mmo, KindPlayer, "Player")
	mmokit.RegisterKind[BotComponents](mmo, KindBot, "Bot")

	mmo.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
		if err := mmokit.GrantDebug(mmo, s, "topology"); err != nil {
			log.Printf("4node-basic: auto-grant topology for %s: %v", s.Username, err)
		}
		stage.SpawnPlayer(s,
			mmokit.WithCollider(PlayerRadius),
			mmokit.WithEntityKind(KindPlayer),
			mmokit.Init(func(c *PlayerComponents) {
				c.Name.Name = s.Username
			}),
		)
	})

	if err := mmo.RegisterService(echo.Kind); err != nil {
		log.Fatalf("4node-basic: register echo service: %v", err)
	}

	mmokit.OnInputWith[basicpb.MoveTargetMsg, MoveDeps](mmo, basicpb.ClientEventCode_BCE_MOVE_TARGET).
		Active().
		Do(func(p *mmokit.Player, msg *basicpb.MoveTargetMsg, c *MoveDeps) {
			c.MT.SetTarget(msg.TargetX, msg.TargetY)
		})

	mmo.AddSystem(mmokit.NewClickToMoveSystem())
	mmo.AddSystem(mmokit.NewPhysicsSystem())
	mmo.AddSystem(mmokit.NewSpatialSystem())
	mmo.AddSystem(mmokit.NewDebugBroadcaster())
	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
	mmo.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)
	mmo.Start()
}
