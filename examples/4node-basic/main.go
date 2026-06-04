package main

import (
	"context"
	"log"

	"github.com/zenion/mmoserver/pkg/mmokit"

	"github.com/zenion/mmoserver/examples/4node-basic/services/echo"
)

func main() {
	process := mmokit.New(mmokit.Config{
		Name:             "basic",
		InvariantMode:    mmokit.InvariantPanic,
		StrictNetIDIndex: true,
		CellsX:           CellsX,
		CellsY:           CellsY,
		CellSize:         CellSize,
		TickRate:         TickRate,
		AoIRadius:        AoIRadius,
	})

	process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location {
		return mmokit.Location{X: CellSize * 0.85, Y: CellSize * 0.85}
	})

	process.OnConsoleReady(func(p *mmokit.Process, console *mmokit.Console) {
		if err := registerBotCommands(p, console.Registry()); err != nil {
			log.Printf("4node-basic: failed to register bot commands: %v", err)
		}
		if err := registerWasmCommands(p, console.Registry()); err != nil {
			log.Printf("4node-basic: failed to register wasm commands: %v", err)
		}
	})

	if err := mmokit.RegisterAuthService(process, mmokit.DefaultAuthOpts()); err != nil {
		log.Fatalf("4node-basic: RegisterAuthService: %v", err)
	}

	if err := mmokit.RegisterChatService(process, mmokit.ChatOpts{
		DefaultChannels: []mmokit.DefaultChannelDef{
			{Slug: "world", Kind: mmokit.ChannelKindSystemAll, Topic: "World chat"},
			{Slug: "help", Kind: mmokit.ChannelKindSystemAll, Topic: "Help chat. Be patient."},
			{Slug: "trade", Kind: mmokit.ChannelKindSystemAll, Topic: "Trade chat."},
		},
	}); err != nil {
		log.Fatalf("4node-basic: RegisterChatService: %v", err)
	}

	mmokit.RegisterKind[PlayerComponents](process, KindPlayer, "Player")
	mmokit.RegisterKind[BotComponents](process, KindBot, "Bot")

	if err := mmokit.RegisterAdminPanel(process, mmokit.AdminPanelDef{
		ID:       "bots",
		Label:    "Bots",
		Icon:     "Bot",
		Group:    "Game",
		Topics:   []string{"bots"},
		Commands: []string{"bot.spawn", "bot.clear", "bot.list"},
	}); err != nil {
		log.Fatalf("4node-basic: register bots panel: %v", err)
	}

	process.OnPlayerJoin(func(session *mmokit.PlayerSession, stage *mmokit.Stage) {
		if err := mmokit.GrantDebug(process, session, "topology"); err != nil {
			log.Printf("4node-basic: auto-grant topology for %s: %v", session.Username, err)
		}
		stage.SpawnPlayer(session,
			mmokit.EntityKind{Type: KindPlayer},
			mmokit.Collider{Radius: PlayerRadius},
			PlayerName{Name: session.Username},
			mmokit.MoveTarget{},
		)
	})

	if err := process.RegisterService(echo.Kind); err != nil {
		log.Fatalf("4node-basic: register echo service: %v", err)
	}

	process.AddSystem(mmokit.NewClickToMoveSystem())
	process.AddSystem(mmokit.NewPhysicsSystem())
	process.AddSystem(mmokit.NewSpatialSystem())
	process.AddSystem(mmokit.NewDebugBroadcaster())
	process.AddSystem(mmokit.NewSystem(&BotSystem{}))
	process.AddSystem(mmokit.NewNetworkSystem())

	log.Printf("4node-basic: grid %dx%d cells, cell size %.0f, AoI %.0f", CellsX, CellsY, CellSize, AoIRadius)

	botsCtx, botsCancel := context.WithCancel(context.Background())
	defer botsCancel()
	startBotsPublisher(botsCtx, process)

	process.Start()
}
