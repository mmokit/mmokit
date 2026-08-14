package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmokit/examples/space/internal/game"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type CurrencyArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Amount     int64  `cmd:"help=new balance amount"`
	CurrencyID uint32 `cmd:"optional,help=currency ID (default: settlement currency)"`
}

type CurrencyResult struct {
	Target     string
	CurrencyID uint32
	NewBalance int64
	Status     string
}

func registerCurrency(reg *cmdsys.Registry, coord *mmokit.Process, playerDB *game.PlayerRepo, cfg **game.GameConfig) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.currency",
		Capability:  "player.currency",
		Description: "set a player's currency balance (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        CurrencyArgs{},
		Result:      CurrencyResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(CurrencyArgs)
			username := strings.ToLower(args.Username)
			curID := args.CurrencyID
			if curID == 0 && cfg != nil && *cfg != nil {
				curID = (*cfg).SettlementCurrencyID
			}
			target := mmokit.ResolvePlayerTarget(env, username)

			pdata := playerDB.Get(username)
			if pdata == nil {
				return nil, fmt.Errorf("player %q not loaded — they must have logged in at least once", username)
			}
			if pdata.Currencies == nil {
				pdata.Currencies = make(map[uint32]int64)
			}
			pdata.Currencies[curID] = args.Amount
			playerDB.MarkDirty(username)

			if target.Online != nil && target.Stage != nil {
				gw := gwForStage(coord, target.Stage)
				if gw != nil {
					_, _ = mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (struct{}, error) {
						sendBankContentsAdmin(gw, target.Online.ConnID, pdata, *cfg)
						return struct{}{}, nil
					})
				}
				return CurrencyResult{
					Target:     username,
					CurrencyID: curID,
					NewBalance: args.Amount,
					Status:     "online",
				}, nil
			}

			if target.Offline != nil {
				return CurrencyResult{
					Target:     username,
					CurrencyID: curID,
					NewBalance: args.Amount,
					Status:     "offline",
				}, nil
			}

			return nil, fmt.Errorf("player %q not found", username)
		},
	})
}

// sendBankContentsAdmin sends a typed BankContents event to a player.
func sendBankContentsAdmin(gw *game.GameWorld, connID uint32, pdata *game.PlayerData, cfg *game.GameConfig) {
	var items []game.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, game.InventoryItem{ItemID: id, Quantity: qty})
		}
	}
	var currencies []game.CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, game.CurrencyBalance{CurrencyID: curID, Balance: bal})
		}
	}
	var bankMaxMass float32
	if cfg != nil {
		bankMaxMass = cfg.BankMaxMass
	}
	mmokit.SendEvent(gw.GetStage(), connID, &game.BankContents{
		Items:      items,
		TotalMass:  pdata.BankTotalMass(),
		MaxMass:    bankMaxMass,
		Currencies: currencies,
	})
}
