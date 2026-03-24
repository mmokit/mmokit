package netutil

import (
	"log"

	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/pkg/net"
)

// MakeEvent builds a channel-0x00 frame: [0x00] + ServerEvent{code, data}.
func MakeEvent(code uint32, payload proto.Message) []byte {
	var inner []byte
	if payload != nil {
		var err error
		inner, err = proto.Marshal(payload)
		if err != nil {
			log.Printf("MakeEvent: marshal payload: %v", err)
			return nil
		}
	}
	evt := &gamepb.ServerEvent{
		Code: code,
		Data: inner,
	}
	evtData, err := proto.Marshal(evt)
	if err != nil {
		log.Printf("MakeEvent: marshal event: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(evtData))
	frame[0] = net.ChannelEvent
	copy(frame[1:], evtData)
	return frame
}

// MakeOpResponse builds a channel-0x01 frame: [0x01] + OperationResponse{code, reqID, returnCode, errorMsg, data}.
// The payload is already-serialized bytes (nil if no payload).
func MakeOpResponse(code, reqID uint32, returnCode int32, errorMsg string, payload []byte) []byte {
	inner := payload
	resp := &gamepb.OperationResponse{
		Code:       code,
		RequestId:  reqID,
		ReturnCode: returnCode,
		ErrorMsg:   errorMsg,
		Data:       inner,
	}
	respData, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("MakeOpResponse: marshal response: %v", err)
		return nil
	}
	frame := make([]byte, 1+len(respData))
	frame[0] = net.ChannelOperation
	copy(frame[1:], respData)
	return frame
}
