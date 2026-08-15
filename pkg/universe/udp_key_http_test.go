package universe

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/logger"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/services/auth"
)

// fakeResolver stands in for the auth service's session validator.
type fakeResolver struct {
	token string
	sess  *auth.ResolvedSession
}

func (f *fakeResolver) Resolve(_ context.Context, token string) (*auth.ResolvedSession, error) {
	if token != f.token {
		return nil, auth.ErrTokenInvalid
	}
	return f.sess, nil
}

func udpKeyProcess(t *testing.T, resolver auth.Resolver, devInsecure bool) *Process {
	t.Helper()
	return &Process{
		cfg: Config{
			AuthResolver:      resolver,
			DevInsecureCookie: devInsecure,
			AuthHTTPOpts:      auth.DefaultHTTPOpts(),
		},
		Log: logger.New(),
	}
}

func goodResolver() *fakeResolver {
	return &fakeResolver{
		token: "valid-session-token",
		sess: &auth.ResolvedSession{
			UserID:    uuid.MustParse("11111111-2222-3333-4444-555555555555"),
			Username:  "alice",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
}

func postUDPKey(p *Process, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/auth/udp-key", nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.DefaultHTTPOpts().CookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	p.handleUDPKeyIssue(w, r)
	return w
}

// Without a resolver the process cannot know who is asking, so the route must
// not exist rather than fall back to anonymous issuance.
func TestUDPKeyRequiresAuthResolver(t *testing.T) {
	p := udpKeyProcess(t, nil, true)
	if got := postUDPKey(p, "anything").Code; got != http.StatusNotFound {
		t.Fatalf("status = %d want 404 when no AuthResolver is configured", got)
	}
}

// The key is a bearer secret. A plaintext listener must refuse unless the
// operator has already accepted plaintext credentials via --dev-insecure-cookie.
func TestUDPKeyRefusesPlaintextWithoutDevFlag(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), false)
	w := postUDPKey(p, "valid-session-token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d want 403 on a plaintext listener without --dev-insecure-cookie", w.Code)
	}
	if strings.Contains(w.Body.String(), "key") && strings.Contains(w.Body.String(), "=") {
		t.Fatal("refusal body appears to contain key material")
	}
}

func TestUDPKeyRejectsMissingCookie(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), true)
	if got := postUDPKey(p, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401 with no cookie", got)
	}
}

func TestUDPKeyRejectsInvalidCookie(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), true)
	if got := postUDPKey(p, "wrong-token").Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401 with an unresolvable cookie", got)
	}
}

func TestUDPKeyIssuesAndRegisters(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), true)
	w := postUDPKey(p, "valid-session-token")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}

	var resp UDPKeyResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// keyId must be a hex STRING: it is a uint64, and a JSON number would lose
	// precision above 2^53 in a browser client.
	idBytes, err := hex.DecodeString(resp.KeyID)
	if err != nil || len(idBytes) != 8 {
		t.Fatalf("keyId = %q, want 16 hex chars: %v", resp.KeyID, err)
	}
	var id uint64
	for _, b := range idBytes {
		id = id<<8 | uint64(b)
	}

	key, err := base64.StdEncoding.DecodeString(resp.Key)
	if err != nil {
		t.Fatalf("key not base64: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("key length = %d want 32", len(key))
	}
	if resp.ExpiresAtMs <= time.Now().UnixMilli() {
		t.Fatalf("expiresAtMs = %d is not in the future", resp.ExpiresAtMs)
	}

	// The issued key must be resolvable by the UDP side under that exact ID,
	// and must be the same bytes the client was handed.
	entry, err := p.UDPKeyRegistry().Lookup(pkgnet.UDPKeyID(id), time.Now())
	if err != nil {
		t.Fatalf("issued key not resolvable from the registry: %v", err)
	}
	if string(entry.Key[:]) != string(key) {
		t.Fatal("registry key differs from the key handed to the client")
	}
	if entry.Username != "alice" {
		t.Fatalf("entry.Username = %q want alice", entry.Username)
	}
	if entry.UserID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("entry.UserID = %q", entry.UserID)
	}
}

// A bearer secret must not be cached by a proxy or by the browser.
func TestUDPKeyResponseIsNotCacheable(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), true)
	w := postUDPKey(p, "valid-session-token")
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q want no-store", got)
	}
}

// Two calls must never hand out the same key, or two sessions would share a
// keystream.
func TestUDPKeyIssuesDistinctKeys(t *testing.T) {
	p := udpKeyProcess(t, goodResolver(), true)
	seen := map[string]bool{}
	for i := range 20 {
		w := postUDPKey(p, "valid-session-token")
		if w.Code != http.StatusOK {
			t.Fatalf("issue %d: status %d", i, w.Code)
		}
		var resp UDPKeyResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if seen[resp.Key] {
			t.Fatalf("duplicate key material at issue %d", i)
		}
		if seen[resp.KeyID] {
			t.Fatalf("duplicate key id at issue %d", i)
		}
		seen[resp.Key] = true
		seen[resp.KeyID] = true
	}
}
