package net

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log"
	"net"
	"sync"

	"github.com/zenion/gameserver/pkg/net/udpproto"
)

const (
	maxUDPPacket = 1400 // safe MTU
)

// UDPServer manages a single UDP socket and dispatches packets to UDPTransports.
type UDPServer struct {
	conn      *net.UDPConn
	connMgr   *ConnManager
	mu        sync.RWMutex
	byToken   map[uint32]*UDPTransport
	byAddr    map[string]*UDPTransport // addr string → transport (for handshake routing)
	connIDs   map[*UDPTransport]uint32 // transport → connMgr connID
}

// NewUDPServer creates a new UDP server bound to the given address.
func NewUDPServer(addr string, connMgr *ConnManager) (*UDPServer, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}

	s := &UDPServer{
		conn:    conn,
		connMgr: connMgr,
		byToken: make(map[uint32]*UDPTransport),
		byAddr:  make(map[string]*UDPTransport),
		connIDs: make(map[*UDPTransport]uint32),
	}
	return s, nil
}

// Run starts the read loop. Blocks until context is cancelled.
func (s *UDPServer) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		s.conn.Close()
	}()

	buf := make([]byte, maxUDPPacket)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("udp: read error: %v", err)
				continue
			}
		}
		if n == 0 {
			continue
		}

		// Copy packet data so buf can be reused
		data := make([]byte, n)
		copy(data, buf[:n])

		s.dispatch(data, addr)
	}
}

func (s *UDPServer) dispatch(data []byte, addr *net.UDPAddr) {
	pktType := data[0]

	switch pktType {
	case udpproto.TypeConnReq:
		s.handleConnReq(data, addr)

	case udpproto.TypeUnreliable:
		token, payload, err := udpproto.DecodeUnreliable(data)
		if err != nil {
			return
		}
		s.mu.RLock()
		t := s.byToken[token]
		s.mu.RUnlock()
		if t != nil {
			t.handleUnreliable(payload)
		}

	case udpproto.TypeReliable:
		token, seq, payload, err := udpproto.DecodeReliable(data)
		if err != nil {
			return
		}
		s.mu.RLock()
		t := s.byToken[token]
		s.mu.RUnlock()
		if t != nil {
			t.handleReliable(seq, payload)
		}

	case udpproto.TypeACK:
		token, ackSeq, ackBits, err := udpproto.DecodeACK(data)
		if err != nil {
			return
		}
		s.mu.RLock()
		t := s.byToken[token]
		s.mu.RUnlock()
		if t != nil {
			t.handleACK(ackSeq, ackBits)
		}

	case udpproto.TypeDisconnect:
		token, err := udpproto.DecodeDisconnect(data)
		if err != nil {
			return
		}
		s.mu.RLock()
		t := s.byToken[token]
		s.mu.RUnlock()
		if t != nil {
			s.removeTransport(t)
		}
	}
}

func (s *UDPServer) handleConnReq(data []byte, addr *net.UDPAddr) {
	clientSalt, err := udpproto.DecodeConnReq(data)
	if err != nil {
		return
	}

	addrKey := addr.String()

	// Check if this addr already has a pending/active connection
	s.mu.RLock()
	existing := s.byAddr[addrKey]
	s.mu.RUnlock()
	if existing != nil {
		// Resend accept (client may not have received it)
		resp := udpproto.EncodeConnAccept(existing.clientSalt, existing.serverSalt)
		s.conn.WriteToUDP(resp, addr)
		return
	}

	// Generate server salt
	var saltBuf [8]byte
	if _, err := rand.Read(saltBuf[:]); err != nil {
		log.Printf("udp: failed to generate salt: %v", err)
		return
	}
	serverSalt := binary.LittleEndian.Uint64(saltBuf[:])
	token := udpproto.MakeToken(clientSalt, serverSalt)

	// Create transport and register with ConnManager
	t := newUDPTransport(s, addr, token, clientSalt, serverSalt)
	connID := s.connMgr.AddTransport(t)

	s.mu.Lock()
	s.byToken[token] = t
	s.byAddr[addrKey] = t
	s.connIDs[t] = connID
	s.mu.Unlock()

	log.Printf("udp: new connection from %s (token=%08x, connID=%d)", addrKey, token, connID)

	// Send accept
	resp := udpproto.EncodeConnAccept(clientSalt, serverSalt)
	s.conn.WriteToUDP(resp, addr)
}


func (s *UDPServer) removeTransport(t *UDPTransport) {
	s.mu.Lock()
	connID, ok := s.connIDs[t]
	delete(s.byToken, t.token)
	delete(s.byAddr, t.addr.String())
	delete(s.connIDs, t)
	s.mu.Unlock()

	t.Close()

	if ok {
		// Fire disconnect event (remove from ConnManager)
		s.connMgr.mu.Lock()
		delete(s.connMgr.conns, connID)
		s.connMgr.mu.Unlock()
		s.connMgr.events <- PlayerEvent{ConnID: connID, Disconnect: true}
	}
}
