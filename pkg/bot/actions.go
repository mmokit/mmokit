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
	mine         bool
	mineTargetID uint32
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

// StartMining starts mining a target asteroid.
func (b *Bot) StartMining(targetNetID uint32) {
	b.inputMu.Lock()
	b.pending.mine = true
	b.pending.mineTargetID = targetNetID
	b.inputMu.Unlock()
}

// StopMining stops mining.
func (b *Bot) StopMining() {
	b.inputMu.Lock()
	b.pending.mine = false
	b.pending.mineTargetID = 0
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
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_Respawn{Respawn: &gamepb.RespawnRequestMsg{}},
	})
}

// Chat sends a chat message (reliable).
func (b *Bot) Chat(text string) {
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_Chat{Chat: &gamepb.ChatMsg{
			Username: b.name,
			Text:     text,
		}},
	})
}

// DepositItem transfers an item from cargo to bank (reliable).
func (b *Bot) DepositItem(itemID uint32, qty float32) {
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_Transfer{Transfer: &gamepb.InventoryTransferMsg{
			ItemId:   itemID,
			Quantity: qty,
			Deposit:  true,
		}},
	})
}

// WithdrawItem transfers an item from bank to cargo (reliable).
func (b *Bot) WithdrawItem(itemID uint32, qty float32) {
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_Transfer{Transfer: &gamepb.InventoryTransferMsg{
			ItemId:   itemID,
			Quantity: qty,
			Deposit:  false,
		}},
	})
}

// RequestBank requests bank contents (reliable).
func (b *Bot) RequestBank() {
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_BankRequest{BankRequest: &gamepb.BankRequestMsg{}},
	})
}

// SellBankItem sells an item from the bank (reliable).
func (b *Bot) SellBankItem(itemID uint32, qty float32) {
	b.sendReliable(&gamepb.ClientMessage{
		Msg: &gamepb.ClientMessage_SellBankItem{SellBankItem: &gamepb.SellBankItemMsg{
			ItemId:   itemID,
			Quantity: qty,
		}},
	})
}

func (b *Bot) sendReliable(msg *gamepb.ClientMessage) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return
	}
	b.conn.SendReliable(data)
}
