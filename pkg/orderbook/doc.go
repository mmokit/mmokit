// Package orderbook is a generic limit-order matching engine: price-time
// priority, partial fills, and aggregated depth views.
//
// It is deliberately game-agnostic. It knows about prices, quantities and
// order identity, and nothing about items, currency, stations, or who is
// allowed to trade. Settlement — validating a trade, moving goods, charging
// fees — belongs to the game.
package orderbook
