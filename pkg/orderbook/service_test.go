package orderbook

import (
	"testing"
	"time"
)

func newTestService() *Service {
	return NewService(DefaultConfig())
}

// ---------------------------------------------------------------------------
// PlaceSellOrder
// ---------------------------------------------------------------------------

func TestService_PlaceSellOrder_NoMatch(t *testing.T) {
	s := newTestService()
	matches, resting, err := s.PlaceSellOrder("alice", 1, 10, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
	if resting == nil {
		t.Fatal("expected resting order")
	}
	if resting.Quantity != 5 {
		t.Fatalf("expected resting qty 5, got %d", resting.Quantity)
	}
}

func TestService_PlaceSellOrder_FullMatch(t *testing.T) {
	s := newTestService()
	s.PlaceBuyOrder("bob", 1, 10, 100, 5)

	matches, resting, err := s.PlaceSellOrder("alice", 1, 10, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Quantity != 5 {
		t.Fatalf("expected match qty 5, got %d", matches[0].Quantity)
	}
	if matches[0].Price != 100 {
		t.Fatalf("expected match price 100, got %d", matches[0].Price)
	}
	if matches[0].BuyerID != "bob" {
		t.Fatalf("expected buyer bob, got %s", matches[0].BuyerID)
	}
	if matches[0].SellerID != "alice" {
		t.Fatalf("expected seller alice, got %s", matches[0].SellerID)
	}
	if resting != nil {
		t.Fatal("expected no resting order")
	}
}

func TestService_PlaceSellOrder_PartialMatch(t *testing.T) {
	s := newTestService()
	s.PlaceBuyOrder("bob", 1, 10, 100, 3)

	matches, resting, err := s.PlaceSellOrder("alice", 1, 10, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Quantity != 3 {
		t.Fatalf("expected match qty 3, got %d", matches[0].Quantity)
	}
	if resting == nil || resting.Quantity != 2 {
		t.Fatal("expected resting order with qty 2")
	}
}

func TestService_PlaceSellOrder_PriceBelowMin(t *testing.T) {
	s := newTestService()
	_, _, err := s.PlaceSellOrder("alice", 1, 10, 0, 5)
	if err == nil {
		t.Fatal("expected error for price below minimum")
	}
}

func TestService_PlaceSellOrder_ZeroQty(t *testing.T) {
	s := newTestService()
	_, _, err := s.PlaceSellOrder("alice", 1, 10, 10, 0)
	if err == nil {
		t.Fatal("expected error for zero qty")
	}
}

func TestService_PlaceSellOrder_MatchesHighestBuyFirst(t *testing.T) {
	s := newTestService()
	s.PlaceBuyOrder("bob", 1, 10, 50, 1)
	s.PlaceBuyOrder("carol", 1, 10, 80, 1)

	matches, _, _ := s.PlaceSellOrder("alice", 1, 10, 50, 1)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].BuyerID != "carol" {
		t.Fatalf("expected carol (highest buy), got %s", matches[0].BuyerID)
	}
}

func TestService_PlaceSellOrder_MultipleMatches(t *testing.T) {
	s := newTestService()
	s.PlaceBuyOrder("b1", 1, 10, 50, 3)
	s.PlaceBuyOrder("b2", 1, 10, 40, 4)

	matches, resting, _ := s.PlaceSellOrder("alice", 1, 10, 40, 7)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	totalQty := matches[0].Quantity + matches[1].Quantity
	if totalQty != 7 {
		t.Fatalf("expected total qty 7, got %d", totalQty)
	}
	if resting != nil {
		t.Fatal("expected no resting order")
	}
}

// ---------------------------------------------------------------------------
// PlaceBuyOrder
// ---------------------------------------------------------------------------

func TestService_PlaceBuyOrder_NoMatch(t *testing.T) {
	s := newTestService()
	matches, resting, err := s.PlaceBuyOrder("bob", 1, 10, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
	if resting == nil || resting.Quantity != 5 {
		t.Fatal("expected resting order with qty 5")
	}
}

func TestService_PlaceBuyOrder_FullMatch(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 100, 5)

	matches, resting, err := s.PlaceBuyOrder("bob", 1, 10, 100, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Quantity != 5 {
		t.Fatalf("expected 1 match with qty 5")
	}
	if resting != nil {
		t.Fatal("expected no resting order")
	}
}

func TestService_PlaceBuyOrder_MatchesLowestSellFirst(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 80, 1)
	s.PlaceSellOrder("carol", 1, 10, 50, 1)

	matches, _, _ := s.PlaceBuyOrder("bob", 1, 10, 80, 1)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].SellerID != "carol" {
		t.Fatalf("expected carol (lowest sell), got %s", matches[0].SellerID)
	}
	if matches[0].Price != 50 {
		t.Fatalf("expected price 50, got %d", matches[0].Price)
	}
}

// ---------------------------------------------------------------------------
// InstantSell
// ---------------------------------------------------------------------------

func TestService_InstantSell_Matches(t *testing.T) {
	s := newTestService()
	s.PlaceBuyOrder("bob", 1, 10, 50, 5)

	matches, err := s.InstantSell("alice", 1, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Quantity != 5 {
		t.Fatal("expected 1 match with qty 5")
	}
}

func TestService_InstantSell_NoLiquidity(t *testing.T) {
	s := newTestService()
	matches, err := s.InstantSell("alice", 1, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

// ---------------------------------------------------------------------------
// InstantBuy
// ---------------------------------------------------------------------------

func TestService_InstantBuy_Matches(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 50, 5)

	matches, err := s.InstantBuy("bob", 1, 10, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Quantity != 5 {
		t.Fatal("expected 1 match with qty 5")
	}
}

func TestService_InstantBuy_WithBudget(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 50, 5)

	// Budget of 100 can only buy 2 at price 50
	matches, err := s.InstantBuy("bob", 1, 10, 5, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Quantity != 2 {
		t.Fatalf("expected qty 2 (limited by budget), got %d", matches[0].Quantity)
	}
}

func TestService_InstantBuy_NoLiquidity(t *testing.T) {
	s := newTestService()
	matches, err := s.InstantBuy("bob", 1, 10, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
}

// ---------------------------------------------------------------------------
// CancelOrder
// ---------------------------------------------------------------------------

func TestService_CancelOrder(t *testing.T) {
	s := newTestService()
	_, resting, _ := s.PlaceSellOrder("alice", 1, 10, 100, 5)

	order, err := s.CancelOrder("alice", resting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != resting.ID {
		t.Fatalf("expected order %d, got %d", resting.ID, order.ID)
	}
	if order.Quantity != 5 {
		t.Fatalf("expected qty 5, got %d", order.Quantity)
	}

	// Verify removed from book
	view := s.Browse(1, 10)
	if len(view.SellLevels) != 0 {
		t.Fatal("expected empty sell levels after cancel")
	}
}

func TestService_CancelOrder_NotFound(t *testing.T) {
	s := newTestService()
	_, err := s.CancelOrder("alice", 9999)
	if err == nil {
		t.Fatal("expected error for non-existent order")
	}
}

func TestService_CancelOrder_WrongPlayer(t *testing.T) {
	s := newTestService()
	_, resting, _ := s.PlaceSellOrder("alice", 1, 10, 100, 5)

	_, err := s.CancelOrder("bob", resting.ID)
	if err == nil {
		t.Fatal("expected error when cancelling another player's order")
	}
}

// ---------------------------------------------------------------------------
// ExpireOrders
// ---------------------------------------------------------------------------

func TestService_ExpireOrders(t *testing.T) {
	s := newTestService()
	order := &Order{
		ID: 100, Side: SideSell, Owner: "alice",
		LocationID: 1, ItemID: 10, Price: 50,
		Quantity: 3, OrigQty: 3,
		CreatedAt: time.Now().Unix() - 1000,
		ExpiresAt: time.Now().Unix() - 1,
	}
	s.InsertLoadedOrder(order)

	expired := s.ExpireOrders()
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired, got %d", len(expired))
	}
	if expired[0].ID != 100 {
		t.Fatalf("expected order 100, got %d", expired[0].ID)
	}

	orders := s.PlayerOrders("alice")
	if len(orders) != 0 {
		t.Fatal("expected order removed after expiry")
	}
}

func TestService_ExpireOrders_KeepsNonExpired(t *testing.T) {
	s := newTestService()
	order := &Order{
		ID: 100, Side: SideSell, Owner: "alice",
		LocationID: 1, ItemID: 10, Price: 50,
		Quantity: 3, OrigQty: 3,
		CreatedAt: time.Now().Unix(),
		ExpiresAt: time.Now().Unix() + 86400,
	}
	s.InsertLoadedOrder(order)

	expired := s.ExpireOrders()
	if len(expired) != 0 {
		t.Fatalf("expected 0 expired, got %d", len(expired))
	}

	orders := s.PlayerOrders("alice")
	if len(orders) != 1 {
		t.Fatal("expected non-expired order to survive")
	}
}

// ---------------------------------------------------------------------------
// Browse & PlayerOrders
// ---------------------------------------------------------------------------

func TestService_Browse(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 50, 3)
	s.PlaceSellOrder("carol", 1, 10, 50, 2)

	view := s.Browse(1, 10)
	if len(view.SellLevels) != 1 {
		t.Fatalf("expected 1 sell level, got %d", len(view.SellLevels))
	}
	if view.SellLevels[0].Quantity != 5 {
		t.Fatalf("expected aggregated qty 5, got %d", view.SellLevels[0].Quantity)
	}
}

func TestService_PlayerOrders(t *testing.T) {
	s := newTestService()
	s.PlaceSellOrder("alice", 1, 10, 50, 3)
	s.PlaceSellOrder("bob", 1, 10, 60, 2)

	orders := s.PlayerOrders("alice")
	if len(orders) != 1 {
		t.Fatalf("expected 1 order for alice, got %d", len(orders))
	}
}

// ---------------------------------------------------------------------------
// MaxOrders
// ---------------------------------------------------------------------------

func TestService_MaxOrders(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxOrders = 2
	s := NewService(cfg)

	s.PlaceSellOrder("alice", 1, 10, 50, 1)
	s.PlaceSellOrder("alice", 1, 10, 60, 1)
	_, _, err := s.PlaceSellOrder("alice", 1, 10, 70, 1)
	if err == nil {
		t.Fatal("expected error for exceeding max orders")
	}
}
