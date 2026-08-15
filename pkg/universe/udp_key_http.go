package universe

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	pkgnet "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/services/auth"
)

// CE-005b Tier 2 unit 2: POST /auth/udp-key.
//
// The client authenticates over HTTPS, receives a UDP session key, and proves
// possession of it on the UDP handshake. This route is mounted next to /ws on
// the gateway's own listener and validates the session through the same
// Config.AuthResolver + cookie path onWSUpgrade uses, so issuance and
// consumption are both gateway-local and no key ever crosses a process
// boundary.

// UDPKeyResponse is the JSON body returned on success.
//
// KeyID is a hex string, not a number: it is a uint64, and a JSON number would
// silently lose precision above 2^53 in every browser client.
//
// Key is the raw 32-byte secret, base64-encoded. It is a bearer credential for
// the lifetime of its TTL, which is why this handler refuses to serve it over a
// plaintext listener unless --dev-insecure-cookie is set.
type UDPKeyResponse struct {
	KeyID       string `json:"keyId"`
	Key         string `json:"key"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// udpKeyRegistry returns the process-level registry, creating it on first use.
func (c *Process) udpKeyRegistry() *pkgnet.UDPKeyRegistry {
	c.udpKeysOnce.Do(func() {
		c.udpKeys = pkgnet.NewUDPKeyRegistry(0, 0)
	})
	return c.udpKeys
}

// UDPKeyRegistry exposes the registry so the UDP listener can resolve a key ID
// presented on the handshake. Non-nil on any process that serves clients.
func (c *Process) UDPKeyRegistry() *pkgnet.UDPKeyRegistry { return c.udpKeyRegistry() }

// handleUDPKeyIssue serves POST /auth/udp-key.
//
// Every rejection is deliberately coarse — 401 with no body detail — so the
// route cannot be used to probe which cookies are valid.
func (c *Process) handleUDPKeyIssue(w http.ResponseWriter, r *http.Request) {
	// Without a resolver there is no way to know who is asking, and issuing a
	// key to an unauthenticated caller would hand out a forgery credential.
	// AnonymousAuth deliberately does NOT apply here: it exists so examples can
	// run without login plumbing, not so anyone can mint transport identity.
	if c.cfg.AuthResolver == nil {
		http.Error(w, "udp key issuance unavailable", http.StatusNotFound)
		return
	}

	// The response body carries a bearer secret, so refuse to put it on a
	// plaintext listener.
	//
	// The escape hatch is --dev-insecure-cookie rather than a second flag or a
	// loopback heuristic. One switch, because it is one decision: that listener
	// already carries the session cookie in the clear, and the session cookie
	// is strictly more powerful than a 5-minute transport key. A loopback check
	// would also be useless here — HTTPPort renders as ":8080", and
	// isLoopbackBind treats an empty host as all-interfaces, so it would refuse
	// in every dev flow while proving nothing about production.
	if tlsCfg, _ := c.httpTLSConfig(); tlsCfg == nil && !c.cfg.DevInsecureCookie {
		c.Log.Log(CatNetConn, "udp-key: refusing to issue over a plaintext listener — set --tls-cert/--tls-key, terminate TLS at a proxy, or pass --dev-insecure-cookie for local HTTP development")
		http.Error(w, "udp key issuance requires TLS", http.StatusForbidden)
		return
	}

	opts := c.cfg.AuthHTTPOpts
	if opts.CookieName == "" {
		opts = auth.DefaultHTTPOpts()
	}
	cookie, err := r.Cookie(opts.CookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	resolved, err := c.cfg.AuthResolver.Resolve(r.Context(), cookie.Value)
	if err != nil {
		c.Log.Log(CatNetConn, "udp-key: cookie validate failed: %v", err)
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}

	now := time.Now()
	id, entry, err := c.udpKeyRegistry().Issue(resolved.UserID.String(), resolved.Username, now)
	if err != nil {
		// A full table is a capacity condition, not a client error.
		c.Log.Log(CatNetConn, "udp-key: issue failed for user=%s: %v", resolved.Username, err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}

	var idBytes [8]byte
	for i := range 8 {
		idBytes[7-i] = byte(uint64(id) >> (8 * i))
	}
	resp := UDPKeyResponse{
		KeyID:       hex.EncodeToString(idBytes[:]),
		Key:         base64.StdEncoding.EncodeToString(entry.Key[:]),
		ExpiresAtMs: entry.ExpiresAt.UnixMilli(),
	}

	// The key must never be cached by a proxy or the browser.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
