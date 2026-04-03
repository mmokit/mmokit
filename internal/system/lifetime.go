package system

import "github.com/zenion/mmoserver/pkg/mmokit"

// LifetimeSystem despawns entities whose Lifetime component has expired.
// Delegates to the generic engine lifetime system via embedding.
type LifetimeSystem = mmokit.LifetimeSystem
