package admin

import (
	"net/http"
	"strings"
	"time"
)

// cookieName is the admin session cookie. Distinct from the game-auth cookie
// so the two domains don't collide on shared hosts.
const cookieName = "admin_session"

// CookieOpts controls cookie attributes for both writes and reads.
type CookieOpts struct {
	Domain   string // empty = host-only
	Path     string // default "/admin"
	Secure   bool   // forced true unless dev-loopback
	SameSite http.SameSite
}

func defaultCookieOpts() CookieOpts {
	return CookieOpts{
		Path:     "/admin",
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// devLoopbackRelaxed returns o with Secure=false when bind is loopback.
func devLoopbackRelaxed(o CookieOpts, bind string) CookieOpts {
	if isLoopbackBind(bind) {
		o.Secure = false
	}
	return o
}

func isLoopbackBind(bind string) bool {
	host := bind
	if i := strings.LastIndex(bind, ":"); i >= 0 {
		host = bind[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func setSessionCookie(w http.ResponseWriter, opts CookieOpts, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Domain:   opts.Domain,
		Path:     opts.Path,
		Expires:  time.Now().Add(ttl),
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: opts.SameSite,
	})
}

func clearSessionCookie(w http.ResponseWriter, opts CookieOpts) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Domain:   opts.Domain,
		Path:     opts.Path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   opts.Secure,
		SameSite: opts.SameSite,
	})
}

func readSessionToken(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
