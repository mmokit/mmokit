package game

import (
	"strings"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// PlayerInputDeps is the deps struct for the high-frequency CE_PLAYER_INPUT
// handler. PlayerInput is required (every active player carries one);
// MoveTarget is optional (some craft don't move).
type PlayerInputDeps struct {
	Input      *gamecomp.PlayerInput
	NetworkID  *mmokit.NetworkID
	MoveTarget *mmokit.MoveTarget `ecs:"optional"`
}

// gameWorldFromPlayer resolves the cell-local GameWorld for a player.
// Returns nil if the player isn't bound to a stage (shouldn't happen
// for an Active session in a normal tick — the dispatcher only fires
// on Active+).
func gameWorldFromPlayer(p *mmokit.Player) *GameWorld {
	stage := p.Stage()
	if stage == nil {
		return nil
	}
	cell := stage.Process().CellByID(stage.CellID())
	if cell == nil {
		return nil
	}
	return UnwrapGameWorld(cell.World)
}

// RegisterInputs installs every space-game input handler on the process.
// Called from GameSetup(coord) instead of mmo.AddSystem(NewInputSystem).
//
// Pattern: continuous-state inputs use OnInputWith with a deps struct;
// discrete actions use OnInput and call the existing gw.Queue. The
// gw.Queue + Pending* enqueue layer is preserved as-is — this redesign
// is about the input registration surface, not the task queue.
func RegisterInputs(mmo *mmokit.Process) {
	mmokit.OnInputWith[gamepb.PlayerInputMsg, PlayerInputDeps](
		mmo, enginepb.ClientEventCode_CE_PLAYER_INPUT,
	).States(mmokit.StateActive, StateDocking).
		Do(func(p *mmokit.Player, msg *gamepb.PlayerInputMsg, c *PlayerInputDeps) {
			if p.State() == StateDocking {
				c.Input.Sequence = msg.Sequence
				return
			}
			prevAbilityCast := c.Input.AbilityCast
			prevLockTarget := c.Input.LockTargetNetID

			c.Input.Sequence = msg.Sequence
			c.Input.JettisonItemID = msg.Jettison
			c.Input.AbilityCast = msg.AbilityCast
			c.Input.LockTargetNetID = msg.LockTargetId

			// Click-to-move: update MoveTarget component. Direction-vector
			// input mode is no longer supported — click-to-move only.
			if msg.MoveActive && c.MoveTarget != nil {
				c.MoveTarget.SetTarget(msg.MoveX, msg.MoveY)
			}

			// Log only on state transitions to avoid per-packet spam.
			if c.Input.AbilityCast != prevAbilityCast || c.Input.LockTargetNetID != prevLockTarget {
				p.Engine().Log.Log(CatPlayerInput, "player=%d abilities=0x%x lock=%d seq=%d",
					c.NetworkID.ID, c.Input.AbilityCast, c.Input.LockTargetNetID, c.Input.Sequence)
			}
		})

	mmokit.OnInput[enginepb.ChatMsg](mmo, enginepb.ClientEventCode_CE_CHAT).
		States(mmokit.StateActive, StateDocking, StateDocked).
		Do(func(p *mmokit.Player, msg *enginepb.ChatMsg) {
			text := strings.TrimSpace(msg.Text)
			if len(text) == 0 || len(text) > 200 {
				return
			}
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			username := p.Username()
			mmokit.Enqueue(gw.Queue, &enginepb.ChatMsg{
				Username: username,
				Text:     text,
			})
			gw.eng.Log.Log(CatPlayerChat, "<%s> %s", username, text)
			gw.Bridge().RelayChatToOtherCells(username, text)
		})

	mmokit.OnInput[gamepb.DockRequestMsg](mmo, gamepb.GameClientEventCode_GCE_DOCK).
		Active().
		Do(func(p *mmokit.Player, _ *gamepb.DockRequestMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingDockRequest{ConnID: p.ConnID()})
		})

	mmokit.OnInput[gamepb.UndockRequestMsg](mmo, gamepb.GameClientEventCode_GCE_UNDOCK).
		States(StateDocked).
		Do(func(p *mmokit.Player, _ *gamepb.UndockRequestMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingUndockRequest{ConnID: p.ConnID()})
		})

	mmokit.OnInput[gamepb.RespawnRequestMsg](mmo, gamepb.GameClientEventCode_GCE_RESPAWN).
		States(StateDead).
		Do(func(p *mmokit.Player, _ *gamepb.RespawnRequestMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			gw.eng.Log.Log(CatPlayerSpawn, "respawn requested: conn=%d", p.ConnID())
			mmokit.Enqueue(gw.Queue, PendingRespawn{ConnID: p.ConnID()})
		})

	mmokit.OnInput[gamepb.InventoryTransferMsg](mmo, gamepb.GameClientEventCode_GCE_INVENTORY_TRANSFER).
		States(mmokit.StateActive, StateDocked).
		Do(func(p *mmokit.Player, msg *gamepb.InventoryTransferMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingTransfer{
				ConnID:  p.ConnID(),
				ItemID:  msg.ItemId,
				Amount:  msg.Quantity,
				Deposit: msg.Deposit,
			})
		})

	mmokit.OnInput[gamepb.BankRequestMsg](mmo, gamepb.GameClientEventCode_GCE_BANK_REQUEST).
		States(mmokit.StateActive, StateDocked).
		Do(func(p *mmokit.Player, _ *gamepb.BankRequestMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingBankRequest{ConnID: p.ConnID()})
		})

	mmokit.OnInput[gamepb.SellBankItemMsg](mmo, gamepb.GameClientEventCode_GCE_SELL_BANK_ITEM).
		States(mmokit.StateActive, StateDocked).
		Do(func(p *mmokit.Player, msg *gamepb.SellBankItemMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingSellRequest{
				ConnID: p.ConnID(),
				ItemID: msg.ItemId,
				Amount: msg.Quantity,
			})
		})

	mmokit.OnInput[gamepb.EquipRequestMsg](mmo, gamepb.GameClientEventCode_GCE_EQUIP).
		States(mmokit.StateActive, StateDocked).
		Do(func(p *mmokit.Player, msg *gamepb.EquipRequestMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingEquipRequest{
				ConnID: p.ConnID(),
				ItemID: msg.ItemId,
				Slot:   item.EquipSlot(msg.Slot),
			})
		})

	mmokit.OnInput[gamepb.ShopBuyMsg](mmo, gamepb.GameClientEventCode_GCE_SHOP_BUY).
		States(mmokit.StateActive, StateDocked).
		Do(func(p *mmokit.Player, msg *gamepb.ShopBuyMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingShopBuy{
				ConnID: p.ConnID(),
				ItemID: msg.ItemId,
				Qty:    msg.Quantity,
			})
		})

	mmokit.OnInput[gamepb.LootItemMsg](mmo, gamepb.GameClientEventCode_GCE_LOOT_ITEM).
		Active().
		Do(func(p *mmokit.Player, msg *gamepb.LootItemMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingLootItem{
				ConnID:     p.ConnID(),
				CrateNetID: msg.CrateNetId,
				ItemID:     msg.ItemId,
			})
		})

	mmokit.OnInput[gamepb.LootAllMsg](mmo, gamepb.GameClientEventCode_GCE_LOOT_ALL).
		Active().
		Do(func(p *mmokit.Player, msg *gamepb.LootAllMsg) {
			gw := gameWorldFromPlayer(p)
			if gw == nil {
				return
			}
			mmokit.Enqueue(gw.Queue, PendingLootAll{
				ConnID:     p.ConnID(),
				CrateNetID: msg.CrateNetId,
			})
		})
}
