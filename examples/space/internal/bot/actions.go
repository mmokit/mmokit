package bot

import (
	"encoding/binary"
	"log"
	"reflect"

	"github.com/mmokit/mmokit/examples/space/internal/game"
	"github.com/mmokit/mmokit/pkg/mmokit"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// Ability slot constants.
const (
	AbilityQ = 0 // Pulse Laser
	AbilityW = 1 // Railgun
	AbilityE = 2 // Ion Burn
	AbilityR = 3 // Plasma Torpedo
	AbilityD = 4 // Emergency Shields
	AbilityF = 5 // Afterburner
)

// pendingInput accumulates input state between input ticks. The bot's
// per-tick sender (Bot.sendInput) reads this snapshot and emits the
// matching typed-input messages on the wire (one HandleClient frame per
// stateful field that changed since last tick).
type pendingInput struct {
	moveX       float32
	moveY       float32
	moveActive  bool
	moveDirty   bool // true when MoveTo / StopMove fired since last send
	selectNetID uint32
	selectDirty bool // true when SelectTarget / ClearSelection fired since last send
	abilityCast uint32
	jettison    uint32
}

// MoveTo sets a move-to destination.
func (b *Bot) MoveTo(x, y float32) {
	b.inputMu.Lock()
	b.pending.moveX = x
	b.pending.moveY = y
	b.pending.moveActive = true
	b.pending.moveDirty = true
	b.inputMu.Unlock()
}

// StopMove clears the move command.
func (b *Bot) StopMove() {
	b.inputMu.Lock()
	b.pending.moveActive = false
	b.pending.moveDirty = true
	b.inputMu.Unlock()
}

// SelectTarget sets the player's selection to netID. The server-side
// selection model is single-target with no per-slot multi-lock — this
// replaces the legacy LockTarget/UnlockTarget pair.
func (b *Bot) SelectTarget(netID uint32) {
	b.inputMu.Lock()
	b.pending.selectNetID = netID
	b.pending.selectDirty = true
	b.inputMu.Unlock()
}

// ClearSelection clears the player's selection (server-side equivalent
// of right-click on the web client).
func (b *Bot) ClearSelection() {
	b.inputMu.Lock()
	b.pending.selectNetID = 0
	b.pending.selectDirty = true
	b.inputMu.Unlock()
}

// CastAbility queues an ability cast by slot (AbilityQ through AbilityF).
func (b *Bot) CastAbility(slot int) {
	b.inputMu.Lock()
	b.pending.abilityCast |= 1 << uint(slot)
	b.inputMu.Unlock()
}

// StartMining selects an asteroid and activates the mining beam (Weapon2 primary = E).
// Safe to call repeatedly — only sends the toggle once per target.
func (b *Bot) StartMining(targetNetID uint32) {
	b.inputMu.Lock()
	b.pending.selectNetID = targetNetID
	b.pending.selectDirty = true
	if b.miningTarget != targetNetID {
		b.pending.abilityCast |= 1 << uint(AbilityE) // mining beam toggle on
		b.miningTarget = targetNetID
	}
	b.inputMu.Unlock()
}

// StopMining deactivates the mining beam.
func (b *Bot) StopMining() {
	b.inputMu.Lock()
	if b.miningTarget != 0 {
		b.pending.abilityCast |= 1 << uint(AbilityE) // mining beam toggle off
		b.miningTarget = 0
	}
	b.inputMu.Unlock()
}

// Jettison sets an item to jettison.
func (b *Bot) Jettison(itemID uint32) {
	b.inputMu.Lock()
	b.pending.jettison = itemID
	b.inputMu.Unlock()
}

// Respawn sends a respawn request (reliable, typed).
func (b *Bot) Respawn() {
	b.inputSeq++
	b.sendTypedInput(&game.Respawn{Sequence: b.inputSeq})
}

// DepositItem transfers an item from cargo to bank (reliable, typed).
func (b *Bot) DepositItem(itemID uint32, qty int32) {
	b.inputSeq++
	b.sendTypedInput(&game.InventoryTransfer{
		Sequence: b.inputSeq, ItemID: itemID, Quantity: qty, Deposit: true,
	})
}

// WithdrawItem transfers an item from bank to cargo (reliable, typed).
func (b *Bot) WithdrawItem(itemID uint32, qty int32) {
	b.inputSeq++
	b.sendTypedInput(&game.InventoryTransfer{
		Sequence: b.inputSeq, ItemID: itemID, Quantity: qty, Deposit: false,
	})
}

// RequestBank fires the typed-op bank request. Bot is fire-and-forget —
// it does not correlate the response (the BankContents server event push
// already updates client state in production; the bot only needs the
// op invocation for load-test traffic).
func (b *Bot) RequestBank() {
	b.inputSeq++
	b.sendTypedOp(&game.BankRequest{Sequence: b.inputSeq})
}

// sendTypedOp marshals a typed-op Request via the reflection codec and
// wraps it in the channel-0x01 typed-op wire frame:
//
//	[u8 0x01][u32 typeID][u64 request_id][u32 bodyLen][body bytes]
//
// Fire-and-forget: the response frame is dropped. Bots that need the
// response should track pending requestIDs and decode the inbound frame
// on the same channel — but for current load-test scope, the bot only
// invokes ops to drive server traffic, not to consume their results.
//
// request_id increments via b.inputSeq cast to u64; collisions with the
// inputSeq used by 0x00 typed inputs are harmless because the two
// channels are decoded independently.
func (b *Bot) sendTypedOp(msg any) {
	t := reflect.TypeOf(msg)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	typeID := mmokit.TypeIDOf(t)
	body, err := pkguniverse.ReflectMarshal(msg)
	if err != nil {
		log.Printf("[bot:%s] encode op %s failed: %v", b.name, t.String(), err)
		return
	}
	frame := make([]byte, 1+4+8+4+len(body))
	frame[0] = pkgnet.ChannelOperation
	binary.LittleEndian.PutUint32(frame[1:5], typeID)
	binary.LittleEndian.PutUint64(frame[5:13], uint64(b.inputSeq))
	binary.LittleEndian.PutUint32(frame[13:17], uint32(len(body)))
	copy(frame[17:], body)
	if err := b.sendFrame(frame); err != nil {
		log.Printf("[bot:%s] send op failed: %v", b.name, err)
	}
}

// sendTypedInput marshals a HandleClient-eligible Go message via the
// reflection codec, wraps it in the channel-0x00 typed-input wire frame,
// and sends it. msg must point at a registered HandleClient type — see
// internal/game/input_messages.go for the canonical set. Frame layout:
//
//	[u8 0x00][u32 typeID][u32 bodyLen][body bytes]
//
// The 4-byte body-length prefix matches the host-side decoder in
// Stage.dispatchInboundEventFrame. Plan 1 Phase 5 unified the typed
// client-input channel with the event channel; the leading byte is now
// pkgnet.ChannelEvent (0x00) — frames that still use ChannelClientInput
// (0x02) panic on the read pump.
func (b *Bot) sendTypedInput(msg any) {
	t := reflect.TypeOf(msg)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	typeID := mmokit.TypeIDOf(t)

	body, err := pkguniverse.ReflectMarshal(msg)
	if err != nil {
		log.Printf("[bot:%s] encode input %s failed: %v", b.name, t.String(), err)
		return
	}
	frame := make([]byte, 1+8+len(body))
	frame[0] = pkgnet.ChannelEvent
	binary.LittleEndian.PutUint32(frame[1:5], typeID)
	binary.LittleEndian.PutUint32(frame[5:9], uint32(len(body)))
	copy(frame[9:], body)
	if err := b.sendFrame(frame); err != nil {
		log.Printf("[bot:%s] send input failed: %v", b.name, err)
	}
}
