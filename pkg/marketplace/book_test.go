package marketplace

import (
	"testing"
)

func TestInsertSell_SortOrder(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 30, CreatedAt: 1})
	ob.InsertSell(&Order{ID: 2, Price: 10, CreatedAt: 2})
	ob.InsertSell(&Order{ID: 3, Price: 20, CreatedAt: 3})

	if len(ob.Sells) != 3 {
		t.Fatalf("expected 3 sells, got %d", len(ob.Sells))
	}
	if ob.Sells[0].Price != 10 || ob.Sells[1].Price != 20 || ob.Sells[2].Price != 30 {
		t.Fatalf("sells not sorted ascending: %.0f, %.0f, %.0f",
			ob.Sells[0].Price, ob.Sells[1].Price, ob.Sells[2].Price)
	}
}

func TestInsertBuy_SortOrder(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertBuy(&Order{ID: 1, Price: 10, CreatedAt: 1})
	ob.InsertBuy(&Order{ID: 2, Price: 30, CreatedAt: 2})
	ob.InsertBuy(&Order{ID: 3, Price: 20, CreatedAt: 3})

	if len(ob.Buys) != 3 {
		t.Fatalf("expected 3 buys, got %d", len(ob.Buys))
	}
	if ob.Buys[0].Price != 30 || ob.Buys[1].Price != 20 || ob.Buys[2].Price != 10 {
		t.Fatalf("buys not sorted descending: %.0f, %.0f, %.0f",
			ob.Buys[0].Price, ob.Buys[1].Price, ob.Buys[2].Price)
	}
}

func TestInsertSell_FIFO(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 10, CreatedAt: 100})
	ob.InsertSell(&Order{ID: 2, Price: 10, CreatedAt: 50}) // older
	ob.InsertSell(&Order{ID: 3, Price: 10, CreatedAt: 200})

	if ob.Sells[0].ID != 2 || ob.Sells[1].ID != 1 || ob.Sells[2].ID != 3 {
		t.Fatalf("same-price sells not FIFO: IDs %d, %d, %d",
			ob.Sells[0].ID, ob.Sells[1].ID, ob.Sells[2].ID)
	}
}

func TestInsertBuy_FIFO(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertBuy(&Order{ID: 1, Price: 10, CreatedAt: 100})
	ob.InsertBuy(&Order{ID: 2, Price: 10, CreatedAt: 50}) // older
	ob.InsertBuy(&Order{ID: 3, Price: 10, CreatedAt: 200})

	if ob.Buys[0].ID != 2 || ob.Buys[1].ID != 1 || ob.Buys[2].ID != 3 {
		t.Fatalf("same-price buys not FIFO: IDs %d, %d, %d",
			ob.Buys[0].ID, ob.Buys[1].ID, ob.Buys[2].ID)
	}
}

func TestRemoveOrder_Sell(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 10})
	ob.InsertSell(&Order{ID: 2, Price: 20})
	ob.InsertSell(&Order{ID: 3, Price: 30})

	removed := ob.RemoveOrder(2)
	if removed == nil || removed.ID != 2 {
		t.Fatal("expected to remove order 2")
	}
	if len(ob.Sells) != 2 {
		t.Fatalf("expected 2 remaining sells, got %d", len(ob.Sells))
	}
	if ob.Sells[0].ID != 1 || ob.Sells[1].ID != 3 {
		t.Fatal("wrong remaining orders")
	}
}

func TestRemoveOrder_Buy(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertBuy(&Order{ID: 1, Price: 30})
	ob.InsertBuy(&Order{ID: 2, Price: 20})
	ob.InsertBuy(&Order{ID: 3, Price: 10})

	removed := ob.RemoveOrder(2)
	if removed == nil || removed.ID != 2 {
		t.Fatal("expected to remove order 2")
	}
	if len(ob.Buys) != 2 {
		t.Fatalf("expected 2 remaining buys, got %d", len(ob.Buys))
	}
}

func TestRemoveOrder_NotFound(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 10})

	removed := ob.RemoveOrder(999)
	if removed != nil {
		t.Fatal("expected nil for non-existent order")
	}
}

func TestAggregateSells(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 10, Quantity: 5})
	ob.InsertSell(&Order{ID: 2, Price: 10, Quantity: 3})
	ob.InsertSell(&Order{ID: 3, Price: 20, Quantity: 7})

	levels := ob.AggregateSells(10)
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d", len(levels))
	}
	if levels[0].Price != 10 || levels[0].Quantity != 8 || levels[0].Count != 2 {
		t.Fatalf("level 0: price=%.0f qty=%.0f count=%d", levels[0].Price, levels[0].Quantity, levels[0].Count)
	}
	if levels[1].Price != 20 || levels[1].Quantity != 7 || levels[1].Count != 1 {
		t.Fatalf("level 1: price=%.0f qty=%.0f count=%d", levels[1].Price, levels[1].Quantity, levels[1].Count)
	}
}

func TestAggregateBuys(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertBuy(&Order{ID: 1, Price: 20, Quantity: 4})
	ob.InsertBuy(&Order{ID: 2, Price: 20, Quantity: 6})
	ob.InsertBuy(&Order{ID: 3, Price: 10, Quantity: 2})

	levels := ob.AggregateBuys(10)
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d", len(levels))
	}
	if levels[0].Price != 20 || levels[0].Quantity != 10 || levels[0].Count != 2 {
		t.Fatalf("level 0: price=%.0f qty=%.0f count=%d", levels[0].Price, levels[0].Quantity, levels[0].Count)
	}
	if levels[1].Price != 10 || levels[1].Quantity != 2 || levels[1].Count != 1 {
		t.Fatalf("level 1: price=%.0f qty=%.0f count=%d", levels[1].Price, levels[1].Quantity, levels[1].Count)
	}
}

func TestAggregate_MaxLevels(t *testing.T) {
	ob := &OrderBook{}
	ob.InsertSell(&Order{ID: 1, Price: 10, Quantity: 1})
	ob.InsertSell(&Order{ID: 2, Price: 20, Quantity: 1})
	ob.InsertSell(&Order{ID: 3, Price: 30, Quantity: 1})

	levels := ob.AggregateSells(2)
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels (capped), got %d", len(levels))
	}
	if levels[1].Price != 20 {
		t.Fatalf("expected second level price=20, got %.0f", levels[1].Price)
	}
}

func TestAggregate_Empty(t *testing.T) {
	ob := &OrderBook{}
	levels := ob.AggregateSells(10)
	if levels != nil {
		t.Fatalf("expected nil for empty book, got %v", levels)
	}
}
