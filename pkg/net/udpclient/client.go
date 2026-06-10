// Package udpclient provides a self-contained Go UDP client for the game server.
package udpclient

import (
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"crypto/rand"
	"encoding/binary"

	"github.com/zenion/mmoserver/pkg/net/udpproto"
)

const (
	maxPacket          = 1400
	handshakeTimeout   = 5 * time.Second
	reliableBufSize    = 256
	retransmitInterval = 100 * time.Millisecond
	reliableTimeout    = 5 * time.Second
	keepaliveInterval  = 1 * time.Second
	connectionTimeout  = 10 * time.Second
)

type reliableEntry struct {
	payload []byte
	sentAt  time.Time
	acked   bool
	used    bool
}

// Client is a UDP game client that connects to the server.
type Client struct {
	conn  *net.UDPConn
	token uint32

	// Outbound reliability
	sendMu  sync.Mutex
	sendSeq uint16
	sendBuf [reliableBufSize]reliableEntry

	// Inbound reliability tracking
	recvSeq  uint16
	recvBits uint32
	ackDirty bool

	// Inbound message queue
	inMu    sync.Mutex
	inCond  *sync.Cond
	inbound [][]byte

	lastRecv time.Time
	lastSend time.Time
	closed   bool
	closeMu  sync.Mutex
	done     chan struct{}
}

// Dial connects to the game server at addr (e.g. "localhost:9000").
func Dial(addr string) (*Client, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return nil, err
	}

	// Generate client salt
	var saltBuf [8]byte
	if _, err := rand.Read(saltBuf[:]); err != nil {
		conn.Close()
		return nil, err
	}
	clientSalt := binary.LittleEndian.Uint64(saltBuf[:])

	// Send connection request
	req := udpproto.EncodeConnReq(clientSalt)
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	// Wait for the ConnAccept (0x04). On connect the server also pushes an
	// unsolicited reliable ServerConfig frame (type 0x01) that races the
	// ConnAccept; the two are separate datagrams with no ordering guarantee.
	// Discard any non-ConnAccept datagram and keep reading until the deadline.
	// ServerConfig is sent reliably (server retransmits every 100ms), so the
	// discarded copy is redelivered once the receive loop is running — and we
	// can't process it pre-handshake anyway (no serverSalt → no token yet).
	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	buf := make([]byte, maxPacket)
	var serverSalt uint64
	for {
		n, err := conn.Read(buf)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("handshake failed: %w", err)
		}
		if n == 0 || buf[0] != udpproto.TypeConnAccept {
			continue // early ServerConfig / stray packet — keep waiting for accept
		}
		respClientSalt, srvSalt, derr := udpproto.DecodeConnAccept(buf[:n])
		if derr != nil || respClientSalt != clientSalt {
			continue // malformed / stale accept — keep waiting
		}
		serverSalt = srvSalt
		break
	}
	conn.SetReadDeadline(time.Time{})

	token := udpproto.MakeToken(clientSalt, serverSalt)

	c := &Client{
		conn:     conn,
		token:    token,
		inbound:  make([][]byte, 0, 32),
		lastRecv: time.Now(),
		lastSend: time.Now(),
		done:     make(chan struct{}),
	}
	c.inCond = sync.NewCond(&c.inMu)

	go c.readLoop()
	go c.tickLoop()

	return c, nil
}

// SendReliable sends a message with reliability guarantees.
func (c *Client) SendReliable(data []byte) error {
	c.sendMu.Lock()
	seq := c.sendSeq
	c.sendSeq++
	idx := seq % reliableBufSize
	payload := make([]byte, len(data))
	copy(payload, data)
	c.sendBuf[idx] = reliableEntry{
		payload: payload,
		sentAt:  time.Now(),
		acked:   false,
		used:    true,
	}
	c.sendMu.Unlock()

	pkt := udpproto.EncodeReliable(c.token, seq, payload)
	_, err := c.conn.Write(pkt)
	c.lastSend = time.Now()
	return err
}

// SendUnreliable sends a message with no delivery guarantee.
func (c *Client) SendUnreliable(data []byte) error {
	pkt := udpproto.EncodeUnreliable(c.token, data)
	_, err := c.conn.Write(pkt)
	c.lastSend = time.Now()
	return err
}

// Recv blocks until a message is available and returns it.
func (c *Client) Recv() ([]byte, error) {
	c.inMu.Lock()
	for len(c.inbound) == 0 {
		c.closeMu.Lock()
		if c.closed {
			c.closeMu.Unlock()
			c.inMu.Unlock()
			return nil, errors.New("connection closed")
		}
		c.closeMu.Unlock()
		c.inCond.Wait()
	}
	msg := c.inbound[0]
	c.inbound = c.inbound[1:]
	c.inMu.Unlock()
	return msg, nil
}

// Close shuts down the client.
func (c *Client) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	c.closeMu.Unlock()

	// Send disconnect (best effort)
	pkt := udpproto.EncodeDisconnect(c.token)
	c.conn.Write(pkt)

	c.inCond.Broadcast()
	return c.conn.Close()
}

func (c *Client) readLoop() {
	buf := make([]byte, maxPacket)
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			select {
			case <-c.done:
				return
			default:
				return
			}
		}
		if n == 0 {
			continue
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		c.handlePacket(data)
	}
}

func (c *Client) handlePacket(data []byte) {
	switch data[0] {
	case udpproto.TypeUnreliable:
		_, payload, err := udpproto.DecodeUnreliable(data)
		if err != nil {
			return
		}
		c.lastRecv = time.Now()
		if len(payload) == 0 {
			return // keepalive
		}
		c.inMu.Lock()
		c.inbound = append(c.inbound, payload)
		c.inCond.Signal()
		c.inMu.Unlock()

	case udpproto.TypeReliable:
		_, seq, payload, err := udpproto.DecodeReliable(data)
		if err != nil {
			return
		}
		c.lastRecv = time.Now()
		c.updateRecvTracking(seq)
		if len(payload) > 0 {
			c.inMu.Lock()
			c.inbound = append(c.inbound, payload)
			c.inCond.Signal()
			c.inMu.Unlock()
		}

	case udpproto.TypeACK:
		_, ackSeq, ackBits, err := udpproto.DecodeACK(data)
		if err != nil {
			return
		}
		c.lastRecv = time.Now()
		c.processACK(ackSeq, ackBits)
	}
}

func (c *Client) updateRecvTracking(seq uint16) {
	if c.recvSeq == 0 && !c.ackDirty {
		c.recvSeq = seq
		c.recvBits = 0
	} else if udpproto.SeqGreaterThan(seq, c.recvSeq) {
		diff := seq - c.recvSeq
		if diff <= 32 {
			c.recvBits = (c.recvBits << diff) | (1 << (diff - 1))
		} else {
			c.recvBits = 0
		}
		c.recvSeq = seq
	} else {
		diff := c.recvSeq - seq
		if diff > 0 && diff <= 32 {
			c.recvBits |= 1 << (diff - 1)
		}
	}
	c.ackDirty = true
}

func (c *Client) processACK(ackSeq uint16, ackBits uint32) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	idx := ackSeq % reliableBufSize
	if c.sendBuf[idx].used {
		c.sendBuf[idx].acked = true
	}
	for i := uint32(0); i < 32; i++ {
		if ackBits&(1<<i) != 0 {
			s := ackSeq - uint16(i) - 1
			idx := s % reliableBufSize
			if c.sendBuf[idx].used {
				c.sendBuf[idx].acked = true
			}
		}
	}
}

func (c *Client) tickLoop() {
	ticker := time.NewTicker(retransmitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.done:
			return
		case now := <-ticker.C:
			// Connection timeout
			if now.Sub(c.lastRecv) > connectionTimeout {
				log.Printf("udp client: connection timeout")
				c.Close()
				return
			}

			// Retransmit
			c.sendMu.Lock()
			for i := range c.sendBuf {
				entry := &c.sendBuf[i]
				if !entry.used || entry.acked {
					continue
				}
				age := now.Sub(entry.sentAt)
				if age > reliableTimeout {
					c.sendMu.Unlock()
					log.Printf("udp client: reliable message timed out")
					c.Close()
					return
				}
				if age >= retransmitInterval {
					seq := uint16(i)
					pkt := udpproto.EncodeReliable(c.token, seq, entry.payload)
					c.conn.Write(pkt)
					entry.sentAt = now
				}
			}
			c.sendMu.Unlock()

			// Send ACK
			if c.ackDirty {
				pkt := udpproto.EncodeACK(c.token, c.recvSeq, c.recvBits)
				c.conn.Write(pkt)
				c.ackDirty = false
			}

			// Keepalive
			if now.Sub(c.lastSend) > keepaliveInterval {
				pkt := udpproto.EncodeUnreliable(c.token, nil)
				c.conn.Write(pkt)
				c.lastSend = now
			}
		}
	}
}
