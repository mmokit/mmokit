// Package persist defines domain-aware repository interfaces for
// persistent game state. Implementations live in subpackages
// (pkg/persist/postgres). Game-domain code depends only on the
// interfaces here, never on a specific backend.
//
// There is no generic key-value Store interface in the new design —
// every persistence operation is typed to its domain
// (PlayerRepository, MarketRepository, ConfigRepository).
//
// Note: during the S5 transition the legacy generic Store interface
// from store.go and the bbolt.go BoltStore implementation still
// coexist with the new repository interfaces. They are deleted in
// the final S5 task once every consumer has been migrated.
package persist
