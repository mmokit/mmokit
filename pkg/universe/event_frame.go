package universe

import (
	"log"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	netpkg "github.com/zenion/mmoserver/pkg/net"
)

// makeEventFrame builds a channel-0x00 event frame from a server event code
// and a proto payload. This is the universe-level equivalent of mmokit.MakeEvent.
func makeEventFrame(code uint32, payload proto.Message) []byte {
	var inner []byte
	if payload != nil {
		var err error
		inner, err = proto.Marshal(payload)
		if err != nil {
			log.Printf("makeEventFrame: marshal payload: %v", err)
			return nil
		}
	}
	evt := &enginepb.ServerEvent{
		Code: code,
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		log.Printf("makeEventFrame: marshal envelope: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = netpkg.ChannelEvent
	copy(frame[1:], evtData)
	return frame
}
