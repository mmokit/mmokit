package universe

import (
	"fmt"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
)

// ForwardsAdminTopics reports whether this process should ship admin topic
// publishes to a remote coordinator instead of publishing to its local bus:
// true only for processes that dialed a remote coordinator (--mode=host or
// --mode=service with --coordinator-addr), which never run the admin SSE
// server themselves. Coordinator-bearing and single-process (`all`) setups
// return false and publish locally.
//
// Note: a service riding a standalone gateway (--mode=service,gateway) has no
// controlClient — its publishes stay local and are dropped; wire a
// gateway-client path if that combination ever needs admin topics.
func (c *Process) ForwardsAdminTopics() bool {
	return c.controlClient != nil
}

// ForwardAdminTopic ships one pre-marshaled admin topic event to the
// coordinator over MeshControl. Best-effort, like the log forwarder: when the
// control stream is down the event is dropped with an error rather than waiting
// on reconnect. A live-stream send can still block briefly on flow control —
// fine for operator-action-frequency admin events, but don't put this on a
// per-tick hot path.
func (c *Process) ForwardAdminTopic(topic string, payloadJSON []byte) error {
	if c.controlClient == nil {
		return fmt.Errorf("mesh control: ForwardAdminTopic: no control client (not a remote host process)")
	}
	return c.controlClient.sendIfReady(&meshpb.HostMessage{
		Msg: &meshpb.HostMessage_AdminTopicEvent{
			AdminTopicEvent: &meshpb.AdminTopicEvent{Topic: topic, Payload: payloadJSON},
		},
	})
}

// OnRemoteAdminTopic installs the callback invoked when a host forwards an
// AdminTopicEvent over MeshControl. Stored atomically so it may be installed
// after the control server is live. The callback fires on the controlServer
// goroutine and must not block; payload is the sender's pre-marshaled JSON.
// Wired by mmokit.DefaultAdminServerFactory.
// Passing nil uninstalls the callback.
func (c *Process) OnRemoteAdminTopic(fn func(topic string, payload []byte)) {
	if fn == nil {
		c.remoteAdminTopic.Store(nil)
		return
	}
	c.remoteAdminTopic.Store(&fn)
}
