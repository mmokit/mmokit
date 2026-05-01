package auth

import (
	"net/http"
	"time"
)

// HTTPOpts configures the cookie shape and HTTP endpoint behavior.
// Defaults match the spec: HttpOnly=true, Secure=true, SameSite=Strict,
// Path="/", Max-Age=SessionTTL.
type HTTPOpts struct {
	// CookieName is the cookie key. Defaults to "mmokit-session".
	CookieName string
	// CookiePath is the cookie path scope. Defaults to "/".
	// Must include both the auth endpoints and the WS upgrade path.
	CookiePath string
	// CookieDomain is the cookie domain. Empty = single-origin
	// (Domain attribute omitted, scoped to the request host).
	CookieDomain string
	// CookieSecure controls the Secure flag. Default true; flip to
	// false only for plain-HTTP local dev (--dev-insecure-cookie).
	CookieSecure bool
	// SameSite is the SameSite flag. Defaults to http.SameSiteStrictMode.
	SameSite http.SameSite
}

// DefaultHTTPOpts returns production-safe cookie defaults.
func DefaultHTTPOpts() HTTPOpts {
	return HTTPOpts{
		CookieName:   "mmokit-session",
		CookiePath:   "/",
		CookieSecure: true,
		SameSite:     http.SameSiteStrictMode,
	}
}

// effective returns o with empty fields filled from DefaultHTTPOpts.
// Used so callers can override only the fields they care about.
//
// CookieSecure=false is a meaningful override (dev mode), so this
// helper does not auto-fill it from default. Callers must set it
// explicitly or rely on the zero value (false). The facade's
// RegisterAuthService fills the entire HTTPOpts from DefaultHTTPOpts
// when the caller didn't touch HTTPOpts at all.
func (o HTTPOpts) effective() HTTPOpts {
	d := DefaultHTTPOpts()
	if o.CookieName == "" {
		o.CookieName = d.CookieName
	}
	if o.CookiePath == "" {
		o.CookiePath = d.CookiePath
	}
	if o.SameSite == 0 {
		o.SameSite = d.SameSite
	}
	return o
}

// setAuthCookie writes a Set-Cookie header carrying the session token.
// ttl mirrors auth.ServiceOpts.SessionTTL.
func setAuthCookie(w http.ResponseWriter, opts HTTPOpts, token string, ttl time.Duration) {
	o := opts.effective()
	http.SetCookie(w, &http.Cookie{
		Name:     o.CookieName,
		Value:    token,
		Path:     o.CookiePath,
		Domain:   o.CookieDomain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   o.CookieSecure,
		SameSite: o.SameSite,
	})
}

// clearAuthCookie writes a Set-Cookie header that immediately expires
// the session cookie on the client.
func clearAuthCookie(w http.ResponseWriter, opts HTTPOpts) {
	o := opts.effective()
	http.SetCookie(w, &http.Cookie{
		Name:     o.CookieName,
		Value:    "",
		Path:     o.CookiePath,
		Domain:   o.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   o.CookieSecure,
		SameSite: o.SameSite,
	})
}

// readAuthCookie returns the session token from the request, or "" if
// the cookie is absent. Used by the gateway's WS-upgrade handler.
func readAuthCookie(r *http.Request, opts HTTPOpts) string {
	o := opts.effective()
	c, err := r.Cookie(o.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
