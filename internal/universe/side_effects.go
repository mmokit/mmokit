package universe

import (
	"github.com/zenion/mmoserver/internal/game"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// buildSideEffectRegistry creates a SideEffectRegistry with all game-specific
// side effect handlers. The closures capture the GameWorld so they can apply
// effects to local players.
func buildSideEffectRegistry(gw *game.GameWorld) *pkguniverse.SideEffectRegistry {
	reg := pkguniverse.NewSideEffectRegistry()

	reg.Register(pkguniverse.SideEffectHandler{
		Type: game.SideEffectCurrency,
		Handle: func(sourceNetID uint32, payload []byte) {
			currencyID, amount := game.UnmarshalCurrencyReward(payload)
			if amount > 0 {
				gw.RewardCurrencyToLocal(sourceNetID, currencyID, amount)
			}
		},
	})

	return reg
}
