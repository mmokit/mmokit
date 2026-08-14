package bot

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mmokit/mmokit/pkg/services/auth"
)

// TestAuthenticate_RegisterSuccess verifies that a 200 from /auth/register
// returns the session cookie immediately (no /auth/login round-trip).
func TestAuthenticate_RegisterSuccess(t *testing.T) {
	cookieName := auth.DefaultHTTPOpts().CookieName
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/register" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "tok-abc"})
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"user_id":"u","username":"alice","expires_at_ms":0}`))
	}))
	defer srv.Close()

	c, err := Authenticate(srv.URL, "alice")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if c.Name != cookieName || c.Value != "tok-abc" {
		t.Fatalf("got cookie %+v, want %s=tok-abc", c, cookieName)
	}
}

// TestAuthenticate_FallsThroughToLogin verifies a 409 from /auth/register
// triggers a follow-up POST to /auth/login.
func TestAuthenticate_FallsThroughToLogin(t *testing.T) {
	cookieName := auth.DefaultHTTPOpts().CookieName
	var registerHits, loginHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/register":
			registerHits.Add(1)
			http.Error(w, `{"code":3,"message":"taken"}`, http.StatusConflict)
		case "/auth/login":
			loginHits.Add(1)
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "tok-login"})
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c, err := Authenticate(srv.URL, "alice")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if c.Value != "tok-login" {
		t.Fatalf("got cookie %+v, want tok-login", c)
	}
	if registerHits.Load() != 1 || loginHits.Load() != 1 {
		t.Fatalf("hits: register=%d login=%d, want 1/1", registerHits.Load(), loginHits.Load())
	}
}

// TestAuthenticate_RegisterError surfaces a non-409 register failure
// without attempting login.
func TestAuthenticate_RegisterError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/login" {
			t.Errorf("login should not be called when register fails non-409")
		}
		http.Error(w, `{"code":2,"message":"weak password"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := Authenticate(srv.URL, "alice")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "register:") {
		t.Fatalf("err %q should be wrapped with register:", err)
	}
}

// TestAuthenticate_NoCookie surfaces a server that returned 200 but
// forgot to set the session cookie.
func TestAuthenticate_NoCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := Authenticate(srv.URL, "alice")
	if err == nil || !strings.Contains(err.Error(), "cookie") {
		t.Fatalf("want missing-cookie error, got %v", err)
	}
}

// TestSetAuthClient_Restore verifies the restore closure puts the
// previous client back.
func TestSetAuthClient_Restore(t *testing.T) {
	prev := authClient
	restore := SetAuthClient(&fakeDoer{})
	if authClient == prev {
		t.Fatal("override did not take effect")
	}
	restore()
	if authClient != prev {
		t.Fatal("restore did not put previous client back")
	}
}

type fakeDoer struct{}

func (f *fakeDoer) Do(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("fake")
}
