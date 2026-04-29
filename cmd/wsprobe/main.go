package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://localhost:8080/ws", nil)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close(websocket.StatusNormalClosure, "bye")
	fmt.Println("connected")

	loginMsg := &enginepb.LoginMsg{Username: "wsprobe"}
	loginData, _ := proto.Marshal(loginMsg)
	envelope := &enginepb.ClientEvent{Code: uint32(enginepb.ClientEventCode_CE_LOGIN), Data: loginData}
	envBytes, _ := proto.Marshal(envelope)
	frame := append([]byte{0x00}, envBytes...)
	if err := c.Write(ctx, websocket.MessageBinary, frame); err != nil {
		fmt.Println("write login:", err)
		os.Exit(1)
	}
	fmt.Println("sent CE_LOGIN; reading responses...")

	for i := 0; i < 30; i++ {
		rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
		_, data, err := c.Read(rctx)
		rcancel()
		if err != nil {
			fmt.Println("read err:", err)
			return
		}
		if len(data) == 0 {
			continue
		}
		ch := data[0]
		body := data[1:]
		if ch == 0x00 && len(body) > 0 {
			var ev enginepb.ServerEvent
			if perr := proto.Unmarshal(body, &ev); perr == nil {
				fmt.Printf("ServerEvent code=%d datalen=%d\n", ev.Code, len(ev.Data))
				continue
			}
		}
		fmt.Printf("frame ch=%02x len=%d\n", ch, len(data))
	}
}
