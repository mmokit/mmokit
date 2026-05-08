package service_test

import (
	"sync/atomic"
	"testing"

	"github.com/zenion/mmoserver/pkg/service"
)

type ping struct{ N int }

func TestBus_SubscribePublishRoundtrip(t *testing.T) {
	b := service.NewBus("test-proc")
	var got int32
	service.Subscribe(b, func(ev ping) {
		atomic.AddInt32(&got, int32(ev.N))
	})
	service.Publish(b, ping{N: 7})
	if g := atomic.LoadInt32(&got); g != 7 {
		t.Fatalf("handler not invoked: got=%d want=7", g)
	}
}
