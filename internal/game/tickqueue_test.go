package game

import "testing"

type testEventA struct{ Value int }
type testEventB struct{ Name string }

func TestTickQueue_EnqueueDrainRoundTrip(t *testing.T) {
	q := NewTickQueue()
	Enqueue(q, testEventA{1})
	Enqueue(q, testEventA{2})
	Enqueue(q, testEventA{3})

	got := Drain[testEventA](q)
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
	for i, want := range []int{1, 2, 3} {
		if got[i].Value != want {
			t.Errorf("item %d: got %d, want %d", i, got[i].Value, want)
		}
	}
}

func TestTickQueue_DrainClears(t *testing.T) {
	q := NewTickQueue()
	Enqueue(q, testEventA{1})
	Drain[testEventA](q)

	got := Drain[testEventA](q)
	if len(got) != 0 {
		t.Fatalf("second drain should return empty slice, got %d items", len(got))
	}
}

func TestTickQueue_PeekDoesNotClear(t *testing.T) {
	q := NewTickQueue()
	Enqueue(q, testEventA{42})

	peek1 := Peek[testEventA](q)
	if len(peek1) != 1 || peek1[0].Value != 42 {
		t.Fatalf("first peek: got %v, want [{42}]", peek1)
	}

	peek2 := Peek[testEventA](q)
	if len(peek2) != 1 || peek2[0].Value != 42 {
		t.Fatalf("second peek: got %v, want [{42}]", peek2)
	}
}

func TestTickQueue_MultipleTypesIndependent(t *testing.T) {
	q := NewTickQueue()
	Enqueue(q, testEventA{10})
	Enqueue(q, testEventB{"hello"})
	Enqueue(q, testEventA{20})

	as := Drain[testEventA](q)
	bs := Drain[testEventB](q)

	if len(as) != 2 {
		t.Fatalf("expected 2 A events, got %d", len(as))
	}
	if as[0].Value != 10 || as[1].Value != 20 {
		t.Errorf("A events: got %v", as)
	}
	if len(bs) != 1 || bs[0].Name != "hello" {
		t.Fatalf("B events: got %v, want [{hello}]", bs)
	}
}

func TestTickQueue_ClearAll(t *testing.T) {
	q := NewTickQueue()
	Enqueue(q, testEventA{1})
	Enqueue(q, testEventB{"x"})

	q.ClearAll()

	as := Drain[testEventA](q)
	bs := Drain[testEventB](q)
	if len(as) != 0 {
		t.Errorf("expected 0 A events after ClearAll, got %d", len(as))
	}
	if len(bs) != 0 {
		t.Errorf("expected 0 B events after ClearAll, got %d", len(bs))
	}
}

func TestTickQueue_EmptyDrainReturnsNil(t *testing.T) {
	q := NewTickQueue()
	got := Drain[testEventA](q)
	if got != nil {
		t.Fatalf("expected nil from empty drain, got %v", got)
	}
}

func TestTickQueue_EmptyPeekReturnsNil(t *testing.T) {
	q := NewTickQueue()
	got := Peek[testEventA](q)
	if got != nil {
		t.Fatalf("expected nil from empty peek, got %v", got)
	}
}
