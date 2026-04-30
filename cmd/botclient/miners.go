package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/bot"
)

const (
	miningRange = 200.0
	sellRange   = 250.0
)

type minerState int

const (
	stateMining   minerState = iota
	stateReturnig            // heading to station to sell
	stateSelling             // at station, depositing + selling
)

func runMiners(addr string, count int) {
	if count < 1 {
		count = 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down miners...")
		cancel()
	}()

	var wg sync.WaitGroup
	bots := make([]*bot.Bot, count)

	for i := 0; i < count; i++ {
		name := fmt.Sprintf("bot_miner_%d", i+1)
		bots[i] = bot.New(name)
		if err := bots[i].Connect(addr); err != nil {
			log.Fatalf("bot %s connect failed: %v", name, err)
		}
		wg.Add(1)
		go runMinerAI(ctx, bots[i], &wg)
		time.Sleep(100 * time.Millisecond)
	}

	<-ctx.Done()
	for _, b := range bots {
		b.Disconnect()
	}
	wg.Wait()
	log.Println("miners scenario finished")
}

func runMinerAI(ctx context.Context, b *bot.Bot, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	state := stateMining
	var targetAsteroidID uint32

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if !b.IsAlive() {
			b.Respawn()
			state = stateMining
			targetAsteroidID = 0
			time.Sleep(500 * time.Millisecond)
			continue
		}

		me := b.MyEntity()
		if me == nil {
			continue
		}
		own := b.OwnState()

		switch state {
		case stateMining:
			// Cargo full? Head to station to sell.
			if own.MaxCargoMass > 0 && own.CargoMass >= own.MaxCargoMass*0.95 {
				b.StopMining()
				state = stateReturnig
				targetAsteroidID = 0
				log.Printf("[bot:%s] cargo full (%.0f/%.0f), heading to station", b.Name(), own.CargoMass, own.MaxCargoMass)
				continue
			}

			// Check if our current target asteroid is still valid
			if targetAsteroidID != 0 {
				ws := b.State()
				if a, ok := ws.Entities[targetAsteroidID]; !ok || a.ResourceRemaining <= 0 {
					// Target depleted or gone, pick a new one
					targetAsteroidID = 0
					b.StopMining()
				}
			}

			// Pick a random asteroid if we don't have a target
			if targetAsteroidID == 0 {
				asteroid := pickRandomAsteroid(b)
				if asteroid == nil {
					b.StopMining()
					continue
				}
				targetAsteroidID = asteroid.ID
			}

			// Navigate to and mine the target asteroid
			ws := b.State()
			asteroid, ok := ws.Entities[targetAsteroidID]
			if !ok {
				targetAsteroidID = 0
				continue
			}

			d := b.DistanceTo(asteroid)
			if d > miningRange {
				b.StopMining()
				b.MoveTo(asteroid.X, asteroid.Y)
			} else {
				b.StopMove()
				b.StartMining(asteroid.ID)
			}

		case stateReturnig:
			// Head to station
			station := b.FindNearestStation()
			if station == nil {
				// No station visible, move toward origin (station spawns at 0,0)
				b.MoveTo(0, 0)
				continue
			}

			d := b.DistanceTo(station)
			if d <= sellRange {
				b.StopMove()
				state = stateSelling
				log.Printf("[bot:%s] arrived at station, selling cargo", b.Name())
			} else {
				b.MoveTo(station.X, station.Y)
			}

		case stateSelling:
			// Deposit all cargo items, then sell them from bank
			cargoIDs := b.CargoItemIDs()
			if len(cargoIDs) > 0 {
				// Deposit each cargo item (qty 0 = all)
				for _, itemID := range cargoIDs {
					b.DepositItem(itemID, 0)
				}
				// Wait a tick for server to process deposits
				continue
			}

			// Cargo is empty — bank now holds the deposit. Sell-to-station
			// was removed (marketplace is the only sink for resources);
			// for load-test purposes the bot just stockpiles and re-mines.
			b.RequestBank()

			log.Printf("[bot:%s] deposited cargo, returning to mine", b.Name())
			state = stateMining
			targetAsteroidID = 0
		}
	}
}

// pickRandomAsteroid picks a random asteroid with resources from all visible ones.
func pickRandomAsteroid(b *bot.Bot) *bot.EntitySnapshot {
	asteroids := b.FindAll(func(e *bot.EntitySnapshot) bool {
		return e.Type == gamepb.EntityType_ENTITY_TYPE_ASTEROID && e.ResourceRemaining > 0
	})
	if len(asteroids) == 0 {
		return nil
	}
	return asteroids[rand.Intn(len(asteroids))]
}
