package system

import (
	"strings"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/engine"
)

// InputSystem drains client input messages into PlayerInput components.
type InputSystem struct {
	gw *game.GameWorld
}

func NewInputSystem(gw *game.GameWorld) *InputSystem { return &InputSystem{gw: gw} }

func (s *InputSystem) Update(dt float32) {
	gw := s.gw

	// Process inputs for alive players
	for connID, entity := range gw.Players.Entities {
		if !gw.ECS.Alive(entity) {
			continue
		}
		if gw.C.Ghost.HasAll(entity) {
			continue
		}

		msgs := gw.ConnMgr.DrainInput(connID)
		if len(msgs) == 0 {
			continue
		}

		isDocking := gw.Players.Docking[connID] != nil
		input := gw.C.PlayerInput.Get(entity)

		for _, data := range msgs {
			var evt gamepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}

			switch gamepb.ClientEventCode(evt.Code) {
			case gamepb.ClientEventCode_CE_PLAYER_INPUT:
				var m gamepb.PlayerInputMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				// Suppress movement/ability input while docking
				if isDocking {
					input.Sequence = m.Sequence
					continue
				}
				input.Sequence = m.Sequence
				input.JettisonItemID = m.Jettison
				input.AbilityCast = m.AbilityCast
				input.LockTargetNetID = m.LockTargetId

				// Click-to-move: update MoveTarget component
				// Client sends positions relative to player's sector origin
				if m.MoveActive && gw.C.MoveTarget.HasAll(entity) {
					mt := gw.C.MoveTarget.Get(entity)
					mt.X = m.MoveX
					mt.Y = m.MoveY
					if gw.C.SectorCoord.HasAll(entity) {
						sec := gw.C.SectorCoord.Get(entity)
						mt.SX = sec.SX
						mt.SY = sec.SY
					}
					mt.Active = true
				}

				netID := gw.C.NetworkID.Get(entity).ID
				gw.Log.Log(game.CatInput, "player=%d abilities=0x%x lock=%d seq=%d",
					netID, input.AbilityCast, input.LockTargetNetID, input.Sequence)

			case gamepb.ClientEventCode_CE_DOCK:
				engine.Enqueue(gw.Queue, game.PendingDockRequest{
					ConnID: connID,
				})

			case gamepb.ClientEventCode_CE_CHAT:
				var m gamepb.ChatMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				text := strings.TrimSpace(m.Text)
				if len(text) == 0 || len(text) > 200 {
					continue
				}
				username := gw.Players.Usernames[connID]
				engine.Enqueue(gw.Queue, &gamepb.ChatMsg{
					Username: username,
					Text:     text,
				})
				gw.Log.Log(game.CatChat, "<%s> %s", username, text)
				gw.Bridge.ChatRelay(username, text)

			case gamepb.ClientEventCode_CE_INVENTORY_TRANSFER:
				var m gamepb.InventoryTransferMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingTransfer{
					ConnID:  connID,
					ItemID:  m.ItemId,
					Amount:  m.Quantity,
					Deposit: m.Deposit,
				})

			case gamepb.ClientEventCode_CE_BANK_REQUEST:
				engine.Enqueue(gw.Queue, game.PendingBankRequest{
					ConnID: connID,
				})

			case gamepb.ClientEventCode_CE_SELL_BANK_ITEM:
				var m gamepb.SellBankItemMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingSellRequest{
					ConnID: connID,
					ItemID: m.ItemId,
					Amount: m.Quantity,
				})

			case gamepb.ClientEventCode_CE_EQUIP:
				var m gamepb.EquipRequestMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingEquipRequest{
					ConnID: connID,
					ItemID: m.ItemId,
					Slot:   item.EquipSlot(m.Slot),
				})

			case gamepb.ClientEventCode_CE_SHOP_BUY:
				var m gamepb.ShopBuyMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingShopBuy{
					ConnID: connID,
					ItemID: m.ItemId,
					Qty:    m.Quantity,
				})

			case gamepb.ClientEventCode_CE_LOOT_ITEM:
				var m gamepb.LootItemMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingLootItem{
					ConnID:     connID,
					CrateNetID: m.CrateNetId,
					ItemID:     m.ItemId,
				})

			case gamepb.ClientEventCode_CE_LOOT_ALL:
				var m gamepb.LootAllMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingLootAll{
					ConnID:     connID,
					CrateNetID: m.CrateNetId,
				})

				}
		}
	}

	// Process inputs for dead players (looking for respawn requests)
	for connID := range gw.Players.Dead {
		msgs := gw.ConnMgr.DrainInput(connID)
		for _, data := range msgs {
			var evt gamepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}

			switch gamepb.ClientEventCode(evt.Code) {
			case gamepb.ClientEventCode_CE_RESPAWN:
				gw.Log.Log(game.CatSpawn, "respawn requested: conn=%d", connID)
				engine.Enqueue(gw.Queue, game.PendingRespawn{ConnID: connID})
			}
		}
	}

	// Process inputs for docked players (undock, bank, shop, etc.)
	for connID := range gw.Players.Docked {
		msgs := gw.ConnMgr.DrainInput(connID)
		for _, data := range msgs {
			var evt gamepb.ClientEvent
			if err := proto.Unmarshal(data, &evt); err != nil {
				continue
			}

			switch gamepb.ClientEventCode(evt.Code) {
			case gamepb.ClientEventCode_CE_UNDOCK:
				engine.Enqueue(gw.Queue, game.PendingUndockRequest{
					ConnID: connID,
				})

			case gamepb.ClientEventCode_CE_INVENTORY_TRANSFER:
				var m gamepb.InventoryTransferMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingTransfer{
					ConnID:  connID,
					ItemID:  m.ItemId,
					Amount:  m.Quantity,
					Deposit: m.Deposit,
				})

			case gamepb.ClientEventCode_CE_BANK_REQUEST:
				engine.Enqueue(gw.Queue, game.PendingBankRequest{
					ConnID: connID,
				})

			case gamepb.ClientEventCode_CE_SELL_BANK_ITEM:
				var m gamepb.SellBankItemMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingSellRequest{
					ConnID: connID,
					ItemID: m.ItemId,
					Amount: m.Quantity,
				})

			case gamepb.ClientEventCode_CE_EQUIP:
				var m gamepb.EquipRequestMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingEquipRequest{
					ConnID: connID,
					ItemID: m.ItemId,
					Slot:   item.EquipSlot(m.Slot),
				})

			case gamepb.ClientEventCode_CE_SHOP_BUY:
				var m gamepb.ShopBuyMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				engine.Enqueue(gw.Queue, game.PendingShopBuy{
					ConnID: connID,
					ItemID: m.ItemId,
					Qty:    m.Quantity,
				})

			case gamepb.ClientEventCode_CE_CHAT:
				var m gamepb.ChatMsg
				if err := proto.Unmarshal(evt.Data, &m); err != nil {
					continue
				}
				text := strings.TrimSpace(m.Text)
				if len(text) == 0 || len(text) > 200 {
					continue
				}
				username := gw.Players.Usernames[connID]
				engine.Enqueue(gw.Queue, &gamepb.ChatMsg{
					Username: username,
					Text:     text,
				})
				gw.Log.Log(game.CatChat, "<%s> %s (docked)", username, text)
				gw.Bridge.ChatRelay(username, text)

			}
		}
	}
}
