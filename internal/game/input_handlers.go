package game

import (
	"strings"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// SetupInputHandlers registers all input handlers on the given router.
func SetupInputHandlers(router *mmokit.InputRouter, gw *GameWorld) {
	// Skip input for ghost entities (mid-transfer, owned by another node)
	router.StateFilter(mmokit.StateActive, func(ctx *mmokit.InputContext) bool {
		return !gw.C.Ghost.HasAll(ctx.Entity)
	})

	movementStates := mmokit.States(mmokit.StateActive, StateDocking)
	tradingStates := mmokit.States(mmokit.StateActive, StateDocked)
	chatStates := mmokit.States(mmokit.StateActive, StateDocking, StateDocked)

	mmokit.Handle(router, enginepb.ClientEventCode_CE_PLAYER_INPUT, movementStates, handlePlayerInput(gw))
	mmokit.Handle(router, enginepb.ClientEventCode_CE_CHAT, chatStates, handleChat(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_DOCK), mmokit.States(mmokit.StateActive), handleDock(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_UNDOCK), mmokit.States(StateDocked), handleUndock(gw))
	router.Handle(uint32(enginepb.ClientEventCode_CE_RESPAWN), mmokit.States(StateDead), handleRespawn(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_INVENTORY_TRANSFER, tradingStates, handleInventoryTransfer(gw))
	router.Handle(uint32(gamepb.GameClientEventCode_GCE_BANK_REQUEST), tradingStates, handleBankRequest(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_SELL_BANK_ITEM, tradingStates, handleSellBankItem(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_EQUIP, tradingStates, handleEquip(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_SHOP_BUY, tradingStates, handleShopBuy(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_LOOT_ITEM, mmokit.States(mmokit.StateActive), handleLootItem(gw))
	mmokit.Handle(router, gamepb.GameClientEventCode_GCE_LOOT_ALL, mmokit.States(mmokit.StateActive), handleLootAll(gw))
}

// handlePlayerInput processes CE_PLAYER_INPUT. When docking, only sequence is updated.
func handlePlayerInput(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.PlayerInputMsg) {
		entity := ctx.Entity
		input := gw.C.PlayerInput.Get(entity)

		// Suppress movement/ability input while docking
		if ctx.Session.State == StateDocking {
			input.Sequence = msg.Sequence
			return
		}

		prevAbilityCast := input.AbilityCast
		prevLockTarget := input.LockTargetNetID

		input.Sequence = msg.Sequence
		input.JettisonItemID = msg.Jettison
		input.AbilityCast = msg.AbilityCast
		input.LockTargetNetID = msg.LockTargetId

		// Click-to-move: update MoveTarget component. The client sends the
		// destination in world-absolute coordinates; MoveTarget stores cell-local
		// coordinates plus a base cell index, so we must convert here.
		// Direction-vector input mode is no longer supported — click-to-move
		// is the only movement mode.
		if msg.MoveActive && gw.C.MoveTarget.HasAll(entity) {
			mt := gw.C.MoveTarget.Get(entity)
			mmokit.SetMoveTarget(mt, msg.MoveX, msg.MoveY)
		}

		// Log only on state transitions to avoid per-packet spam (~20 packets/sec
		// per player). Movement is noisy and uninteresting; abilities and lock
		// target changes are the signals worth capturing.
		if input.AbilityCast != prevAbilityCast || input.LockTargetNetID != prevLockTarget {
			netID := gw.C.NetworkID.Get(entity).ID
			gw.eng.Log.Log(CatPlayerInput, "player=%d abilities=0x%x lock=%d seq=%d",
				netID, input.AbilityCast, input.LockTargetNetID, input.Sequence)
		}
	}
}

// handleChat processes CE_CHAT. Trims, validates, enqueues, and relays to other nodes.
func handleChat(gw *GameWorld) func(ctx *mmokit.InputContext, msg *enginepb.ChatMsg) {
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
		gw.eng.Log.Log(CatPlayerChat, "<%s> %s", username, text)
		gw.Bridge().RelayChatToOtherCells(username, text)
	}
}

// handleDock processes GCE_DOCK. Enqueues a dock request.
func handleDock(gw *GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, PendingDockRequest{ConnID: ctx.ConnID})
	}
}

// handleUndock processes GCE_UNDOCK. Enqueues an undock request.
func handleUndock(gw *GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, PendingUndockRequest{ConnID: ctx.ConnID})
	}
}

// handleRespawn processes CE_RESPAWN. Logs and enqueues a respawn request.
func handleRespawn(gw *GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		gw.eng.Log.Log(CatPlayerSpawn, "respawn requested: conn=%d", ctx.ConnID)
		mmokit.Enqueue(gw.Queue, PendingRespawn{ConnID: ctx.ConnID})
	}
}

// handleInventoryTransfer processes GCE_INVENTORY_TRANSFER.
func handleInventoryTransfer(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.InventoryTransferMsg) {
		mmokit.Enqueue(gw.Queue, PendingTransfer{
			ConnID:  ctx.ConnID,
			ItemID:  msg.ItemId,
			Amount:  msg.Quantity,
			Deposit: msg.Deposit,
		})
	}
}

// handleBankRequest processes GCE_BANK_REQUEST.
func handleBankRequest(gw *GameWorld) func(ctx *mmokit.InputContext, data []byte) {
	return func(ctx *mmokit.InputContext, data []byte) {
		mmokit.Enqueue(gw.Queue, PendingBankRequest{ConnID: ctx.ConnID})
	}
}

// handleSellBankItem processes GCE_SELL_BANK_ITEM.
func handleSellBankItem(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.SellBankItemMsg) {
		mmokit.Enqueue(gw.Queue, PendingSellRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Amount: msg.Quantity,
		})
	}
}

// handleEquip processes GCE_EQUIP.
func handleEquip(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.EquipRequestMsg) {
		mmokit.Enqueue(gw.Queue, PendingEquipRequest{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Slot:   item.EquipSlot(msg.Slot),
		})
	}
}

// handleShopBuy processes GCE_SHOP_BUY.
func handleShopBuy(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.ShopBuyMsg) {
		mmokit.Enqueue(gw.Queue, PendingShopBuy{
			ConnID: ctx.ConnID,
			ItemID: msg.ItemId,
			Qty:    msg.Quantity,
		})
	}
}

// handleLootItem processes GCE_LOOT_ITEM.
func handleLootItem(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootItemMsg) {
		mmokit.Enqueue(gw.Queue, PendingLootItem{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
			ItemID:     msg.ItemId,
		})
	}
}

// handleLootAll processes GCE_LOOT_ALL.
func handleLootAll(gw *GameWorld) func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
	return func(ctx *mmokit.InputContext, msg *gamepb.LootAllMsg) {
		mmokit.Enqueue(gw.Queue, PendingLootAll{
			ConnID:     ctx.ConnID,
			CrateNetID: msg.CrateNetId,
		})
	}
}
