package udpclient

import (
	"net"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/net/udpcrypto"
	"github.com/mmokit/mmokit/pkg/net/udpproto"
)

// On connect the real server pushes an unsolicited reliable ServerConfig frame
// (type 0x01) that races the ConnAccept (0x04) — two separate datagrams with no
// ordering guarantee. Dial must discard the non-accept datagram and complete the
// handshake on the ConnAccept. This reproduces the "unexpected handshake
// response" the C# SDK hit against a live (game-loop-running) server.
func TestDial_SkipsRacingServerConfigFrame(t *testing.T) {
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		n, from, err := srv.ReadFromUDP(buf)
		if err != nil {
			return
		}
		clientSalt, err := udpproto.DecodeConnReq(buf[:n])
		if err != nil {
			return
		}
		// Racing ServerConfig stand-in: a reliable (0x01) datagram FIRST...
		srv.WriteToUDP([]byte{udpproto.TypeReliable, udpproto.Version, 0, 0, 0, 0, 0, 0, 9, 9, 9}, from)
		// ...then the real ConnAccept the handshake is waiting for.
		cookie := make([]byte, udpproto.CookieSize)
		srv.WriteToUDP(udpproto.EncodeConnAccept(clientSalt, 0xABCDEF, cookie), from)
		// Dial writes ConnConfirm next; read and discard it so the socket does
		// not fill, then let the test finish.
		srv.SetReadDeadline(time.Now().Add(2 * time.Second))
		srv.ReadFromUDP(buf)
	}()

	var key udpcrypto.Key
	c, err := Dial(srv.LocalAddr().String(), 1, key)
	if err != nil {
		t.Fatalf("Dial must skip the racing 0x01 frame and accept on 0x04: %v", err)
	}
	c.Close()
	<-done
}
