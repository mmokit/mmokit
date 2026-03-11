package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/pkg/bot"
)

func runDuel(addr string, count int) {
	if count < 2 {
		count = 2
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down bots...")
		cancel()
	}()

	var wg sync.WaitGroup
	bots := make([]*bot.Bot, count)

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("bot_fighter_%d", i+1)
		bots[i] = bot.New(name)
		if err := bots[i].Connect(addr); err != nil {
			log.Fatalf("bot %s connect failed: %v", name, err)
		}
		wg.Add(1)
		go runFighterAI(ctx, bots[i], &wg)
		time.Sleep(100 * time.Millisecond)
	}

	<-ctx.Done()
	for _, b := range bots {
		b.Disconnect()
	}
	wg.Wait()
	log.Println("duel scenario finished")
}

func runFighterAI(ctx context.Context, b *bot.Bot, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	abilitySlots := []int{bot.AbilityQ, bot.AbilityW, bot.AbilityE, bot.AbilityR}
	abilityIdx := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if !b.IsAlive() {
			b.Respawn()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Find nearest other ship
		target := b.FindNearest(func(e *bot.EntitySnapshot) bool {
			return e.Type == gamepb.EntityType_ENTITY_TYPE_SHIP && e.PilotName != b.Name()
		})
		if target == nil {
			continue
		}

		// Move toward target
		b.MoveTo(target.X, target.Y)

		// Lock on
		b.LockTarget(target.ID)

		// Check own entity for lock progress
		me := b.MyEntity()
		if me == nil {
			continue
		}

		// Fire abilities when locked
		if me.LockProgress >= 1.0 {
			b.CastAbility(abilitySlots[abilityIdx%len(abilitySlots)])
			abilityIdx++
		}

		// Defensive: use emergency shields when shield is low
		if me.Shield < 0.3 {
			b.CastAbility(bot.AbilityD)
		}
	}
}
