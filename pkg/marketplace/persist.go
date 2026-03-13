package marketplace

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"

	"github.com/zenion/mmoserver/pkg/persist"
)

const (
	ordersCollection = "market_orders"
	tradesCollection = "market_trades"
	metaCollection   = "market_meta"
	metaNextIDKey    = "next_id"
)

// persistOrder enqueues an order save via the async writer.
func (s *Service) persistOrder(o *Order) {
	if s.writer == nil {
		return
	}
	data, err := json.Marshal(o)
	if err != nil {
		return
	}
	s.writer.Enqueue(persist.Op{
		Collection: ordersCollection,
		Key:        strconv.FormatUint(o.ID, 10),
		Value:      data,
	})
}

// deletePersistOrder enqueues an order deletion via the async writer.
func (s *Service) deletePersistOrder(orderID uint64) {
	if s.writer == nil {
		return
	}
	s.writer.Enqueue(persist.Op{
		Collection: ordersCollection,
		Key:        strconv.FormatUint(orderID, 10),
		Value:      nil, // nil = delete
	})
}

// persistTrade enqueues a trade save via the async writer.
func (s *Service) persistTrade(t *Trade) {
	if s.writer == nil {
		return
	}
	data, err := json.Marshal(t)
	if err != nil {
		return
	}
	s.writer.Enqueue(persist.Op{
		Collection: tradesCollection,
		Key:        strconv.FormatUint(t.ID, 10),
		Value:      data,
	})
}

// persistNextID saves the next ID counter for recovery after restart.
func (s *Service) persistNextID() {
	if s.writer == nil {
		return
	}
	s.writer.Enqueue(persist.Op{
		Collection: metaCollection,
		Key:        metaNextIDKey,
		Value:      []byte(strconv.FormatUint(s.nextID, 10)),
	})
}

// LoadAll reads all persisted orders from the store and rebuilds the in-memory order books.
// Call during startup before processing any requests.
func (s *Service) LoadAll(store persist.Store) error {
	// Load next ID
	if data, err := store.Get(metaCollection, metaNextIDKey); err == nil {
		if id, err := strconv.ParseUint(string(data), 10, 64); err == nil {
			s.nextID = id
		}
	}

	count := 0
	err := store.ForEach(ordersCollection, func(key string, value []byte) error {
		var o Order
		if err := json.Unmarshal(value, &o); err != nil {
			return fmt.Errorf("unmarshal order %s: %w", key, err)
		}
		s.InsertLoadedOrder(&o)
		if o.ID >= s.nextID {
			s.nextID = o.ID + 1
		}
		count++
		return nil
	})
	if err != nil {
		return fmt.Errorf("load market orders: %w", err)
	}
	if count > 0 {
		log.Printf("market: loaded %d orders", count)
	}

	// Persist the nextID so it's always up to date
	s.persistNextID()

	return nil
}
