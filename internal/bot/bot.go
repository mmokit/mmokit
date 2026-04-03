package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/net/udpclient"
	"google.golang.org/protobuf/proto"
)

const (
	defaultInputRate = 50 * time.Millisecond // 20Hz
	connectTimeout   = 5 * time.Second
)

// Option configures a Bot.
type Option func(*Bot)

// WithInputRate sets the input tick interval.
func WithInputRate(d time.Duration) Option {
	return func(b *Bot) { b.inputRate = d }
}

// WithOnSpawn sets a callback fired when the bot spawns.
func WithOnSpawn(fn func()) Option {
	return func(b *Bot) { b.onSpawn = fn }
}

// WithOnDeath sets a callback fired when the bot dies.
func WithOnDeath(fn func(killerID uint32)) Option {
	return func(b *Bot) { b.onDeath = fn }
}

// WithOnUpdate sets a callback fired on each world update.
func WithOnUpdate(fn func(*WorldState)) Option {
	return func(b *Bot) { b.onUpdate = fn }
}

// Bot is a headless game client for testing.
type Bot struct {
	name       string
	conn       *udpclient.Client
	myEntityID uint32
	itemDefs   map[uint32]*gamepb.ItemDefMsg

	mu       sync.RWMutex
	state    WorldState
	ownState OwnState
	alive    bool

	inputSeq uint32

	inputMu      sync.Mutex
	pending      pendingInput
	miningTarget uint32 // tracks which asteroid beam is toggled on for

	inputRate time.Duration

	// Binary frame decoding state
	baselines map[uint32]*baselineEntry
	decoders  *deltaDecoders

	// Event callbacks
	onSpawn  func()
	onDeath  func(killerID uint32)
	onUpdate func(*WorldState)

	// Channels for blocking waits
	spawnCh chan struct{}
	deathCh chan uint32

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new Bot with the given username.
func New(username string, opts ...Option) *Bot {
	b := &Bot{
		name:      username,
		inputRate: defaultInputRate,
		itemDefs:  make(map[uint32]*gamepb.ItemDefMsg),
		baselines: make(map[uint32]*baselineEntry),
		decoders:  newDeltaDecoders(),
		spawnCh:   make(chan struct{}, 1),
		deathCh:   make(chan uint32, 1),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Name returns the bot's username.
func (b *Bot) Name() string { return b.name }

// Connect dials the server and blocks until spawned or timeout.
func (b *Bot) Connect(addr string) error {
	c, err := udpclient.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	b.conn = c

	b.ctx, b.cancel = context.WithCancel(context.Background())

	// Start recv loop before sending login
	go b.recvLoop()

	// Send login
	b.sendEvent(uint32(enginepb.ClientEventCode_CE_LOGIN), &enginepb.LoginMsg{Username: b.name}, true)

	// Wait for spawn
	select {
	case <-b.spawnCh:
		log.Printf("[bot:%s] connected, entityID=%d", b.name, b.myEntityID)
	case <-time.After(connectTimeout):
		b.conn.Close()
		return errors.New("connect timeout waiting for spawn")
	}

	// Start input tick loop
	go b.inputLoop()

	return nil
}

// Disconnect shuts down the bot.
func (b *Bot) Disconnect() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.conn != nil {
		b.conn.Close()
	}
}

func (b *Bot) recvLoop() {
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		data, err := b.conn.Recv()
		if err != nil {
			select {
			case <-b.ctx.Done():
			default:
				log.Printf("[bot:%s] recv error: %v", b.name, err)
			}
			return
		}

		// Server frames are prefixed with a channel byte
		if len(data) < 2 {
			continue
		}
		channel := data[0]
		payload := data[1:]

		if channel != 0x00 {
			// Bot only handles game events (channel 0x00) for now
			continue
		}

		var evt enginepb.ServerEvent
		if err := proto.Unmarshal(payload, &evt); err != nil {
			continue
		}

		switch enginepb.ServerEventCode(evt.Code) {
		case enginepb.ServerEventCode_SE_PLAYER_SPAWNED:
			var spawned gamepb.PlayerSpawnedMsg
			if err := proto.Unmarshal(evt.Data, &spawned); err != nil {
				continue
			}
			b.mu.Lock()
			b.myEntityID = spawned.YourEntityId
			b.alive = true
			for _, def := range spawned.ItemDefs {
				b.itemDefs[def.Id] = def
			}
			b.mu.Unlock()

			select {
			case b.spawnCh <- struct{}{}:
			default:
			}
			if b.onSpawn != nil {
				b.onSpawn()
			}

		case enginepb.ServerEventCode_SE_DELTA_WORLD_UPDATE:
			ws, ok := decodeBinaryFrame(evt.Data, b.baselines, b.decoders)
			if !ok {
				continue
			}
			b.mu.Lock()
			b.state = ws
			b.mu.Unlock()
			if b.onUpdate != nil {
				b.onUpdate(&ws)
			}

		case enginepb.ServerEventCode_SE_PLAYER_DIED:
			var died enginepb.PlayerDiedMsg
			if err := proto.Unmarshal(evt.Data, &died); err != nil {
				continue
			}
			b.mu.Lock()
			b.alive = false
			b.mu.Unlock()

			select {
			case b.deathCh <- died.KillerId:
			default:
			}
			if b.onDeath != nil {
				b.onDeath(died.KillerId)
			}

		case enginepb.ServerEventCode_SE_PLAYER_OWN_STATE:
			var own gamepb.PlayerOwnStateMsg
			if err := proto.Unmarshal(evt.Data, &own); err != nil {
				continue
			}
			b.mu.Lock()
			b.ownState = ownStateFromMsg(&own)
			b.mu.Unlock()

		case enginepb.ServerEventCode_SE_LOGIN_REJECTED:
			var rejected enginepb.LoginRejectedMsg
			if err := proto.Unmarshal(evt.Data, &rejected); err != nil {
				continue
			}
			log.Printf("[bot:%s] login rejected: %s", b.name, rejected.Reason)
		}
	}
}

func (b *Bot) inputLoop() {
	ticker := time.NewTicker(b.inputRate)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.sendInput()
		}
	}
}

func (b *Bot) sendInput() {
	b.inputMu.Lock()
	inp := b.pending
	// Clear one-shot fields
	b.pending.abilityCast = 0
	b.pending.jettison = 0
	b.inputMu.Unlock()

	b.inputSeq++

	b.sendEvent(uint32(enginepb.ClientEventCode_CE_PLAYER_INPUT), &gamepb.PlayerInputMsg{
		Sequence:     b.inputSeq,
		MoveX:        inp.moveX,
		MoveY:        inp.moveY,
		MoveActive:   inp.moveActive,
		LockTargetId: inp.lockTargetID,
		AbilityCast:  inp.abilityCast,
		Jettison:     inp.jettison,
	}, false)
}

// State returns a copy of the current world state.
func (b *Bot) State() WorldState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state
}

// MyEntity returns this bot's entity snapshot, or nil if not found.
func (b *Bot) MyEntity() *EntitySnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state.Entities[b.myEntityID]
}

// FindByName finds an entity by pilot name.
func (b *Bot) FindByName(name string) *EntitySnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, e := range b.state.Entities {
		if e.PilotName == name {
			return e
		}
	}
	return nil
}

// FindNearest finds the nearest entity matching the predicate.
func (b *Bot) FindNearest(pred func(*EntitySnapshot) bool) *EntitySnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	me := b.state.Entities[b.myEntityID]
	if me == nil {
		return nil
	}
	var best *EntitySnapshot
	bestDist := float32(math.MaxFloat32)
	for _, e := range b.state.Entities {
		if e.ID == b.myEntityID || !pred(e) {
			continue
		}
		d := dist(me, e)
		if d < bestDist {
			bestDist = d
			best = e
		}
	}
	return best
}

// FindNearestAsteroid finds the nearest asteroid with resources remaining.
func (b *Bot) FindNearestAsteroid() *EntitySnapshot {
	return b.FindNearest(func(e *EntitySnapshot) bool {
		return e.Type == gamepb.EntityType_ENTITY_TYPE_ASTEROID && e.ResourceRemaining > 0
	})
}

// FindAll returns all entities matching the predicate.
func (b *Bot) FindAll(pred func(*EntitySnapshot) bool) []*EntitySnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var result []*EntitySnapshot
	for _, e := range b.state.Entities {
		if e.ID == b.myEntityID {
			continue
		}
		if pred(e) {
			result = append(result, e)
		}
	}
	return result
}

// FindNearestStation finds the nearest station entity.
func (b *Bot) FindNearestStation() *EntitySnapshot {
	return b.FindNearest(func(e *EntitySnapshot) bool {
		return e.Type == gamepb.EntityType_ENTITY_TYPE_STATION
	})
}

// OwnState returns a copy of the bot's own-entity state (cargo, lock, cooldowns).
func (b *Bot) OwnState() OwnState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ownState
}

// CargoItemIDs returns the item IDs currently in cargo.
func (b *Bot) CargoItemIDs() []uint32 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]uint32, 0, len(b.ownState.CargoItems))
	for _, item := range b.ownState.CargoItems {
		ids = append(ids, item.ItemId)
	}
	return ids
}

// DistanceTo returns the distance from this bot's entity to target.
func (b *Bot) DistanceTo(e *EntitySnapshot) float32 {
	me := b.MyEntity()
	if me == nil {
		return math.MaxFloat32
	}
	return dist(me, e)
}

// IsAlive returns whether the bot is currently alive.
func (b *Bot) IsAlive() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.alive
}

// WaitForDeath blocks until the bot dies and returns the killer's net ID.
func (b *Bot) WaitForDeath(ctx context.Context) uint32 {
	select {
	case id := <-b.deathCh:
		return id
	case <-ctx.Done():
		return 0
	}
}

// WaitForSpawn blocks until the bot spawns.
func (b *Bot) WaitForSpawn(ctx context.Context) {
	select {
	case <-b.spawnCh:
	case <-ctx.Done():
	}
}

func dist(a, b *EntitySnapshot) float32 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}
