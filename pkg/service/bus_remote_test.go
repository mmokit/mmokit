package service_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zenion/mmokit/pkg/service"
)

type remoteEvent struct{ Tag int }

func init() { service.RegisterWireEvent[remoteEvent]() }

func TestBus_RemotePublishCallback_FiresAfterLocalDispatch(t *testing.T) {
	b := service.NewBus("proc-A")

	var localFired int32
	service.Subscribe(b, func(ev remoteEvent) { atomic.AddInt32(&localFired, 1) })

	var remoteCalls []service.RemotePublishCall
	var mu sync.Mutex
	b.SetRemotePublish(func(call service.RemotePublishCall) {
		mu.Lock()
		remoteCalls = append(remoteCalls, call)
		mu.Unlock()
	})

	b.SetRoutingCache(map[string][]string{
		"github.com/zenion/mmokit/pkg/service_test.remoteEvent": {"proc-B", "proc-C"},
	})

	service.Publish(b, remoteEvent{Tag: 9})

	if atomic.LoadInt32(&localFired) != 1 {
		t.Fatal("local subscriber not fired")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(remoteCalls) != 1 {
		t.Fatalf("RemotePublish callback called %d times, want 1", len(remoteCalls))
	}
	got := remoteCalls[0]
	if got.TypeName != "github.com/zenion/mmokit/pkg/service_test.remoteEvent" {
		t.Fatalf("typeName=%q", got.TypeName)
	}
	want := map[string]bool{"proc-B": true, "proc-C": true}
	if len(got.PeerIDs) != 2 || !want[got.PeerIDs[0]] || !want[got.PeerIDs[1]] {
		t.Fatalf("peerIDs=%v", got.PeerIDs)
	}
}

func TestBus_PublishLocal_SkipsRemote(t *testing.T) {
	b := service.NewBus("proc-A")
	service.Subscribe(b, func(ev remoteEvent) {})
	var remoteCalled int32
	b.SetRemotePublish(func(call service.RemotePublishCall) { atomic.AddInt32(&remoteCalled, 1) })
	b.SetRoutingCache(map[string][]string{
		"github.com/zenion/mmokit/pkg/service_test.remoteEvent": {"proc-B"},
	})
	service.PublishLocal(b, remoteEvent{Tag: 1})
	if atomic.LoadInt32(&remoteCalled) != 0 {
		t.Fatalf("PublishLocal must not invoke RemotePublish")
	}
}

type localOnlyEvent struct{ Tag int }

func init() { service.RegisterEventType[localOnlyEvent]() } // local-only — NOT wire-eligible

func TestBus_NonWireEvent_SuppressesRemoteFanout(t *testing.T) {
	b := service.NewBus("proc-A")
	var remoteCalled int32
	b.SetRemotePublish(func(call service.RemotePublishCall) { atomic.AddInt32(&remoteCalled, 1) })

	// Routing cache claims proc-B subscribes — but the type is local-only,
	// so the fan-out must NOT fire.
	b.SetRoutingCache(map[string][]string{
		"github.com/zenion/mmokit/pkg/service_test.localOnlyEvent": {"proc-B"},
	})

	service.Publish(b, localOnlyEvent{Tag: 1})
	if atomic.LoadInt32(&remoteCalled) != 0 {
		t.Fatalf("non-wire-eligible Publish leaked to remote dispatch (called %d times)", atomic.LoadInt32(&remoteCalled))
	}
}

func TestBus_RemotePublish_SelfSkipped(t *testing.T) {
	b := service.NewBus("proc-A")
	var calls []service.RemotePublishCall
	b.SetRemotePublish(func(call service.RemotePublishCall) {
		calls = append(calls, call)
	})
	b.SetRoutingCache(map[string][]string{
		"github.com/zenion/mmokit/pkg/service_test.remoteEvent": {"proc-A", "proc-B"},
	})
	service.Publish(b, remoteEvent{Tag: 1})
	if len(calls) != 1 {
		t.Fatalf("expected one RemotePublish call, got %d", len(calls))
	}
	if len(calls[0].PeerIDs) != 1 || calls[0].PeerIDs[0] != "proc-B" {
		t.Fatalf("expected proc-B only, got %v", calls[0].PeerIDs)
	}
}
