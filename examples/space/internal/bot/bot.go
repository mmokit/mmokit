package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/examples/space/internal/game"
	"github.com/zenion/mmokit/pkg/mmokit"
	pkgnet "github.com/zenion/mmokit/pkg/net"
)

const (
	defaultInputRate = 50 * time.Millisecond // 20Hz
	defaultPingRate  = 2 * time.Second
	connectTimeout   = 10 * time.Second
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

// Bot is a headless game client for testing. Connects via WebSocket
// using the same HTTP cookie auth real clients use; consumes typed
// events on channel 0x00 and typed-op responses on channel 0x01.
type Bot struct {
	name       string
	ws         *websocket.Conn
	wsWriteMu  sync.Mutex // serializes WebSocket writes (coder/websocket disallows concurrent Write)
	myEntityID uint32

	mu       sync.RWMutex
	state    WorldState
	ownState OwnState
	alive    bool

	inputSeq uint32

	inputMu      sync.Mutex
	pending      pendingInput
	miningTarget uint32 // tracks which asteroid beam is toggled on for
	// selectedNetID tracks the last SelectTarget the bot has sent. The
	// selection-model rewrite collapsed the multi-slot lock state into a
	// single per-player Selection.EntityNetID; the bot mirrors that with
	// one scalar and only re-emits SelectTarget when the value changes.
	selectedNetID uint32

	inputRate time.Duration

	// Binary frame decoding state for WorldDelta bodies.
	decoderState *deltaDecoderState
	decoders     *deltaDecoders

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
		name:         username,
		inputRate:    defaultInputRate,
		decoderState: newDeltaDecoderState(),
		decoders:     newDeltaDecoders(),
		state:        WorldState{Entities: make(map[uint32]*EntitySnapshot)},
		spawnCh:      make(chan struct{}, 1),
		deathCh:      make(chan uint32, 1),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Name returns the bot's username.
func (b *Bot) Name() string { return b.name }

// Connect authenticates via HTTP cookie, dials the gateway WebSocket
// carrying that cookie, then blocks until the spawn confirmation
// arrives or the timeout fires. addr may be a host:port pair, an
// http(s):// URL, or a ws(s):// URL — the helper normalizes both the
// HTTP path used for /auth/{register,login} and the WS path /ws.
func (b *Bot) Connect(addr string) error {
	// A transport reconnect starts a new frame-sequence scope even when the
	// server reuses the same player entity during its reconnect grace window.
	b.decoderState = newDeltaDecoderState()

	cookie, err := Authenticate(toHTTPURL(addr), b.name)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	log.Printf("[bot:%s] auth cookie acquired", b.name)

	dialCtx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	hdr := http.Header{}
	hdr.Set("Cookie", cookie.Name+"="+cookie.Value)

	ws, _, err := websocket.Dial(dialCtx, toWSURL(addr), &websocket.DialOptions{
		HTTPHeader: hdr,
	})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	// coder/websocket caps inbound messages at 32 KiB by default;
	// WorldDelta bodies in busy AoIs blow past that. Lift the cap to
	// match the server-side buffer budget (1 MiB is generous; matches
	// the web client's default).
	ws.SetReadLimit(1 << 20)
	b.ws = ws

	b.ctx, b.cancel = context.WithCancel(context.Background())

	// Start recv before any writes so the spawn frame is always picked up.
	go b.recvLoop()

	// Wait for spawn confirmation (sent by the server after the cookie
	// validates and the player is assigned to a cell).
	select {
	case <-b.spawnCh:
		log.Printf("[bot:%s] connected, entityID=%d", b.name, b.myEntityID)
	case <-time.After(connectTimeout):
		_ = ws.Close(websocket.StatusNormalClosure, "spawn timeout")
		b.cancel()
		return errors.New("connect timeout waiting for spawn")
	}

	// Start input + ping loops only after spawn so we never write
	// before the server has dispatched our PlayerAssignment.
	go b.inputLoop()
	go b.pingLoop()

	return nil
}

// Disconnect shuts down the bot.
func (b *Bot) Disconnect() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.ws != nil {
		_ = b.ws.Close(websocket.StatusNormalClosure, "")
	}
	log.Printf("[bot:%s] disconnected", b.name)
}

// sendFrame writes one wire frame (channel byte + payload) over the
// WebSocket. Serializes concurrent writes — coder/websocket disallows
// overlapping Writes on the same connection. WebSocket is reliable by
// definition, so SendReliable and SendUnreliable both route through
// here.
func (b *Bot) sendFrame(frame []byte) error {
	if b.ws == nil {
		return errors.New("bot: not connected")
	}
	b.wsWriteMu.Lock()
	defer b.wsWriteMu.Unlock()
	return b.ws.Write(b.ctx, websocket.MessageBinary, frame)
}

func (b *Bot) recvLoop() {
	defer func() {
		if b.cancel != nil {
			b.cancel()
		}
	}()
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		_, data, err := b.ws.Read(b.ctx)
		if err != nil {
			select {
			case <-b.ctx.Done():
			default:
				log.Printf("[bot:%s] recv error: %v", b.name, err)
			}
			return
		}

		if len(data) < 1 {
			continue
		}
		channel := data[0]
		payload := data[1:]

		switch channel {
		case pkgnet.ChannelEvent: // typed events
			b.decodeTypedEventFrame(payload)
		case pkgnet.ChannelOperation: // typed-op responses
			// Bots are fire-and-forget on ops today (sendTypedOp does
			// not correlate). Drop the response payload silently rather
			// than log per-frame; rewire when a bot scenario actually
			// needs the response.
			_ = payload
		default:
			// Unknown channel; skip
		}
	}
}

// pingLoop keeps the connection liveness signal flowing through the
// typed-input path. Server-side HandleClient[Ping] (engine default)
// echoes a Pong with the same timestamp.
func (b *Bot) pingLoop() {
	ticker := time.NewTicker(defaultPingRate)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.sendTypedInput(&mmokit.Ping{ClientTime: time.Now().UnixMilli()})
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
	// Clear one-shot + dirty fields
	b.pending.abilityCast = 0
	b.pending.jettison = 0
	b.pending.moveDirty = false
	b.pending.selectDirty = false
	b.inputMu.Unlock()

	// One typed message per stateful field that changed since last tick,
	// plus per-bit CastAbility for each pressed slot.
	if inp.moveDirty {
		b.inputSeq++
		b.sendTypedInput(&game.SetMoveTarget{
			Sequence: b.inputSeq,
			Active:   inp.moveActive,
			X:        inp.moveX,
			Y:        inp.moveY,
		})
	}
	if inp.selectDirty {
		// One typed SelectTarget per change; NetID=0 clears the selection
		// (the selection-model equivalent of right-click on the web client).
		// Suppress no-op repeats so an AI that keeps asking for the same
		// target every tick doesn't flood the wire.
		next := inp.selectNetID
		if next != b.selectedNetID {
			b.inputSeq++
			b.sendTypedInput(&game.SelectTarget{
				Sequence: b.inputSeq,
				NetID:    next,
			})
			b.selectedNetID = next
		}
	}
	for slot := uint8(0); slot < 8; slot++ {
		if inp.abilityCast&(1<<slot) == 0 {
			continue
		}
		b.inputSeq++
		b.sendTypedInput(&game.CastAbility{
			Sequence: b.inputSeq,
			Slot:     slot,
		})
	}
	if inp.jettison != 0 {
		b.inputSeq++
		b.sendTypedInput(&game.JettisonItem{
			Sequence: b.inputSeq,
			ItemID:   inp.jettison,
		})
	}
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
		return e.Type == gamecomp.KindAsteroid && e.ResourceRemaining > 0
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
		return e.Type == gamecomp.KindStation
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
		ids = append(ids, item.ItemID)
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

// toHTTPURL normalizes addr (host:port, ws://…, wss://…, http://…,
// https://…) into the HTTP base URL Authenticate uses for /auth/* POSTs.
func toHTTPURL(addr string) string {
	switch {
	case strings.HasPrefix(addr, "http://"), strings.HasPrefix(addr, "https://"):
		return addr
	case strings.HasPrefix(addr, "ws://"):
		return "http://" + strings.TrimPrefix(addr, "ws://")
	case strings.HasPrefix(addr, "wss://"):
		return "https://" + strings.TrimPrefix(addr, "wss://")
	default:
		return "http://" + addr
	}
}

// toWSURL normalizes addr into the WebSocket URL the gateway listens on
// (path /ws — see pkg/net/server.go::ListenAndServe).
func toWSURL(addr string) string {
	switch {
	case strings.HasPrefix(addr, "ws://"), strings.HasPrefix(addr, "wss://"):
		return addr
	case strings.HasPrefix(addr, "http://"):
		return "ws://" + strings.TrimPrefix(addr, "http://") + "/ws"
	case strings.HasPrefix(addr, "https://"):
		return "wss://" + strings.TrimPrefix(addr, "https://") + "/ws"
	default:
		return "ws://" + addr + "/ws"
	}
}
