package universe

import (
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

// frameEvent wraps a proto login message in the same envelope the gateway
// sends to the coordinator: enginepb.ClientEvent with a code and the
// marshalled payload as Data.
func frameEvent(t *testing.T, code uint32, payload proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(payload)
	if err != nil {
		t.Fatalf("proto.Marshal payload: %v", err)
	}
	evt := &enginepb.ClientEvent{Code: code, Data: data}
	raw, err := proto.Marshal(evt)
	if err != nil {
		t.Fatalf("proto.Marshal envelope: %v", err)
	}
	return raw
}

func TestHandleLoginSuccess(t *testing.T) {
	const code = 42

	handler := HandleLogin(code, func(m *enginepb.LoginMsg) (string, any, error) {
		name, err := ValidateUsername(m.Username, 20)
		return name, "session-data", err
	})

	frame := frameEvent(t, code, &enginepb.LoginMsg{Username: "  Alice  "})
	name, data, err := handler(1, [][]byte{frame})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "alice" {
		t.Errorf("name = %q, want %q (trimmed + lowercased)", name, "alice")
	}
	if data != "session-data" {
		t.Errorf("data = %v, want %q", data, "session-data")
	}
}

func TestHandleLoginSkipsNonMatchingCode(t *testing.T) {
	const loginCode = 42
	const otherCode = 7

	handler := HandleLogin(loginCode, func(m *enginepb.LoginMsg) (string, any, error) {
		return m.Username, nil, nil
	})

	// A batch where the login frame is preceded by an unrelated event: the
	// handler must skip the non-matching code and still find the login.
	noise := frameEvent(t, otherCode, &enginepb.LoginMsg{Username: "noise"})
	login := frameEvent(t, loginCode, &enginepb.LoginMsg{Username: "bob"})

	name, _, err := handler(1, [][]byte{noise, login})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "bob" {
		t.Errorf("name = %q, want %q", name, "bob")
	}
}

func TestHandleLoginPending(t *testing.T) {
	handler := HandleLogin(1, func(m *enginepb.LoginMsg) (string, any, error) {
		return m.Username, nil, nil
	})

	// No frames at all → pending.
	_, _, err := handler(1, nil)
	if !errors.Is(err, ErrLoginPending) {
		t.Errorf("empty msgs: got %v, want ErrLoginPending", err)
	}

	// Only non-matching codes → pending.
	frame := frameEvent(t, 99, &enginepb.LoginMsg{Username: "alice"})
	_, _, err = handler(1, [][]byte{frame})
	if !errors.Is(err, ErrLoginPending) {
		t.Errorf("non-matching code: got %v, want ErrLoginPending", err)
	}
}

func TestHandleLoginSkipsMalformed(t *testing.T) {
	handler := HandleLogin(1, func(m *enginepb.LoginMsg) (string, any, error) {
		return m.Username, nil, nil
	})

	// Garbage envelope bytes must not crash or return spurious errors — the
	// handler just walks past them and keeps looking.
	valid := frameEvent(t, 1, &enginepb.LoginMsg{Username: "alice"})
	name, _, err := handler(1, [][]byte{[]byte("not-a-proto"), valid})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if name != "alice" {
		t.Errorf("name = %q, want %q", name, "alice")
	}
}

func TestHandleLoginPropagatesExtractError(t *testing.T) {
	custom := errors.New("username blacklisted")
	handler := HandleLogin(1, func(m *enginepb.LoginMsg) (string, any, error) {
		return "", nil, custom
	})

	frame := frameEvent(t, 1, &enginepb.LoginMsg{Username: "alice"})
	_, _, err := handler(1, [][]byte{frame})
	if !errors.Is(err, custom) {
		t.Errorf("err = %v, want %v", err, custom)
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		maxLen  int
		want    string
		wantErr bool
	}{
		{"trim + lowercase", "  Alice  ", 20, "alice", false},
		{"already normalized", "bob", 20, "bob", false},
		{"empty after trim", "   ", 20, "", true},
		{"empty", "", 20, "", true},
		{"at boundary", "aaaaaaaaaaaaaaaaaaaa", 20, "aaaaaaaaaaaaaaaaaaaa", false},
		{"over boundary", "aaaaaaaaaaaaaaaaaaaaa", 20, "", true},
		{"no length cap", "a-very-long-name-here-goes-lots-more", 0, "a-very-long-name-here-goes-lots-more", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateUsername(tc.raw, tc.maxLen)
			if tc.wantErr {
				if !errors.Is(err, ErrLoginPending) {
					t.Errorf("err = %v, want ErrLoginPending", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
