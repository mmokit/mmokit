// Package facadetest holds the black-box test suite for the mmokit facade at
// the module root.
//
// These tests were previously `package mmokit_test` files sitting beside the
// facade sources. They live here instead so the root package directory stays
// browsable, and the separation is load-bearing rather than cosmetic: nothing
// in this package can reach an unexported facade identifier, so every test
// here exercises mmokit exactly the way a game does — through the public API
// and nothing else.
//
// White-box tests that legitimately need package internals stay at the root as
// `package mmokit`. If a test here starts wanting an unexported symbol, that
// is the signal to move the test back rather than to export the symbol.
package facadetest
