package commands

import (
	"context"
	"fmt"
	"strings"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
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

			pdata := playerDB.GetOrCreate(username)
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

// sendBankContentsAdmin sends a BankContentsMsg to a player.
func sendBankContentsAdmin(gw *game.GameWorld, connID uint32, pdata *game.PlayerData, cfg *game.GameConfig) {
	var items []*gamepb.InventoryItem
	for id, qty := range pdata.Bank {
		if qty > 0 {
			items = append(items, &gamepb.InventoryItem{ItemId: id, Quantity: qty})
		}
	}
	var currencies []*gamepb.CurrencyBalance
	for curID, bal := range pdata.Currencies {
		if bal != 0 {
			currencies = append(currencies, &gamepb.CurrencyBalance{CurrencyId: curID, Balance: bal})
		}
	}
	var bankMaxMass float32
	if cfg != nil {
		bankMaxMass = cfg.BankMaxMass
	}
	gw.ServerEvents().Send(gw.Engine().ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_BANK_CONTENTS), &gamepb.BankContentsMsg{
		Items:      items,
		TotalMass:  pdata.BankTotalMass(),
		MaxMass:    bankMaxMass,
		Currencies: currencies,
	})
}
