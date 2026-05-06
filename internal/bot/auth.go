package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zenion/mmoserver/pkg/auth"
)

// devPassword is the fixed password every bot registers / logs in with.
// Bots are dev-only load-test clients; a single shared password is fine —
// the server-side auth machinery (rate limiting, lockout, audit log)
// remains unchanged. Production accounts use real passwords.
const devPassword = "bot-test-password-do-not-use-in-prod"

// errUserExists signals that /auth/register returned 409 (username
// already taken). Authenticate falls through to /auth/login when this
// fires.
var errUserExists = errors.New("username already exists")

// httpDoer is the subset of *http.Client Authenticate needs. Tests can
// substitute a fake to validate the flow without a live server.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// authClient is the HTTP client Authenticate uses. Defaults to
// http.DefaultClient; tests override via SetAuthClient. Wrapped in a
// var so concurrent overrides during a single test are caller-managed.
var authClient httpDoer = http.DefaultClient

// SetAuthClient swaps the HTTP client used for /auth/register and
// /auth/login. Restore the previous value after the test finishes —
// the override is process-global.
func SetAuthClient(c httpDoer) (restore func()) {
	prev := authClient
	authClient = c
	return func() { authClient = prev }
}

// Authenticate performs the same /auth/register or /auth/login flow the
// web client uses. Returns the session cookie that the gateway validates
// at WebSocket upgrade time. On 409 (username taken) it falls through to
// /auth/login. The returned cookie has the cookie name configured on the
// server (auth.DefaultHTTPOpts().CookieName, currently "mmokit-session").
func Authenticate(serverURL, username string) (*http.Cookie, error) {
	base := strings.TrimSuffix(serverURL, "/")

	// 1. Try register first (idempotent on first-time bots).
	cookie, err := postAuth(base+"/auth/register", username, devPassword)
	if err == nil {
		return cookie, nil
	}
	if !errors.Is(err, errUserExists) {
		return nil, fmt.Errorf("register: %w", err)
	}

	// 2. Username taken — log in instead.
	cookie, err = postAuth(base+"/auth/login", username, devPassword)
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return cookie, nil
}

// postAuth POSTs a {username, password} body to url and returns the
// session cookie from the Set-Cookie header. Returns errUserExists on
// HTTP 409 so Authenticate can switch to the login path.
func postAuth(url, username, password string) (*http.Cookie, error) {
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := authClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, errUserExists
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	cookieName := auth.DefaultHTTPOpts().CookieName
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			return c, nil
		}
	}
	return nil, fmt.Errorf("auth response had no %q cookie", cookieName)
}
