package bot

import (
	gamepb "github.com/zenion/mmoserver/gen/go"
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

// pendingInput accumulates input state between input ticks.
type pendingInput struct {
	moveX        float32
	moveY        float32
	moveActive   bool
	lockTargetID uint32
	abilityCast  uint32
	jettison     uint32
}

// MoveTo sets a move-to destination.
func (b *Bot) MoveTo(x, y float32) {
	b.inputMu.Lock()
	b.pending.moveX = x
	b.pending.moveY = y
	b.pending.moveActive = true
	b.inputMu.Unlock()
}

// StopMove clears the move command.
func (b *Bot) StopMove() {
	b.inputMu.Lock()
	b.pending.moveActive = false
	b.inputMu.Unlock()
}

// LockTarget sets the combat lock-on target.
func (b *Bot) LockTarget(netID uint32) {
	b.inputMu.Lock()
	b.pending.lockTargetID = netID
	b.inputMu.Unlock()
}

// ClearLock clears the combat lock-on target.
func (b *Bot) ClearLock() {
	b.inputMu.Lock()
	b.pending.lockTargetID = 0
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

// Respawn sends a respawn request (reliable).
func (b *Bot) Respawn() {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_RESPAWN), &gamepb.RespawnRequestMsg{}, true)
}

// Chat sends a chat message (reliable).
func (b *Bot) Chat(text string) {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_CHAT), &gamepb.ChatMsg{
		Username: b.name,
		Text:     text,
	}, true)
}

// DepositItem transfers an item from cargo to bank (reliable).
func (b *Bot) DepositItem(itemID uint32, qty int32) {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_INVENTORY_TRANSFER), &gamepb.InventoryTransferMsg{
		ItemId:   itemID,
		Quantity: qty,
		Deposit:  true,
	}, true)
}

// WithdrawItem transfers an item from bank to cargo (reliable).
func (b *Bot) WithdrawItem(itemID uint32, qty int32) {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_INVENTORY_TRANSFER), &gamepb.InventoryTransferMsg{
		ItemId:   itemID,
		Quantity: qty,
		Deposit:  false,
	}, true)
}

// RequestBank requests bank contents (reliable).
func (b *Bot) RequestBank() {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_BANK_REQUEST), &gamepb.BankRequestMsg{}, true)
}

// SellBankItem sells an item from the bank (reliable).
func (b *Bot) SellBankItem(itemID uint32, qty int32) {
	b.sendEvent(uint32(gamepb.ClientEventCode_CE_SELL_BANK_ITEM), &gamepb.SellBankItemMsg{
		ItemId:   itemID,
		Quantity: qty,
	}, true)
}

func (b *Bot) sendEvent(code uint32, payload proto.Message, reliable bool) {
	inner, err := proto.Marshal(payload)
	if err != nil {
		return
	}
	evt := &gamepb.ClientEvent{
		Code: code,
		Data: inner,
	}
	data, err := proto.Marshal(evt)
	if err != nil {
		return
	}
	if reliable {
		b.conn.SendReliable(data)
	} else {
		b.conn.SendUnreliable(data)
	}
}
