package marketplace

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type mockBank struct {
	balances map[string]map[uint32]int32
	flux     map[string]int64
}

func newMockBank() *mockBank {
	return &mockBank{
		balances: make(map[string]map[uint32]int32),
		flux:     make(map[string]int64),
	}
}

func (mb *mockBank) set(player string, itemID uint32, qty int32) {
	if mb.balances[player] == nil {
		mb.balances[player] = make(map[uint32]int32)
	}
	mb.balances[player][itemID] = qty
}

func (mb *mockBank) get(player string, itemID uint32) int32 {
	if mb.balances[player] == nil {
		return 0
	}
	return mb.balances[player][itemID]
}

func (mb *mockBank) setFlux(player string, amount int64) {
	if mb.flux == nil {
		mb.flux = make(map[string]int64)
	}
	mb.flux[player] = amount
}

func (mb *mockBank) getFlux(player string) int64 {
	return mb.flux[player]
}

func (mb *mockBank) ops() BankOps {
	return BankOps{
		GetBankBalance: func(player string, itemID uint32) int32 {
			if mb.balances[player] == nil {
				return 0
			}
			return mb.balances[player][itemID]
		},
		ModifyBank: func(player string, fn func(bank map[uint32]int32)) {
			if mb.balances[player] == nil {
				mb.balances[player] = make(map[uint32]int32)
			}
			fn(mb.balances[player])
		},
		GetFlux: func(player string) int64 {
			return mb.flux[player]
		},
		ModifyFlux: func(player string, delta int64) {
			if mb.flux == nil {
				mb.flux = make(map[string]int64)
			}
			mb.flux[player] += delta
		},
		MarkDirty:      func(player string) {},
		SendBankUpdate: func(player string) {},
	}
}

func newTestService(mb *mockBank) *Service {
	return NewService(mb.ops(), DefaultConfig(), logger.New(), nil, nil)
}

func newTestServiceWithConfig(mb *mockBank, cfg Config) *Service {
	return NewService(mb.ops(), cfg, logger.New(), nil, nil)
}

const (
	testItem    uint32 = 10
	testStation uint32 = 1
)

// ---------------------------------------------------------------------------
// Validation tests
// ---------------------------------------------------------------------------

func TestPlaceSellOrder_FluxRejected(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)
	_, err := s.PlaceSellOrder("alice", testStation, fluxItemID, 10, 1)
	if err == nil {
		t.Fatal("expected error for Flux sell")
	}
}

func TestPlaceBuyOrder_FluxRejected(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)
	_, err := s.PlaceBuyOrder("alice", testStation, fluxItemID, 10, 1)
	if err == nil {
		t.Fatal("expected error for Flux buy")
	}
}

func TestPlaceSellOrder_PriceBelowMin(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)
	_, err := s.PlaceSellOrder("alice", testStation, testItem, 0, 1)
	if err == nil {
		t.Fatal("expected error for price below minimum")
	}
}

func TestPlaceSellOrder_ZeroQty(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)
	_, err := s.PlaceSellOrder("alice", testStation, testItem, 10, 0)
	if err == nil {
		t.Fatal("expected error for zero qty")
	}
}

func TestPlaceSellOrder_InsufficientBank(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 5)
	s := newTestService(mb)
	_, err := s.PlaceSellOrder("alice", testStation, testItem, 10, 10)
	if err == nil {
		t.Fatal("expected error for insufficient bank balance")
	}
}

func TestPlaceBuyOrder_InsufficientFlux(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("alice", 50)
	s := newTestService(mb)
	_, err := s.PlaceBuyOrder("alice", testStation, testItem, 10, 10) // needs 100
	if err == nil {
		t.Fatal("expected error for insufficient Flux")
	}
}

func TestPlaceSellOrder_MaxOrders(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 100)
	cfg := DefaultConfig()
	cfg.MaxOrders = 2
	s := newTestServiceWithConfig(mb, cfg)

	s.PlaceSellOrder("alice", testStation, testItem, 10, 1)
	s.PlaceSellOrder("alice", testStation, testItem, 11, 1)
	_, err := s.PlaceSellOrder("alice", testStation, testItem, 12, 1)
	if err == nil {
		t.Fatal("expected error for exceeding max orders")
	}
}

// ---------------------------------------------------------------------------
// Sell order matching
// ---------------------------------------------------------------------------

func TestSellOrder_NoMatch_Resting(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	res, err := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID == 0 {
		t.Fatal("expected resting order ID")
	}
	if res.FilledQty != 0 {
		t.Fatalf("expected 0 filled, got %d", res.FilledQty)
	}
	// Items escrowed from bank
	if mb.get("alice", testItem) != 5 {
		t.Fatalf("expected 5 items remaining in bank, got %d", mb.get("alice", testItem))
	}
}

func TestSellOrder_FullMatch(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 1000)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	// Bob places buy order
	s.PlaceBuyOrder("bob", testStation, testItem, 100, 5)

	// Alice sells into it
	res, err := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 5 {
		t.Fatalf("expected 5 filled, got %d", res.FilledQty)
	}
	if res.OrderID != 0 {
		t.Fatal("expected no resting order")
	}

	// Bob should have items
	if mb.get("bob", testItem) != 5 {
		t.Fatalf("bob items: expected 5, got %d", mb.get("bob", testItem))
	}
	// Alice gets Flux minus tax (2%)
	expectedFlux := int64(5 * 100 * 0.98)
	if mb.getFlux("alice") != expectedFlux {
		t.Fatalf("alice flux: expected %d, got %d", expectedFlux, mb.getFlux("alice"))
	}
}

func TestSellOrder_PartialMatch(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 300)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	s.PlaceBuyOrder("bob", testStation, testItem, 100, 3)

	res, err := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 3 {
		t.Fatalf("expected 3 filled, got %d", res.FilledQty)
	}
	if res.OrderID == 0 {
		t.Fatal("expected resting order for remainder")
	}
}

func TestSellOrder_MatchesHighestBuyFirst(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 500)
	mb.setFlux("carol", 500)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	s.PlaceBuyOrder("bob", testStation, testItem, 50, 1)
	s.PlaceBuyOrder("carol", testStation, testItem, 80, 1)

	// Alice sells 1 at price 50 — should match Carol's 80 first (higher price)
	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 50, 1)
	if res.FilledQty != 1 {
		t.Fatalf("expected 1 filled, got %d", res.FilledQty)
	}
	// Carol should have the item, not Bob
	if mb.get("carol", testItem) != 1 {
		t.Fatal("expected carol to receive the item (highest buy)")
	}
	if mb.get("bob", testItem) != 0 {
		t.Fatal("bob should not have received anything")
	}
}

func TestSellOrder_NoMatchWhenBuyPriceTooLow(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 100)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	s.PlaceBuyOrder("bob", testStation, testItem, 5, 1)

	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 10, 1)
	if res.FilledQty != 0 {
		t.Fatalf("expected 0 filled when buy price too low, got %d", res.FilledQty)
	}
	if res.OrderID == 0 {
		t.Fatal("expected resting sell order")
	}
}

// ---------------------------------------------------------------------------
// Buy order matching
// ---------------------------------------------------------------------------

func TestBuyOrder_NoMatch_Resting(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	res, err := s.PlaceBuyOrder("bob", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID == 0 {
		t.Fatal("expected resting order")
	}
	if res.FilledQty != 0 {
		t.Fatalf("expected 0 filled, got %d", res.FilledQty)
	}
	// Flux escrowed
	if mb.getFlux("bob") != 500 {
		t.Fatalf("expected 500 flux remaining, got %d", mb.getFlux("bob"))
	}
}

func TestBuyOrder_FullMatch(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 100, 5)

	res, err := s.PlaceBuyOrder("bob", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 5 {
		t.Fatalf("expected 5 filled, got %d", res.FilledQty)
	}
	if mb.get("bob", testItem) != 5 {
		t.Fatalf("bob items: expected 5, got %d", mb.get("bob", testItem))
	}
}

func TestBuyOrder_PartialMatch(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 3)
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 100, 3)

	res, err := s.PlaceBuyOrder("bob", testStation, testItem, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 3 {
		t.Fatalf("expected 3 filled, got %d", res.FilledQty)
	}
	if res.OrderID == 0 {
		t.Fatal("expected resting order for remainder")
	}
}

func TestBuyOrder_MatchesLowestSellFirst(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.set("carol", testItem, 10)
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 80, 1)
	s.PlaceSellOrder("carol", testStation, testItem, 50, 1)

	res, _ := s.PlaceBuyOrder("bob", testStation, testItem, 80, 1)
	if res.FilledQty != 1 {
		t.Fatalf("expected 1 filled, got %d", res.FilledQty)
	}
	// Carol's cheaper order should match first
	if mb.get("carol", testItem) != 9 {
		t.Fatal("expected carol's sell to be matched (lowest)")
	}
}

func TestBuyOrder_FIFOWithinSamePrice(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.set("carol", testItem, 10)
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	// Alice places first, carol second, same price
	s.PlaceSellOrder("alice", testStation, testItem, 50, 1)
	s.PlaceSellOrder("carol", testStation, testItem, 50, 1)

	s.PlaceBuyOrder("bob", testStation, testItem, 50, 1)

	// Alice's order is older, should match first
	if mb.get("alice", testItem) != 9 {
		t.Fatal("expected alice's older sell to match first (FIFO)")
	}
	if mb.get("carol", testItem) != 9 {
		t.Fatal("carol's order should still be resting")
	}
}

// ---------------------------------------------------------------------------
// Tax
// ---------------------------------------------------------------------------

func TestTax_SellerPays(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.setFlux("bob", 1000)
	s := newTestService(mb) // default 2% tax

	s.PlaceBuyOrder("bob", testStation, testItem, 100, 1)
	s.PlaceSellOrder("alice", testStation, testItem, 100, 1)

	// Seller gets 100 * 0.98 = 98
	if mb.getFlux("alice") != 98 {
		t.Fatalf("expected seller to get 98 flux, got %d", mb.getFlux("alice"))
	}
}

func TestTax_CustomRate(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.setFlux("bob", 1000)
	cfg := DefaultConfig()
	cfg.TaxPct = 0.05
	s := newTestServiceWithConfig(mb, cfg)

	s.PlaceBuyOrder("bob", testStation, testItem, 100, 1)
	s.PlaceSellOrder("alice", testStation, testItem, 100, 1)

	// Seller gets 100 * 0.95 = 95
	if mb.getFlux("alice") != 95 {
		t.Fatalf("expected seller to get 95 flux, got %d", mb.getFlux("alice"))
	}
}

// ---------------------------------------------------------------------------
// Instant trades
// ---------------------------------------------------------------------------

func TestInstantSell_MatchesBuyBook(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 500)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	s.PlaceBuyOrder("bob", testStation, testItem, 50, 5)

	res, err := s.InstantSell("alice", testStation, testItem, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 5 {
		t.Fatalf("expected 5 filled, got %d", res.FilledQty)
	}
	if mb.get("bob", testItem) != 5 {
		t.Fatalf("bob should have 5 items, got %d", mb.get("bob", testItem))
	}
}

func TestInstantSell_PartialFill(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 100)
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	s.PlaceBuyOrder("bob", testStation, testItem, 50, 2)

	res, _ := s.InstantSell("alice", testStation, testItem, 5)
	if res.FilledQty != 2 {
		t.Fatalf("expected 2 filled, got %d", res.FilledQty)
	}
	// Remaining items stay in bank
	if mb.get("alice", testItem) != 8 {
		t.Fatalf("expected 8 items remaining, got %d", mb.get("alice", testItem))
	}
}

func TestInstantSell_NoLiquidity(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	res, err := s.InstantSell("alice", testStation, testItem, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 0 {
		t.Fatalf("expected 0 filled, got %d", res.FilledQty)
	}
	if mb.get("alice", testItem) != 10 {
		t.Fatalf("items should be untouched, got %d", mb.get("alice", testItem))
	}
}

func TestInstantBuy_MatchesSellBook(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 50, 5)

	res, err := s.InstantBuy("bob", testStation, testItem, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 5 {
		t.Fatalf("expected 5 filled, got %d", res.FilledQty)
	}
	if mb.get("bob", testItem) != 5 {
		t.Fatalf("bob should have 5 items, got %d", mb.get("bob", testItem))
	}
}

func TestInstantBuy_LimitedByFlux(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	mb.setFlux("bob", 100) // can only afford 2 at price 50
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 50, 5)

	res, _ := s.InstantBuy("bob", testStation, testItem, 5)
	if res.FilledQty != 2 {
		t.Fatalf("expected 2 filled (limited by flux), got %d", res.FilledQty)
	}
}

func TestInstantBuy_NoLiquidity(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	res, err := s.InstantBuy("bob", testStation, testItem, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQty != 0 {
		t.Fatalf("expected 0 filled, got %d", res.FilledQty)
	}
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestCancel_SellRefundsItems(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)
	if mb.get("alice", testItem) != 5 {
		t.Fatalf("expected 5 after escrow, got %d", mb.get("alice", testItem))
	}

	err := s.CancelOrder("alice", res.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if mb.get("alice", testItem) != 10 {
		t.Fatalf("expected 10 after cancel refund, got %d", mb.get("alice", testItem))
	}
}

func TestCancel_BuyRefundsFlux(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("bob", 1000)
	s := newTestService(mb)

	res, _ := s.PlaceBuyOrder("bob", testStation, testItem, 100, 5) // escrows 500
	if mb.getFlux("bob") != 500 {
		t.Fatalf("expected 500 after escrow, got %d", mb.getFlux("bob"))
	}

	err := s.CancelOrder("bob", res.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if mb.getFlux("bob") != 1000 {
		t.Fatalf("expected 1000 after cancel refund, got %d", mb.getFlux("bob"))
	}
}

func TestCancel_NotFound(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)
	err := s.CancelOrder("alice", 9999)
	if err == nil {
		t.Fatal("expected error for non-existent order")
	}
}

func TestCancel_WrongPlayer(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)

	err := s.CancelOrder("bob", res.OrderID)
	if err == nil {
		t.Fatal("expected error when cancelling another player's order")
	}
}

func TestCancel_RemovedFromBook(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 10)
	s := newTestService(mb)

	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 100, 5)
	s.CancelOrder("alice", res.OrderID)

	view := s.Browse(testStation, testItem)
	if len(view.SellLevels) != 0 {
		t.Fatal("expected empty sell levels after cancel")
	}
	orders := s.PlayerOrders("alice")
	if len(orders) != 0 {
		t.Fatal("expected no player orders after cancel")
	}
}

// ---------------------------------------------------------------------------
// Expiry
// ---------------------------------------------------------------------------

func TestExpire_RemovesExpired(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)

	order := &Order{
		ID: 100, Side: SideSell, Player: "alice",
		StationID: testStation, ItemID: testItem, Price: 50,
		Quantity: 3, OrigQty: 3,
		CreatedAt: time.Now().Unix() - 1000,
		ExpiresAt: time.Now().Unix() - 1, // already expired
	}
	s.InsertLoadedOrder(order)

	s.ExpireOrders()

	orders := s.PlayerOrders("alice")
	if len(orders) != 0 {
		t.Fatal("expected expired order to be removed")
	}
}

func TestExpire_KeepsNonExpired(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)

	order := &Order{
		ID: 100, Side: SideSell, Player: "alice",
		StationID: testStation, ItemID: testItem, Price: 50,
		Quantity: 3, OrigQty: 3,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Unix() + 86400, // future
	}
	s.InsertLoadedOrder(order)

	s.ExpireOrders()

	orders := s.PlayerOrders("alice")
	if len(orders) != 1 {
		t.Fatal("expected non-expired order to survive")
	}
}

func TestExpire_SellRefundsItems(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)

	order := &Order{
		ID: 100, Side: SideSell, Player: "alice",
		StationID: testStation, ItemID: testItem, Price: 50,
		Quantity: 7, OrigQty: 7,
		CreatedAt: time.Now().Unix() - 1000,
		ExpiresAt: time.Now().Unix() - 1,
	}
	s.InsertLoadedOrder(order)
	s.ExpireOrders()

	if mb.get("alice", testItem) != 7 {
		t.Fatalf("expected 7 items refunded, got %d", mb.get("alice", testItem))
	}
}

func TestExpire_BuyRefundsFlux(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)

	order := &Order{
		ID: 100, Side: SideBuy, Player: "bob",
		StationID: testStation, ItemID: testItem, Price: 50,
		Quantity: 4, OrigQty: 4,
		CreatedAt: time.Now().Unix() - 1000,
		ExpiresAt: time.Now().Unix() - 1,
	}
	s.InsertLoadedOrder(order)
	s.ExpireOrders()

	// Refund = price * qty = 50 * 4 = 200
	if mb.getFlux("bob") != 200 {
		t.Fatalf("expected 200 flux refunded, got %d", mb.getFlux("bob"))
	}
}

// ---------------------------------------------------------------------------
// Browse & PlayerOrders
// ---------------------------------------------------------------------------

func TestBrowse_Empty(t *testing.T) {
	mb := newMockBank()
	s := newTestService(mb)

	view := s.Browse(testStation, testItem)
	if len(view.SellLevels) != 0 || len(view.BuyLevels) != 0 {
		t.Fatal("expected empty levels")
	}
}

func TestBrowse_Aggregates(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 20)
	mb.set("carol", testItem, 20)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 50, 3)
	s.PlaceSellOrder("carol", testStation, testItem, 50, 2)
	s.PlaceSellOrder("alice", testStation, testItem, 75, 5)

	view := s.Browse(testStation, testItem)
	if len(view.SellLevels) != 2 {
		t.Fatalf("expected 2 sell levels, got %d", len(view.SellLevels))
	}
	if view.SellLevels[0].Price != 50 || view.SellLevels[0].Quantity != 5 || view.SellLevels[0].Count != 2 {
		t.Fatalf("level 0: price=%d qty=%d count=%d",
			view.SellLevels[0].Price, view.SellLevels[0].Quantity, view.SellLevels[0].Count)
	}
}

func TestBrowse_PerStation(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 20)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", 1, testItem, 50, 3)

	view := s.Browse(2, testItem)
	if len(view.SellLevels) != 0 {
		t.Fatal("station 2 should have no orders")
	}
}

func TestPlayerOrders_OnlyOwn(t *testing.T) {
	mb := newMockBank()
	mb.set("alice", testItem, 20)
	mb.set("bob", testItem, 20)
	s := newTestService(mb)

	s.PlaceSellOrder("alice", testStation, testItem, 50, 3)
	s.PlaceSellOrder("bob", testStation, testItem, 60, 2)

	orders := s.PlayerOrders("alice")
	if len(orders) != 1 {
		t.Fatalf("expected 1 order for alice, got %d", len(orders))
	}
	if orders[0].Player != "alice" {
		t.Fatalf("expected alice's order, got %s", orders[0].Player)
	}
}

// ---------------------------------------------------------------------------
// Multi-match edge case
// ---------------------------------------------------------------------------

func TestSellOrder_MatchesMultipleBuys(t *testing.T) {
	mb := newMockBank()
	mb.setFlux("b1", 1000)
	mb.setFlux("b2", 1000)
	mb.setFlux("b3", 1000)
	mb.set("alice", testItem, 100)
	s := newTestService(mb)

	// Three buy orders at different prices
	s.PlaceBuyOrder("b1", testStation, testItem, 30, 2)
	s.PlaceBuyOrder("b2", testStation, testItem, 50, 3)
	s.PlaceBuyOrder("b3", testStation, testItem, 40, 4)

	// Alice sells 9 at price 30 — should match: b2@50(3), b3@40(4), b1@30(2)
	res, _ := s.PlaceSellOrder("alice", testStation, testItem, 30, 9)
	if res.FilledQty != 9 {
		t.Fatalf("expected 9 filled, got %d", res.FilledQty)
	}
	if res.OrderID != 0 {
		t.Fatal("expected no resting order (fully filled)")
	}

	if mb.get("b2", testItem) != 3 {
		t.Fatalf("b2 should have 3 items, got %d", mb.get("b2", testItem))
	}
	if mb.get("b3", testItem) != 4 {
		t.Fatalf("b3 should have 4 items, got %d", mb.get("b3", testItem))
	}
	if mb.get("b1", testItem) != 2 {
		t.Fatalf("b1 should have 2 items, got %d", mb.get("b1", testItem))
	}
}
