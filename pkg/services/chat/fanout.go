package chat

import (
	"github.com/google/uuid"
)

// fanoutEvent dispatches a typed server event to every connection
// subscribed to channelID. SYSTEM_ALL walks the online[] map directly;
// other channels walk subs[channelID].
//
// The actual send is performed by sendEventFn — wired by the mmokit
// facade to the gateway's per-conn typed-event sender. In unit tests
// it can be replaced with a recorder.
func (s *Service) fanoutEvent(channelID uuid.UUID, event any) {
	s.mu.RLock()
	c, ok := s.channels[channelID]
	if !ok {
		s.mu.RUnlock()
		return
	}
	var conns []uint32
	if c.Kind == "system_all" {
		conns = make([]uint32, 0, len(s.online))
		for _, cid := range s.online {
			conns = append(conns, cid)
		}
	} else {
		subs := s.subs[channelID]
		conns = make([]uint32, 0, len(subs))
		for cid := range subs {
			conns = append(conns, cid)
		}
	}
	send := s.sendEventFn
	s.mu.RUnlock()
	if send == nil {
		return // unit-test path — no gateway wired
	}
	for _, cid := range conns {
		send(cid, event)
	}
}

// fanoutToOne sends an event to a single connection.
func (s *Service) fanoutToOne(connID uint32, event any) {
	s.mu.RLock()
	send := s.sendEventFn
	s.mu.RUnlock()
	if send == nil {
		return
	}
	send(connID, event)
}
