package game

import (
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// BankRequest is the typed-op request for fetching the calling player's
// current bank contents. Replaces the channel-0x00 BankRequest typed
// input — Plan 1 left it on the events channel because at that point the
// ops channel wasn't typed yet; Plan 2 Phase 3 migrates it to a
// RoutePlayerCell typed-op (req/res correlation via the typed-op promise
// correlator on the client).
//
// The same BankContents payload still flows out-of-band as a typed
// server event when the server mutates state on its own (admin tools,
// future automated payouts, deposits/withdraws via InventoryTransfer
// inputs) — see GameWorld.SendBankContents.
type BankRequest struct {
	// Sequence echoes the client's input-sequence counter so the UI can
	// match a refresh against an in-flight transfer. Optional; ignored
	// by the server today.
	Sequence uint32
}

// BankResponse carries the post-handler bank state. Reuses BankContents
// as its body — same Go struct used by the typed-event push so consumers
// can render with one rendering path regardless of whether the bank
// update came from a request or an out-of-band server push.
//
// Error is populated when the handler rejects the request (e.g.
// player not docked, no bank access). Empty string on success.
type BankResponse struct {
	Contents BankContents
	Error    string
}

// HandleBankRequest is the typed-op handler. Runs on the player's cell
// engine via Process.DispatchCellRoutedOp → SubmitLoopJob, so it has
// safe access to ECS state. Replaces the per-tick queue drain that
// preceded the typed-op migration.
func HandleBankRequest(ctx *mmokit.OpContext, _ *BankRequest) (*BankResponse, error) {
	stage := mmokit.OpContextStage(ctx)
	if stage == nil {
		return &BankResponse{Error: "no cell context"}, nil
	}
	gw := mmokit.State[GameWorld](stage)
	if gw == nil {
		return &BankResponse{Error: "no game world"}, nil
	}

	sess := gw.Engine().Players.ByConnID(ctx.ConnID)
	if sess == nil || sess.Username == "" {
		return &BankResponse{Error: "no active session"}, nil
	}

	// Docked players skip the entity / proximity check — bank UI is
	// always available while docked.
	if sess.State != StateDocked {
		entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
		if !entity.Alive() {
			return &BankResponse{Error: "no live entity"}, nil
		}
		pos := mmokit.Get[mmokit.Position](entity)
		if pos == nil {
			return &BankResponse{Error: "no position"}, nil
		}
		sellRange2 := float64(gw.Config.SellRange) * float64(gw.Config.SellRange)
		if !nearAnyStation(gw, pos, sellRange2) {
			return &BankResponse{Error: "not near station"}, nil
		}
	}

	pdata := gw.PlayerDB.Bind(sess)
	return &BankResponse{Contents: buildBankContents(gw, pdata)}, nil
}

// nearAnyStation iterates every Station component on the cell stage and
// returns true if any falls within range2 of pos. Used by op_bank and
// the bank-transfer deferred handler in system_economy.go.
func nearAnyStation(gw *GameWorld, pos *mmokit.Position, range2 float64) bool {
	var near bool
	mmokit.ForEach2(gw.stage, func(_ mmokit.Entity, _ *gamecomp.Station, sp *mmokit.Position) {
		if near {
			return
		}
		dx := float64(pos.X - sp.X)
		dy := float64(pos.Y - sp.Y)
		if dx*dx+dy*dy <= range2 {
			near = true
		}
	})
	return near
}

// buildBankContents snapshots the player's bank, docked cargo, and
// currency balances into a BankContents message. Mirrors GameWorld.
// SendBankContents but returns the value instead of sending it.
func buildBankContents(gw *GameWorld, pdata *PlayerData) BankContents {
	var items []InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, InventoryItem{ItemID: id, Quantity: qty})
		}
	}
	var cargoItems []InventoryItem
	for id, qty := range pdata.Cargo {
		if qty > 0 {
			cargoItems = append(cargoItems, InventoryItem{ItemID: id, Quantity: qty})
		}
	}
	var currencies []CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, CurrencyBalance{CurrencyID: curID, Balance: bal})
		}
	}
	return BankContents{
		Items:        items,
		TotalMass:    pdata.BankTotalMass(),
		MaxMass:      gw.Config.BankMaxMass,
		CargoItems:   cargoItems,
		CargoMass:    pdata.CargoTotalMass(),
		MaxCargoMass: gw.Config.MaxCargo,
		Currencies:   currencies,
	}
}
