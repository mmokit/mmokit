package auth

import (
	"encoding/base64"
	"testing"
)

func TestNewTokenLengthAndEntropy(t *testing.T) {
	t1, h1, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(raw))
	}
	if len(h1) != 32 {
		t.Fatalf("hash want 32 bytes, got %d", len(h1))
	}
	t2, _, _ := NewToken()
	if t1 == t2 {
		t.Fatal("tokens must differ")
	}
}

func TestHashTokenStable(t *testing.T) {
	tok, h1, _ := NewToken()
	h2 := HashToken(tok)
	if string(h1) != string(h2) {
		t.Fatal("HashToken not stable")
	}
}
