package marketplace

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/zenion/mmoserver/pkg/orderbook"
	"github.com/zenion/mmoserver/pkg/persist"
)

const (
	ordersCollection = "market_orders"
	tradesCollection = "market_trades"
	metaCollection   = "market_meta"
	metaNextIDKey    = "next_id"
)

// persistOrder enqueues an order save via the async writer.
func (st *Settlement) persistOrder(o *orderbook.Order) {
	if st.writer == nil {
		return
	}
	data, err := json.Marshal(o)
	if err != nil {
		return
	}
	st.writer.Enqueue(persist.Op{
		Collection: ordersCollection,
		Key:        strconv.FormatUint(o.ID, 10),
		Value:      data,
	})
}

// deletePersistOrder enqueues an order deletion via the async writer.
func (st *Settlement) deletePersistOrder(orderID uint64) {
	if st.writer == nil {
		return
	}
	st.writer.Enqueue(persist.Op{
		Collection: ordersCollection,
		Key:        strconv.FormatUint(orderID, 10),
		Value:      nil, // nil = delete
	})
}

// persistTrade enqueues a trade save via the async writer.
func (st *Settlement) persistTrade(t *orderbook.Trade) {
	if st.writer == nil {
		return
	}
	data, err := json.Marshal(t)
	if err != nil {
		return
	}
	st.writer.Enqueue(persist.Op{
		Collection: tradesCollection,
		Key:        strconv.FormatUint(t.ID, 10),
		Value:      data,
	})
}

// persistNextID saves the next ID counter for recovery after restart.
func (st *Settlement) persistNextID() {
	if st.writer == nil {
		return
	}
	nextID := st.ob.NextID()
	st.writer.Enqueue(persist.Op{
		Collection: metaCollection,
		Key:        metaNextIDKey,
		Value:      []byte(strconv.FormatUint(nextID, 10)),
	})
}

// LoadAll reads all persisted orders from the store and rebuilds the in-memory order books.
// Call during startup before processing any requests.
func (st *Settlement) LoadAll(store persist.Store) error {
	// Load next ID
	if data, err := store.Get(metaCollection, metaNextIDKey); err == nil {
		if id, err := strconv.ParseUint(string(data), 10, 64); err == nil {
			st.ob.SetNextID(id)
		}
	}

	count := 0
	var maxID uint64
	err := store.ForEach(ordersCollection, func(key string, value []byte) error {
		var o orderbook.Order
		if err := json.Unmarshal(value, &o); err != nil {
			return fmt.Errorf("unmarshal order %s: %w", key, err)
		}
		st.InsertLoadedOrder(&o)
		if o.ID >= maxID {
			maxID = o.ID + 1
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("load market orders: %w", err)
	}

	// Ensure nextID is at least past the highest loaded order
	if currentNext := st.ob.NextID(); maxID > currentNext {
		st.ob.SetNextID(maxID)
	}

	if count > 0 {
		log.Printf("market: loaded %d orders", count)
	}

	// Persist the nextID so it's always up to date
	st.persistNextID()

	return nil
}
