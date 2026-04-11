package replication

import "testing"

type fakeViewer struct {
	id   uint64
	x, y float32
	sent []Frame
}

func (v *fakeViewer) ID() uint64                   { return v.id }
func (v *fakeViewer) Position() (float32, float32) { return v.x, v.y }
func (v *fakeViewer) Tier(entityKind uint16) ReplicationTier {
	return ReplicationTier{Radius: 100}
}
func (v *fakeViewer) Baselines() *BaselineStore { return nil }
func (v *fakeViewer) Send(frame Frame)          { v.sent = append(v.sent, frame) }

func TestViewer_Interface(t *testing.T) {
	var v Viewer = &fakeViewer{id: 1, x: 10, y: 20}
	if v.ID() != 1 {
		t.Fatal("ID mismatch")
	}
	x, y := v.Position()
	if x != 10 || y != 20 {
		t.Fatal("position mismatch")
	}
	v.Send(Frame{})
	if len(v.(*fakeViewer).sent) != 1 {
		t.Fatal("send not recorded")
	}
}
