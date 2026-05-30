package auth

import (
	"net/http"
	"testing"
)

func TestCrossSiteHTTPOpts(t *testing.T) {
	in := DefaultHTTPOpts() // SameSite=Strict, Secure=true
	out := CrossSiteHTTPOpts(in)

	if out.SameSite != http.SameSiteNoneMode {
		t.Fatalf("SameSite = %v, want None", out.SameSite)
	}
	if !out.CookieSecure {
		t.Fatalf("CookieSecure = false, want true (SameSite=None requires Secure)")
	}
	// Unrelated fields preserved.
	if out.CookieName != in.CookieName {
		t.Fatalf("CookieName changed: %q != %q", out.CookieName, in.CookieName)
	}
	// The None mode must survive effective() normalization (which only
	// fills SameSite when it's the zero/default value), else it would be
	// silently reset to the Strict default at cookie-set time.
	if got := out.effective().SameSite; got != http.SameSiteNoneMode {
		t.Fatalf("effective().SameSite = %v, want None (survives normalization)", got)
	}
}

func TestCrossSiteHTTPOpts_ForcesSecureOverDevInsecure(t *testing.T) {
	in := DefaultHTTPOpts()
	in.CookieSecure = false // as --dev-insecure-cookie would set it
	out := CrossSiteHTTPOpts(in)
	if !out.CookieSecure {
		t.Fatalf("cross-site mode must force Secure=true even over a dev-insecure base")
	}
}
