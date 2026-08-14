package commands

import (
	"context"
	"fmt"
	"strings"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/examples/space/internal/game"
	"github.com/zenion/mmokit/examples/space/internal/item"
	"github.com/zenion/mmokit/pkg/cmdsys"
	"github.com/zenion/mmokit/pkg/mmokit"
)

type GiveArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Item     string `cmd:"help=resource name (ore/crystal/gas/metal)"`
	Qty      int32  `cmd:"help=quantity to give"`
}

type GiveResult struct {
	Target string
	Item   string
	Given  int32
	Added  int32
	Status string
}

func registerGive(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.give",
		Capability:  "player.give",
		Description: "add a resource item to a player's cargo (online or offline)",
		Route:       cmdsys.RoutePlayerHomeOrOwner,
		Args:        GiveArgs{},
		Result:      GiveResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(GiveArgs)
			username := strings.ToLower(args.Username)
			itemID, ok := resolveResource(strings.ToLower(args.Item))
			if !ok {
				return nil, fmt.Errorf("unknown resource %q", args.Item)
			}
			target := mmokit.ResolvePlayerTarget(env, username)

			if target.Online != nil && target.Stage != nil {
				if gw := gwForStage(coord, target.Stage); gw == nil {
					return nil, fmt.Errorf("player.give: not a game-world cell")
				}
				return mmokit.CmdOnLoop(ctx, target.Stage.Engine(), func() (GiveResult, error) {
					e := mmokit.EntityFromECS(target.Stage, target.Online.Entity)
					inv := mmokit.Get[gamecomp.Inventory](e)
					if inv == nil {
						return GiveResult{}, fmt.Errorf("player has no inventory")
					}
					added := inv.AddItem(itemID, args.Qty)
					return GiveResult{
						Target: username,
						Item:   itemNameOrPlaceholder(itemID),
						Given:  args.Qty,
						Added:  added,
						Status: "online",
					}, nil
				})
			}

			if target.Offline != nil {
				pd, ok := target.Offline.(*game.PlayerData)
				if !ok {
					return nil, fmt.Errorf("player.give: offline accessor type mismatch")
				}
				if pd.Cargo == nil {
					pd.Cargo = make(map[uint32]int32)
				}
				pd.Cargo[itemID] += args.Qty
				target.DirtyMark()
				return GiveResult{
					Target: username,
					Item:   itemNameOrPlaceholder(itemID),
					Given:  args.Qty,
					Added:  args.Qty,
					Status: "offline",
				}, nil
			}

			return nil, fmt.Errorf("player %q not found", username)
		},
	})
}

// resolveResource maps short resource names to item IDs.
func resolveResource(input string) (uint32, bool) {
	input = strings.ToLower(input)
	for _, def := range item.All() {
		if def.Category == item.CategoryResource && strings.HasPrefix(strings.ToLower(def.Name), input) {
			return def.ID, true
		}
	}
	return 0, false
}

// itemNameOrPlaceholder returns the item name for the given ID, or a fallback string.
func itemNameOrPlaceholder(id uint32) string {
	if def := item.Get(id); def != nil {
		return def.Name
	}
	return fmt.Sprintf("item#%d", id)
}
