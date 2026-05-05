package bot

import (
	"encoding/binary"
	"reflect"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
	"google.golang.org/protobuf/proto"
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
	moveX        float32
	moveY        float32
	moveActive   bool
	moveDirty    bool // true when MoveTo / StopMove fired since last send
	lockTargetID uint32
	lockDirty    bool // true when LockTarget / ClearLock fired since last send
	abilityCast  uint32
	jettison     uint32
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

// LockTarget sets the combat lock-on target.
func (b *Bot) LockTarget(netID uint32) {
	b.inputMu.Lock()
	b.pending.lockTargetID = netID
	b.pending.lockDirty = true
	b.inputMu.Unlock()
}

// ClearLock clears the combat lock-on target.
func (b *Bot) ClearLock() {
	b.inputMu.Lock()
	b.pending.lockTargetID = 0
	b.pending.lockDirty = true
	b.inputMu.Unlock()
}

// CastAbility queues an ability cast by slot (AbilityQ through AbilityF).
func (b *Bot) CastAbility(slot int) {
	b.inputMu.Lock()
	b.pending.abilityCast |= 1 << uint(slot)
	b.inputMu.Unlock()
}

// StartMining locks an asteroid and activates the mining beam (Weapon2 primary = E).
// Safe to call repeatedly — only sends the toggle once per target.
func (b *Bot) StartMining(targetNetID uint32) {
	b.inputMu.Lock()
	b.pending.lockTargetID = targetNetID
	b.pending.lockDirty = true
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
	b.sendTypedInput(&game.Respawn{Sequence: b.inputSeq}, true)
}

// Chat sends a chat message (reliable, typed).
func (b *Bot) Chat(text string) {
	b.inputSeq++
	b.sendTypedInput(&game.Chat{Sequence: b.inputSeq, Username: b.name, Text: text}, true)
}

// DepositItem transfers an item from cargo to bank (reliable, typed).
func (b *Bot) DepositItem(itemID uint32, qty int32) {
	b.inputSeq++
	b.sendTypedInput(&game.InventoryTransfer{
		Sequence: b.inputSeq, ItemID: itemID, Quantity: qty, Deposit: true,
	}, true)
}

// WithdrawItem transfers an item from bank to cargo (reliable, typed).
func (b *Bot) WithdrawItem(itemID uint32, qty int32) {
	b.inputSeq++
	b.sendTypedInput(&game.InventoryTransfer{
		Sequence: b.inputSeq, ItemID: itemID, Quantity: qty, Deposit: false,
	}, true)
}

// RequestBank requests bank contents (reliable, typed).
func (b *Bot) RequestBank() {
	b.inputSeq++
	b.sendTypedInput(&game.BankRequest{Sequence: b.inputSeq}, true)
}

// sendEvent sends a legacy channel-0x00 ClientEvent. Used only for
// CE_LOGIN — every game-input code has migrated to typed-input
// (channel 0x02). Frame layout: [u8 0x00][marshaled enginepb.ClientEvent].
func (b *Bot) sendEvent(code uint32, payload proto.Message, reliable bool) {
	inner, err := proto.Marshal(payload)
	if err != nil {
		return
	}
	evt := &enginepb.ClientEvent{Code: code, Data: inner}
	data, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	frame := make([]byte, 1+len(data))
	frame[0] = pkgnet.ChannelEvent
	copy(frame[1:], data)
	if reliable {
		b.conn.SendReliable(frame)
	} else {
		b.conn.SendUnreliable(frame)
	}
}

// sendTypedInput marshals a HandleClient-eligible Go message via the
// reflection codec, wraps it in the channel-0x02 wire frame, and sends
// it. msg must point at a registered HandleClient type — see
// internal/game/input_messages.go for the canonical set. Frame layout:
//
//	[u8 0x02][u32 typeID][u32 bodyLen][body bytes]
//
// The 4-byte body-length prefix matches the gateway-side decoder in
// pkg/net/conn.go (typed-input parsing).
func (b *Bot) sendTypedInput(msg any, reliable bool) {
	t := reflect.TypeOf(msg)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	typeID := mmokit.TypeIDOf(t)

	body := pkguniverse.ReflectMarshal(msg)
	frame := make([]byte, 1+8+len(body))
	frame[0] = pkgnet.ChannelClientInput
	binary.LittleEndian.PutUint32(frame[1:5], typeID)
	binary.LittleEndian.PutUint32(frame[5:9], uint32(len(body)))
	copy(frame[9:], body)
	if reliable {
		b.conn.SendReliable(frame)
	} else {
		b.conn.SendUnreliable(frame)
	}
}
