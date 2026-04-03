package system

import (
	"strings"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SetupInputHandlers registers all input handlers on the given router.
func SetupInputHandlers(router *mmokit.InputRouter, gw *game.GameWorld) {
	// Skip input for ghost entities (mid-transfer, owned by another node)
	router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
		return !gw.C.Ghost.HasAll(ctx.Entity)
	})

	movementStates := mmokit.States(mmokit.StateActive, game.StateDocking)
	tradingStates := mmokit.States(mmokit.StateActive, game.StateDocked)
	chatStates := mmokit.States(mmokit.StateActive, game.StateDocking, game.StateDocked)

	mmokit.Handle(router, enginepb.ClientEventCode_CE_PLAYER_INPUT, movementStates, handlePlayerInput(gw))
	mmokit.Handle(router, enginepb.ClientEventCode_CE_CHAT, chatStates, handleChat(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_DOCK), mmokit.States(mmokit.StateActive), handleDock(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_UNDOCK), mmokit.States(game.StateDocked), handleUndock(gw))
	router.Handle(uint32(enginepb.ClientEventCode_CE_RESPAWN), mmokit.States(mmokit.StateDead), handleRespawn(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_INVENTORY_TRANSFER, tradingStates, handleInventoryTransfer(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_BANK_REQUEST), tradingStates, handleBankRequest(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_SELL_BANK_ITEM, tradingStates, handleSellBankItem(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_EQUIP, tradingStates, handleEquip(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_SHOP_BUY, tradingStates, handleShopBuy(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_LOOT_ITEM, mmokit.States(mmokit.StateActive), handleLootItem(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_LOOT_ALL, mmokit.States(mmokit.StateActive), handleLootAll(gw))
}

// handlePlayerInput processes CE_PLAYER_INPUT. When docking, only sequence is updated.
func handlePlayerInput(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
		entity := ctx.Entity
		input := gw.C.PlayerInput.Get(entity)

		// Suppress movement/ability input while docking
		if ctx.Session.State == game.StateDocking {
			input.Sequence = msg.Sequence
			return
		}

		input.Sequence = msg.Sequence
		input.JettisonItemID = msg.Jettison
		input.AbilityCast = msg.AbilityCast
		input.LockTargetNetID = msg.LockTargetId

		// Click-to-move: update MoveTarget component
		if msg.MoveActive && gw.C.MoveTarget.HasAll(entity) {
			mt := gw.C.MoveTarget.Get(entity)
			mt.X = msg.MoveX
			mt.Y = msg.MoveY
			if gw.C.CellCoord.HasAll(entity) {
				sec := gw.C.CellCoord.Get(entity)
				mt.CellX = sec.CellX
				mt.CellY = sec.CellY
			}
			mt.Active = true
			input.DirActive = false // mutual exclusion: destination clears direction
		}

		// Direction-vector mode
		input.DirX = msg.DirX
		input.DirY = msg.DirY
		input.DirActive = msg.DirActive
		if msg.DirActive && gw.C.MoveTarget.HasAll(entity) {
			gw.C.MoveTarget.Get(entity).Active = false // mutual exclusion: direction clears destination
		}

		netID := gw.C.NetworkID.Get(entity).ID
		gw.Log.Log(game.CatPlayerInput, "player=%d abilities=0x%x lock=%d seq=%d",
			netID, input.AbilityCast, input.LockTargetNetID, input.Sequence)
	}
}

// handleChat processes CE_CHAT. Trims, validates, enqueues, and relays to other nodes.
func handleChat(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *enginepb.ChatMsg) {
	return func(ctx *mmokit.InputContext, msg *enginepb.ChatMsg) {
		text := strings.TrimSpace(msg.Text)
		if len(text) == 0 || len(text) > 200 {
			return
		}
		username := ctx.Session.Username
		mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
			Username: username,
			Text:     text,
		})
		gw.Log.Log(game.CatPlayerChat, "<%s> %s", username, text)
		gw.Bridge.RelayChatToOtherNodes(username, text)
	}
}

// handleDock processes GCE_DOCK. Enqueues a dock request.
func handleDock(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingDockRequest{ConnID: ctx.ConnID})
	}
}

// handleUndock processes GCE_UNDOCK. Enqueues an undock request.
func handleUndock(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingUndockRequest{ConnID: ctx.ConnID})
	}
}

// handleRespawn processes CE_RESPAWN. Logs and enqueues a respawn request.
func handleRespawn(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		gw.Log.Log(game.CatPlayerSpawn, "respawn requested: conn=%d", ctx.ConnID)
		mmokit.Enqueue(gw.Queue, game.PendingRespawn{ConnID: ctx.ConnID})
	}
}

// handleInventoryTransfer processes GCE_INVENTORY_TRANSFER.
func handleInventoryTransfer(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingTransfer{
			ConnID:  ctx.ConnID,
			ItemID:  msg.ItemId,
			Amount:  msg.Quantity,
			Deposit: msg.Deposit,
		})
	}
}

// handleBankRequest processes GCE_BANK_REQUEST.
func handleBankRequest(gw *game.GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, game.PendingBankRequest{ConnID: ctx.ConnID})
	}
}

// handleSellBankItem processes GCE_SELL_BANK_ITEM.
func handleSellBankItem(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingSellRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Amount: msg.Quantity,
		})
	}
}

// handleEquip processes GCE_EQUIP.
func handleEquip(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingEquipRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Slot:   item.EquipSlot(msg.Slot),
		})
	}
}

// handleShopBuy processes GCE_SHOP_BUY.
func handleShopBuy(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingShopBuy{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Qty:    msg.Quantity,
		})
	}
}

// handleLootItem processes GCE_LOOT_ITEM.
func handleLootItem(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingLootItem{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
			ItemID:     msg.ItemId,
		})
	}
}

// handleLootAll processes GCE_LOOT_ALL.
func handleLootAll(gw *game.GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
		mmokit.Enqueue(gw.Queue, game.PendingLootAll{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
		})
	}
}
