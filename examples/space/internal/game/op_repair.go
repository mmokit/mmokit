package game

import (
	"fmt"
	"math"

	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/mmokit"
)

// RepairRequest is the typed-op the client sends from the docked station UI
// when the player clicks "Repair." Server validates docked state + sufficient
// credits, charges the player, and restores Health to Max. Shield is excluded
// — it has its own regen path and would blur "repair" with "rearm."
//
// Cost formula: (MaxHP − CurrentHP) × Config.RepairCostPerHP, rounded up.
// A 0 cost (full HP, or RepairCostPerHP=0) succeeds as a no-op so the UI
// can render "fully repaired" without a special-case error.
type RepairRequest struct {
	Sequence uint32
}

// RepairResponse carries the outcome. Cost is the credits actually charged
// (0 on no-op or error). NewHealth is the post-repair Health.Current — same
// value the replication system will push next tick, but echoed here so the
// client can update the HUD without waiting for the next delta.
type RepairResponse struct {
	Success   bool
	Cost      int64
	NewHealth float32
	Error     string
}

// HandleRepairRequest validates and executes a station repair. Runs on the
// player's authoritative cell engine via Process.DispatchCellRoutedOp.
func HandleRepairRequest(ctx *mmokit.OpContext, _ *RepairRequest) (*RepairResponse, error) {
	stage := mmokit.OpContextStage(ctx)
	if stage == nil {
		return &RepairResponse{Error: "no cell context"}, nil
	}
	gw := mmokit.State[GameWorld](stage)
	if gw == nil {
		return &RepairResponse{Error: "no game world"}, nil
	}
	return gw.doRepair(ctx.ConnID), nil
}

// doRepair is the testable core of HandleRepairRequest — runs the session +
// docked + credit checks, applies the repair, and emits the CurrencyUpdate
// echo. Split out from the typed-op handler so unit tests can exercise the
// full damage→repair→spend chain without mocking an OpContext.
func (gw *GameWorld) doRepair(connID uint32) *RepairResponse {
	sess := gw.Engine().Players.ByConnID(connID)
	if sess == nil || sess.Username == "" {
		return &RepairResponse{Error: "no active session"}
	}
	if sess.State != StateDocked {
		return &RepairResponse{Error: "must be docked at a station"}
	}

	entity := mmokit.EntityFromECS(gw.stage, sess.Entity)
	if !entity.Alive() {
		return &RepairResponse{Error: "no live entity"}
	}
	health := mmokit.Get[gamecomp.Health](entity)
	if health == nil {
		return &RepairResponse{Error: "no health component"}
	}

	missing := health.Max - health.Current
	if missing <= 0 {
		return &RepairResponse{Success: true, Cost: 0, NewHealth: health.Current}
	}

	// Ceil so a fractional missing HP still costs at least 1 credit.
	cost := int64(math.Ceil(float64(missing * gw.Config.RepairCostPerHP)))

	pdata := gw.PlayerDB.Bind(sess)
	if cost > 0 && !pdata.SpendCurrency(item.CreditsItemID, cost) {
		have := pdata.GetCurrency(item.CreditsItemID)
		return &RepairResponse{
			Error: fmt.Sprintf("need %d credits, have %d", cost, have),
		}
	}

	health.Current = health.Max
	gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)

	gw.eng.Log.Log(CatEconomyBank, "repair: player=%s cost=%d hp=%.0f/%.0f",
		sess.Username, cost, health.Current, health.Max)

	if cost > 0 {
		mmokit.SendEvent(gw.stage, connID, &CurrencyUpdate{
			CurrencyID: item.CreditsItemID,
			Balance:    pdata.GetCurrency(item.CreditsItemID),
			Earned:     -cost,
		})
	}

	return &RepairResponse{
		Success:   true,
		Cost:      cost,
		NewHealth: health.Current,
	}
}
