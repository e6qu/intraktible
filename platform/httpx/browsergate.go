// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"strings"

	"github.com/e6qu/intraktible/platform/auth"
)

// SignInEntry is where an anonymous browser is sent to obtain a session. Exactly
// one is in force per deployment, decided at the composition root from the
// configured providers — never negotiated per request.
type SignInEntry struct {
	// Path is the local target an anonymous browser is redirected to.
	Path string
	// Exempt lists additional path prefixes that must stay reachable without a
	// session, beyond the always-public set (the sign-in machinery itself).
	Exempt []string
}

// BrowserGate fails closed for browser navigations to the embedded UI: an
// unauthenticated browser is redirected to the deployment's sign-in entry point
// instead of being handed the SPA shell.
//
// Why this must be server-side. The SPA shell renders for anyone, discovers it is
// unauthenticated only after its own JS runs, and only then redirects. Anything
// inspecting the server's response — an SSO validator sampling at
// domcontentloaded, a crawler, a curl — sees 200 and a page, i.e. an application
// that does not fail closed. Doing the check before a byte of HTML is written
// makes the wire behavior match the intent.
//
// It deliberately gates only what a browser navigates to. API clients under /v1
// are authenticated by the /v1 chain and must keep receiving 401 with a JSON body
// rather than a redirect to an HTML page, and non-navigation requests (an asset
// fetch, an XHR) are left alone so the sign-in page itself can load.
func BrowserGate(next http.Handler, sessions auth.SessionStore, entry SignInEntry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isBrowserNavigation(r) || publicBrowserPath(r.URL.Path, entry) || hasSession(r, sessions) {
			next.ServeHTTP(w, r)
			return
		}
		// 303 rather than 302: the response to this GET is a different resource, and
		// 303 is the status that says so without inviting a method rewrite.
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, entry.Path, http.StatusSeeOther)
	})
}

// isBrowserNavigation reports whether r is a top-level document request — the only
// thing that should ever be answered with a redirect to a sign-in page. It reads
// Sec-Fetch-Mode where the browser sends it (every current engine does) and falls
// back to the Accept header for clients that do not.
func isBrowserNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return strings.EqualFold(mode, "navigate")
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// publicBrowserPath reports whether p must stay reachable without a session. The
// sign-in entry point itself heads the list — gating it would make the redirect a
// loop — alongside the SSO endpoints, the operational probes, and the published
// API contract.
func publicBrowserPath(p string, entry SignInEntry) bool {
	if p == entry.Path || strings.HasPrefix(p, entry.Path+"/") {
		return true
	}
	for _, prefix := range entry.Exempt {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	for _, prefix := range publicBrowserPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// publicBrowserPrefixes are reachable without a session in every deployment: the
// sign-in machinery, the operational probes, and the API contract.
var publicBrowserPrefixes = []string{
	"/v1/auth",
	"/auth",
	"/healthz",
	"/readyz",
	"/version",
	"/metrics",
	"/openapi.json",
	"/docs",
}

// hasSession reports whether r carries a session cookie that currently resolves.
// An expired or revoked cookie is no session at all, which is what sends a
// returning user back through sign-in rather than into a shell that will 401 on
// its first fetch.
func hasSession(r *http.Request, sessions auth.SessionStore) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	_, _, _, _, ok := sessions.Resolve(cookie.Value)
	return ok
}
