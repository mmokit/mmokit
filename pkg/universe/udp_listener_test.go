package universe

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/logger"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/net/udpclient"
)

// freeUDPAddr binds an ephemeral UDP port, closes it, and returns the address —
// a free port the test can hand to startUDPListener.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	addr := c.LocalAddr().String()
	c.Close()
	return addr
}

// A Gateway-role process must bind the UDP game protocol so a client can
// complete the ConnReq/ConnAccept handshake — this is what makes the C# SDK
// (and the Go udpclient) able to connect without any per-game wiring.
func TestStartUDPListener_GatewayBindsAndHandshakes(t *testing.T) {
	addr := freeUDPAddr(t)
	p := &Process{
		ConnMgr: pkgnet.NewConnManager(),
		Log:     logger.New(),
		roles:   Roles{RoleGateway: {}},
		cfg:     Config{UDPListen: addr},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.startUDPListener(ctx)
	time.Sleep(50 * time.Millisecond) // let the goroutine bind

	// A UDP session now requires a key issued over HTTPS, so mint one directly
	// from the process registry the listener was wired to.
	keyID, entry, err := p.UDPKeyRegistry().Issue("test-user", "tester", time.Now())
	if err != nil {
		t.Fatalf("issue udp key: %v", err)
	}
	client, err := udpclient.Dial(addr, uint64(keyID), entry.Key)
	if err != nil {
		t.Fatalf("udpclient.Dial(%s): %v — gateway did not bind UDP", addr, err)
	}

	// The handshake must actually establish a session, not merely bind a port.
	deadline := time.Now().Add(2 * time.Second)
	established := false
	for time.Now().Before(deadline) {
		if stats, ok := p.UDPStats(); ok && stats.HandshakeRejectDrops == 0 {
			if len(p.ConnMgr.ActiveConnIDs()) > 0 {
				established = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !established {
		stats, _ := p.UDPStats()
		t.Fatalf("authenticated handshake did not establish a session (handshakeRejects=%d)",
			stats.HandshakeRejectDrops)
	}
	client.Close()

	// Cancelling the ctx stops the listener; the port frees up.
	cancel()
	time.Sleep(50 * time.Millisecond)
	c, err := net.ListenUDP("udp", mustResolveUDP(t, addr))
	if err != nil {
		t.Fatalf("port %s not released after ctx cancel: %v", addr, err)
	}
	c.Close()
}

// A process without the Gateway role must NOT bind UDP (the port stays free).
func TestStartUDPListener_NonGatewayNoop(t *testing.T) {
	addr := freeUDPAddr(t)
	p := &Process{
		ConnMgr: pkgnet.NewConnManager(),
		Log:     logger.New(),
		roles:   Roles{RoleCoordinator: {}}, // no RoleGateway
		cfg:     Config{UDPListen: addr},
	}
	p.startUDPListener(context.Background())

	// The listener was a no-op, so we can still bind the port ourselves.
	c, err := net.ListenUDP("udp", mustResolveUDP(t, addr))
	if err != nil {
		t.Fatalf("non-gateway process bound UDP %s (should be a no-op): %v", addr, err)
	}
	c.Close()
}

// An empty UDPListen disables the listener even on a Gateway process.
func TestStartUDPListener_EmptyAddrDisabled(t *testing.T) {
	p := &Process{
		ConnMgr: pkgnet.NewConnManager(),
		Log:     logger.New(),
		roles:   Roles{RoleGateway: {}},
		cfg:     Config{UDPListen: ""},
	}
	// Must not panic and must not bind anything.
	p.startUDPListener(context.Background())
}

func mustResolveUDP(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve %s: %v", addr, err)
	}
	return a
}
