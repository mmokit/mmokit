package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/cmdsys"
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
}

func registerGive(reg *cmdsys.Registry, resolver *Resolver) error {
	return reg.Register(cmdsys.Command{
		Verb:        "player.give",
		Capability:  "player.give",
		Description: "add a resource item to a player's cargo",
		Route:       cmdsys.RoutePlayerOwner,
		Args:        GiveArgs{},
		Result:      GiveResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(GiveArgs)
			username := strings.ToLower(args.Username)
			itemName := strings.ToLower(args.Item)
			itemID, ok := resolveResource(itemName)
			if !ok {
				return nil, fmt.Errorf("unknown resource %q", args.Item)
			}
			gw := resolver.GameWorldForUser(username)
			if gw == nil {
				return nil, fmt.Errorf("player %q not found on this host", username)
			}
			var result GiveResult
			_, err := ExecOnLoop(gw, func() (any, error) {
				sess := gw.Players.ByUsername(username)
				if sess == nil {
					return nil, fmt.Errorf("player %q session not found", username)
				}
				entity := sess.Entity
				if !gw.C.Inventory.HasAll(entity) {
					return nil, fmt.Errorf("player has no inventory")
				}
				inv := gw.C.Inventory.Get(entity)
				added := inv.AddItem(itemID, args.Qty)
				defName := fmt.Sprintf("item#%d", itemID)
				if def := item.Get(itemID); def != nil {
					defName = def.Name
				}
				result = GiveResult{
					Target: username,
					Item:   defName,
					Given:  args.Qty,
					Added:  added,
				}
				return result, nil
			})
			if err != nil {
				return nil, err
			}
			return result, nil
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
