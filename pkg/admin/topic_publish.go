package admin

import (
	"encoding/json"
	"sync"

	"github.com/mmokit/mmokit/pkg/universe"
)

// busMap caches the *TopicBus per *universe.Process so game-side code can
// publish to admin topics before the admin Server is constructed.
// DefaultAdminServerFactory pulls the same bus into ServerOpts, so the SSE
// multiplexer fans payloads out to subscribers that subscribed either side of
// server construction.
var (
	busMu  sync.Mutex
	busMap = map[*universe.Process]*TopicBus{}
)

// BusFor returns the admin topic bus for p, creating it on first use.
func BusFor(p *universe.Process) *TopicBus {
	busMu.Lock()
	defer busMu.Unlock()
	b, ok := busMap[p]
	if !ok {
		b = NewTopicBus(0)
		busMap[p] = b
	}
	return b
}

// ForgetBus drops p's cached bus, releasing the map entry that would otherwise
// pin the *universe.Process for the life of the program. Call it when a process
// is torn down; tests that spin up throwaway processes want it in cleanup.
// Publishing again after ForgetBus simply builds a fresh bus, so a late
// publisher gets a live (if unsubscribed) bus rather than a panic.
func ForgetBus(p *universe.Process) {
	busMu.Lock()
	defer busMu.Unlock()
	delete(busMap, p)
}

// PublishTopic publishes payload to an admin dashboard topic.
//
// On a process that dialed a remote coordinator (--mode=host, or --mode=service
// with --coordinator-addr) there is no local SSE server, so the payload is
// marshaled and forwarded over MeshControl instead. Everywhere else it goes
// straight to the local bus. Both paths are best-effort: a marshal failure or a
// downed control stream logs and drops rather than blocking the caller, which
// matters because operator verbs call this from command handlers.
func PublishTopic(coord *universe.Process, topic string, payload any) {
	if coord.ForwardsAdminTopics() {
		b, err := json.Marshal(payload)
		if err != nil {
			coord.Log.Log("admin", "PublishTopic: marshal topic %q: %v", topic, err)
			return
		}
		if err := coord.ForwardAdminTopic(topic, b); err != nil {
			coord.Log.Log("admin", "PublishTopic: topic %q dropped: %v", topic, err)
		}
		return
	}
	BusFor(coord).Publish(topic, payload)
}
