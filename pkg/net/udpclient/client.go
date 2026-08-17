// Package udpclient provides a self-contained Go UDP client for the game server.
package udpclient

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpcrypto"
	"github.com/mmokit/mmokit/pkg/net/udpproto"
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

// ErrReliableWindowFull reports that all reliable ring slots are still
// awaiting ACKs. Callers may retry after the peer advances the ACK window.
var ErrReliableWindowFull = errors.New("udp client: reliable send window full")

// clientClockEpoch keeps activity timestamps atomic without giving up
// time.Time's monotonic clock behavior.
var clientClockEpoch = time.Now()

func clientClockStamp(now time.Time) int64 {
	return now.Sub(clientClockEpoch).Nanoseconds()
}

func advanceClientClock(clock *atomic.Int64, stamp int64) {
	for {
		previous := clock.Load()
		if stamp <= previous || clock.CompareAndSwap(previous, stamp) {
			return
		}
	}
}

type reliableEntry struct {
	seq         uint16
	payload     []byte
	firstSentAt time.Time
	lastSentAt  time.Time
	used        bool
}

// Client is a UDP game client that connects to the server.
type Client struct {
	conn  *net.UDPConn
	token uint32

	// crypto owns the traffic keys, the send counter and the replay window.
	crypto *udpcrypto.Session

	// Outbound reliability
	sendMu  sync.Mutex
	sendSeq uint16
	sendBuf [reliableBufSize]reliableEntry

	// Inbound reliability tracking
	recvMu   sync.Mutex
	recvInit bool
	recvSeq  uint16
	recvBits uint32
	ackDirty bool

	// Inbound message queue
	inMu    sync.Mutex
	inCond  *sync.Cond
	inbound [][]byte

	lastRecvTick atomic.Int64
	lastSendTick atomic.Int64
	closed       bool
	closeMu      sync.Mutex
	done         chan struct{}
}

// Dial connects to the game server at addr (e.g. "localhost:9000").
//
// keyID and key come from POST /auth/udp-key over HTTPS: the client
// authenticates there first and only then opens a UDP session. That ordering is
// the whole of CE-005b Tier 2 — v1 built a session first and authenticated
// afterwards over the op channel, which left every byte forgeable on path.
func Dial(addr string, keyID uint64, key udpcrypto.Key) (*Client, error) {
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
	cookie := make([]byte, 0, udpproto.CookieSize)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("handshake failed: %w", err)
		}
		if n == 0 || buf[0] != udpproto.TypeConnAccept {
			continue // early ServerConfig / stray packet — keep waiting for accept
		}
		respClientSalt, srvSalt, srvCookie, derr := udpproto.DecodeConnAccept(buf[:n])
		if derr != nil || respClientSalt != clientSalt {
			continue // malformed / stale accept — keep waiting
		}
		serverSalt = srvSalt
		cookie = append(cookie[:0], srvCookie...) // aliases buf; copy before reuse
		break
	}
	conn.SetReadDeadline(time.Time{})

	token := udpproto.MakeToken(clientSalt, serverSalt)

	sess, err := udpcrypto.NewSession(key, udpcrypto.RoleClient,
		udpproto.SessionSalt(clientSalt, serverSalt))
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("derive session keys: %w", err)
	}

	// Echo the cookie and name the key. The server holds nothing until this
	// arrives, so a lost ConnConfirm costs a retry rather than a leaked table
	// entry; promotion is idempotent on the server side.
	if _, err := conn.Write(udpproto.EncodeConnConfirm(keyID, clientSalt, serverSalt, cookie)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connection confirm failed: %w", err)
	}

	c := &Client{
		conn:    conn,
		token:   token,
		crypto:  sess,
		inbound: make([][]byte, 0, 32),
		done:    make(chan struct{}),
	}
	now := clientClockStamp(time.Now())
	c.lastRecvTick.Store(now)
	c.lastSendTick.Store(now)
	c.inCond = sync.NewCond(&c.inMu)

	go c.readLoop()
	go c.tickLoop()

	return c, nil
}

// sealSend seals body and writes header||sealed. buildHeader receives the
// counter udpcrypto allocated, because the counter lives in the header and the
// header is the AEAD's additional authenticated data.
func (c *Client) sealSend(body []byte, buildHeader func(counter uint64) []byte) error {
	hdr, sealed, err := c.crypto.SealWithHeader(body, buildHeader)
	if err != nil {
		return err
	}
	pkt := make([]byte, 0, len(hdr)+len(sealed))
	pkt = append(pkt, hdr...)
	pkt = append(pkt, sealed...)
	return c.writePacket(pkt)
}

// SendReliable sends a message with reliability guarantees.
func (c *Client) SendReliable(data []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.closeMu.Lock()
	closed := c.closed
	c.closeMu.Unlock()
	if closed {
		return net.ErrClosed
	}

	seq := c.sendSeq
	idx := seq % reliableBufSize
	if c.sendBuf[idx].used {
		return ErrReliableWindowFull
	}
	c.sendSeq++

	payload := make([]byte, len(data))
	copy(payload, data)
	now := time.Now()
	c.sendBuf[idx] = reliableEntry{
		seq:         seq,
		payload:     payload,
		firstSentAt: now,
		lastSentAt:  now,
		used:        true,
	}

	err := c.sealSend(payload, func(ctr uint64) []byte {
		return udpproto.EncodeReliableHeader(c.token, seq, ctr)
	})
	if err != nil {
		// A rejected initial write must not leave a frame that maintenance can
		// later retransmit despite the caller observing an error.
		c.sendBuf[idx] = reliableEntry{}
	}
	return err
}

// SendUnreliable sends a message with no delivery guarantee.
func (c *Client) SendUnreliable(data []byte) error {
	return c.sealSend(data, func(ctr uint64) []byte {
		return udpproto.EncodeUnreliableHeader(c.token, ctr)
	})
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
	// Match SendReliable and maintenance's sendMu -> closeMu lock order. This
	// makes close atomic with reliable admission and releases retained payloads.
	c.sendMu.Lock()
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		c.sendMu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	c.closeMu.Unlock()
	clear(c.sendBuf[:])
	c.sendMu.Unlock()

	// Hold the condition lock while broadcasting so Recv cannot miss the close
	// notification between checking c.closed and entering Cond.Wait.
	c.inMu.Lock()
	c.inCond.Broadcast()
	c.inMu.Unlock()

	// Send disconnect (best effort). Sealed with an empty body so the tag alone
	// proves we hold the session key; the server will not act on a teardown it
	// cannot authenticate.
	if hdr, sealed, err := c.crypto.SealWithHeader(nil, func(ctr uint64) []byte {
		return udpproto.EncodeDisconnectHeader(c.token, ctr)
	}); err == nil {
		pkt := make([]byte, 0, len(hdr)+len(sealed))
		pkt = append(pkt, hdr...)
		pkt = append(pkt, sealed...)
		c.conn.Write(pkt)
	}

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
	if len(data) == 0 {
		return
	}

	switch data[0] {
	case udpproto.TypeUnreliable:
		_, counter, aad, sealed, err := udpproto.DecodeUnreliable(data)
		if err != nil {
			return
		}
		payload, oerr := c.crypto.Open(nil, counter, sealed, aad)
		if oerr != nil {
			return // forged or replayed; must not refresh liveness
		}
		advanceClientClock(&c.lastRecvTick, clientClockStamp(time.Now()))
		if len(payload) == 0 {
			return // keepalive
		}
		c.inMu.Lock()
		c.inbound = append(c.inbound, payload)
		c.inCond.Signal()
		c.inMu.Unlock()

	case udpproto.TypeReliable:
		_, seq, counter, aad, sealed, err := udpproto.DecodeReliable(data)
		if err != nil {
			return
		}
		payload, oerr := c.crypto.Open(nil, counter, sealed, aad)
		if oerr != nil {
			return
		}
		advanceClientClock(&c.lastRecvTick, clientClockStamp(time.Now()))
		deliver := c.updateRecvTracking(seq)
		if deliver && len(payload) > 0 {
			c.inMu.Lock()
			c.inbound = append(c.inbound, payload)
			c.inCond.Signal()
			c.inMu.Unlock()
		}

	case udpproto.TypeACK:
		_, counter, aad, sealed, err := udpproto.DecodeACK(data)
		if err != nil {
			return
		}
		body, oerr := c.crypto.Open(nil, counter, sealed, aad)
		if oerr != nil {
			return
		}
		ackSeq, ackBits, berr := udpproto.DecodeACKBody(body)
		if berr != nil {
			return
		}
		advanceClientClock(&c.lastRecvTick, clientClockStamp(time.Now()))
		c.processACK(ackSeq, ackBits)
	}
}

func (c *Client) updateRecvTracking(seq uint16) bool {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()

	deliver := false
	if !c.recvInit {
		c.recvInit = true
		c.recvSeq = seq
		c.recvBits = 0
		deliver = true
	} else if udpproto.SeqGreaterThan(seq, c.recvSeq) {
		diff := seq - c.recvSeq
		if diff <= 32 {
			c.recvBits = (c.recvBits << diff) | (1 << (diff - 1))
		} else {
			c.recvBits = 0
		}
		c.recvSeq = seq
		deliver = true
	} else {
		diff := c.recvSeq - seq
		if diff > 0 && diff <= 32 {
			bit := uint32(1) << (diff - 1)
			if c.recvBits&bit == 0 {
				c.recvBits |= bit
				deliver = true
			}
		}
	}
	c.ackDirty = true
	return deliver
}

func (c *Client) processACK(ackSeq uint16, ackBits uint32) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.markAcked(ackSeq)
	for i := uint32(0); i < 32; i++ {
		if ackBits&(1<<i) != 0 {
			c.markAcked(ackSeq - uint16(i) - 1)
		}
	}
}

func (c *Client) markAcked(seq uint16) {
	idx := seq % reliableBufSize
	entry := &c.sendBuf[idx]
	if entry.used && entry.seq == seq {
		*entry = reliableEntry{}
	}
}

func (c *Client) writePacket(data []byte) error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}

	n, err := c.conn.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	c.lastSendTick.Store(clientClockStamp(time.Now()))
	return nil
}

func (c *Client) takePendingACK() (uint16, uint32, bool) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	if !c.ackDirty {
		return 0, 0, false
	}

	ackSeq, ackBits := c.recvSeq, c.recvBits
	c.ackDirty = false
	return ackSeq, ackBits, true
}

func (c *Client) sendACK() {
	ackSeq, ackBits, ok := c.takePendingACK()
	if !ok {
		return
	}

	err := c.sealSend(udpproto.EncodeACKBody(ackSeq, ackBits), func(ctr uint64) []byte {
		return udpproto.EncodeACKHeader(c.token, ctr)
	})
	if err != nil && !errors.Is(err, net.ErrClosed) {
		// Preserve a newer dirty state and retry the latest receive-window
		// snapshot after transient write failures.
		c.recvMu.Lock()
		c.ackDirty = true
		c.recvMu.Unlock()
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
			if !c.maintenanceTick(now) {
				return
			}
		}
	}
}

func (c *Client) maintenanceTick(now time.Time) bool {
	nowTick := clientClockStamp(now)
	if time.Duration(nowTick-c.lastRecvTick.Load()) > connectionTimeout {
		log.Printf("udp client: connection timeout")
		c.Close()
		return false
	}

	c.sendMu.Lock()
	for i := range c.sendBuf {
		entry := &c.sendBuf[i]
		if !entry.used {
			continue
		}
		if now.Sub(entry.firstSentAt) > reliableTimeout {
			c.sendMu.Unlock()
			log.Printf("udp client: reliable message timed out")
			c.Close()
			return false
		}
		if now.Sub(entry.lastSentAt) >= retransmitInterval {
			pkt := c.encodeReliableEntry(entry)
			c.writePacket(pkt)
			entry.lastSentAt = now
		}
	}
	c.sendMu.Unlock()

	c.sendACK()

	if time.Duration(nowTick-c.lastSendTick.Load()) > keepaliveInterval {
		c.sealSend(nil, func(ctr uint64) []byte {
			return udpproto.EncodeUnreliableHeader(c.token, ctr)
		})
	}
	return true
}

// encodeReliableEntry re-seals a retransmit with a FRESH counter. Resending the
// original bytes would carry the original counter and the server's replay
// window would discard them, making every retransmission a no-op.
func (c *Client) encodeReliableEntry(entry *reliableEntry) []byte {
	hdr, sealed, err := c.crypto.SealWithHeader(entry.payload, func(ctr uint64) []byte {
		return udpproto.EncodeReliableHeader(c.token, entry.seq, ctr)
	})
	if err != nil {
		return nil
	}
	pkt := make([]byte, 0, len(hdr)+len(sealed))
	pkt = append(pkt, hdr...)
	pkt = append(pkt, sealed...)
	return pkt
}
