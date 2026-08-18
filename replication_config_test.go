package mmokit

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/spatial"
)

func TestDefaultReplicationConfigReadsSessionStreamGeneration(t *testing.T) {
	eng := engine.New(engine.Config{TickRate: 20}, net.NewConnManager(), logger.New())
	eng.Players.RegisterPlayer(77, "pilot")
	session := eng.Players.ByConnID(77)
	if session == nil {
		t.Fatal("player session was not registered")
	}

	cfg := DefaultReplicationConfig(newTestProcess(t), eng, spatial.NewHashGrid(100), nil)
	if cfg.StreamGeneration == nil {
		t.Fatal("default replication config has no stream-generation callback")
	}
	viewer := ViewerInfo{ConnID: 77}
	if generation, ok := cfg.StreamGeneration(&viewer); !ok || generation != 1 {
		t.Fatalf("initial stream generation = (%d, %v), want (1, true)", generation, ok)
	}

	session.StreamGeneration = 9
	if generation, ok := cfg.StreamGeneration(&viewer); !ok || generation != 9 {
		t.Fatalf("updated stream generation = (%d, %v), want (9, true)", generation, ok)
	}

	eng.Players.Remove(session)
	if generation, ok := cfg.StreamGeneration(&viewer); ok || generation != 0 {
		t.Fatalf("removed-session stream generation = (%d, %v), want (0, false)", generation, ok)
	}
}
