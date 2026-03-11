package system

import (
	"strings"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/logger"
)

// InputSystem drains client input messages into PlayerInput components.
type InputSystem struct {
	gw *game.GameWorld
}

func NewInputSystem(gw *game.GameWorld) *InputSystem { return &InputSystem{gw: gw} }

func (s *InputSystem) Update(dt float32) {
	gw := s.gw

	// Process inputs for alive players
	for connID, entity := range gw.PlayerEntities {
		if !gw.ECS.Alive(entity) {
			continue
		}

		msgs := gw.ConnMgr.DrainInput(connID)
		if len(msgs) == 0 {
			continue
		}

		input := gw.PlayerInputMap.Get(entity)

		for _, data := range msgs {
			var msg gamepb.ClientMessage
			if err := proto.Unmarshal(data, &msg); err != nil {
				continue
			}

			switch m := msg.Msg.(type) {
			case *gamepb.ClientMessage_Input:
				input.Sequence = m.Input.Sequence
				input.JettisonItemID = m.Input.Jettison
				input.AbilityCast = m.Input.AbilityCast
				input.LockTargetNetID = m.Input.LockTargetId

				// Click-to-move: update MoveTarget component
				if m.Input.MoveActive && gw.MoveTargetMap.HasAll(entity) {
					mt := gw.MoveTargetMap.Get(entity)
					mt.X = m.Input.MoveX
					mt.Y = m.Input.MoveY
					mt.Active = true
				}

				netID := gw.NetworkIDMap.Get(entity).ID
				gw.Log.Log(logger.CatInput, "player=%d abilities=0x%x lock=%d seq=%d",
					netID, input.AbilityCast, input.LockTargetNetID, input.Sequence)

			case *gamepb.ClientMessage_Chat:
				text := strings.TrimSpace(m.Chat.Text)
				if len(text) == 0 || len(text) > 200 {
					continue
				}
				username := gw.ConnToUsername[connID]
				gw.PendingChat = append(gw.PendingChat, &gamepb.ChatMsg{
					Username: username,
					Text:     text,
				})
				gw.Log.Log(logger.CatChat, "<%s> %s", username, text)

			case *gamepb.ClientMessage_Transfer:
				gw.PendingTransfers = append(gw.PendingTransfers, game.PendingTransfer{
					ConnID:  connID,
					ItemID:  m.Transfer.ItemId,
					Amount:  m.Transfer.Quantity,
					Deposit: m.Transfer.Deposit,
				})

			case *gamepb.ClientMessage_BankRequest:
				gw.PendingBankRequests = append(gw.PendingBankRequests, game.PendingBankRequest{
					ConnID: connID,
				})

			case *gamepb.ClientMessage_SellBankItem:
				gw.PendingSellRequests = append(gw.PendingSellRequests, game.PendingSellRequest{
					ConnID: connID,
					ItemID: m.SellBankItem.ItemId,
					Amount: m.SellBankItem.Quantity,
				})

			case *gamepb.ClientMessage_EquipRequest:
				gw.PendingEquipRequests = append(gw.PendingEquipRequests, game.PendingEquipRequest{
					ConnID: connID,
					ItemID: m.EquipRequest.ItemId,
					Slot:   item.EquipSlot(m.EquipRequest.Slot),
				})

			case *gamepb.ClientMessage_ShopBuy:
				gw.PendingShopBuys = append(gw.PendingShopBuys, game.PendingShopBuy{
					ConnID: connID,
					ItemID: m.ShopBuy.ItemId,
					Qty:    m.ShopBuy.Quantity,
				})
			}
		}
	}

	// Process inputs for dead players (looking for respawn requests)
	for connID := range gw.DeadPlayers {
		msgs := gw.ConnMgr.DrainInput(connID)
		for _, data := range msgs {
			var msg gamepb.ClientMessage
			if err := proto.Unmarshal(data, &msg); err != nil {
				continue
			}

			if _, ok := msg.Msg.(*gamepb.ClientMessage_Respawn); ok {
				gw.Log.Log(logger.CatSpawn, "respawn requested: conn=%d", connID)
				gw.PendingRespawns = append(gw.PendingRespawns, connID)
			}
		}
	}
}
