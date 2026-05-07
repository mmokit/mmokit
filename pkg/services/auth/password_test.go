package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("hunter2", DefaultArgonParams())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("bad encoded form: %s", h)
	}
	ok, err := VerifyPassword("hunter2", h)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected verify true")
	}
	bad, err := VerifyPassword("wrong", h)
	if err != nil {
		t.Fatal(err)
	}
	if bad {
		t.Fatal("expected verify false")
	}
}

func TestPasswordHashUniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same", DefaultArgonParams())
	h2, _ := HashPassword("same", DefaultArgonParams())
	if h1 == h2 {
		t.Fatal("salts must differ")
	}
}

func TestVerifyParsesEncodedParams(t *testing.T) {
	weaker := ArgonParams{Memory: 8192, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	h, _ := HashPassword("p", weaker)
	ok, _ := VerifyPassword("p", h)
	if !ok {
		t.Fatal("must verify with embedded weak params")
	}
}
