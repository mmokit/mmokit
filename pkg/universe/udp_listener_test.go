package universe

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/logger"
	pkgnet "github.com/zenion/mmokit/pkg/net"
	"github.com/zenion/mmokit/pkg/net/udpclient"
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

	client, err := udpclient.Dial(addr)
	if err != nil {
		t.Fatalf("udpclient.Dial(%s): %v — gateway did not bind UDP", addr, err)
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
