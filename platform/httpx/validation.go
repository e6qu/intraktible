// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/e6qu/intraktible/platform/auth"
)

const validationSignedOutPath = "/v1/auth/signed-out"

var validationPage = template.Must(template.New("validation").Parse(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="color-scheme" content="light dark">
    <title>Intraktible account validation</title>
    <style>
      :root { font-family: system-ui, sans-serif; color-scheme: light dark; }
      body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: Canvas; color: CanvasText; }
      main { width: min(38rem, calc(100% - 2rem)); padding: 2rem; border: 1px solid color-mix(in srgb, CanvasText 24%, transparent); border-radius: 1rem; }
      h1 { margin-top: 0; }
      dl { display: grid; gap: .75rem; }
      dl div { display: grid; grid-template-columns: 8rem 1fr; gap: 1rem; }
      dt { color: color-mix(in srgb, CanvasText 68%, transparent); }
      dd { margin: 0; overflow-wrap: anywhere; }
      button { border: 0; border-radius: .6rem; padding: .7rem 1rem; font: inherit; font-weight: 700; color: white; background: #6d28d9; cursor: pointer; }
      button:focus-visible { outline: 3px solid #22d3ee; outline-offset: 3px; }
      button:disabled { cursor: wait; opacity: .7; }
    </style>
  </head>
  <body>
    <main aria-labelledby="validation-title">
      <p>Authenticated application session</p>
      <h1 id="validation-title">Intraktible account</h1>
      <dl aria-label="Current account details">
        <div><dt>Username</dt><dd data-testid="validation-username">{{.Username}}</dd></div>
        <div><dt>Email</dt><dd data-testid="validation-email">{{.Email}}</dd></div>
        <div><dt>Role</dt><dd data-testid="validation-role">{{.Role}}</dd></div>
        <div><dt>Release</dt><dd data-testid="validation-release">{{.Revision}}</dd></div>
      </dl>
      <form id="validation-sign-out" action="/v1/logout" method="post">
        <button type="submit">Sign out</button>
      </form>
      <p id="validation-sign-out-status" role="status" aria-live="polite"></p>
    </main>
    <script>
      document.getElementById('validation-sign-out').addEventListener('submit', async (event) => {
        event.preventDefault();
        const button = event.currentTarget.querySelector('button');
        const status = document.getElementById('validation-sign-out-status');
        button.disabled = true;
        status.textContent = 'Signing out…';
        try {
          const response = await fetch('/v1/logout', {
            method: 'POST', credentials: 'same-origin',
            headers: { 'Accept': 'application/json', 'X-Requested-With': 'intraktible' }
          });
          if (!response.ok) throw new Error('logout failed');
          const result = await response.json();
          window.location.assign(result.logout_url || '` + validationSignedOutPath + `');
        } catch (_) {
          button.disabled = false;
          status.textContent = 'Sign out failed. Your session is still active; try again.';
        }
      });
    </script>
  </body>
</html>`))

// ValidationHandler exposes the app-owned, deployment-neutral browser contract
// used to verify a real OpenID Connect session. It accepts only an ordinary
// Intraktible session cookie: validator credentials, bearer tokens, and API keys
// are not authentication mechanisms for this endpoint.
func ValidationHandler(sessions auth.SessionStore) http.HandlerFunc {
	revision, _, _ := applicationBuildMetadata()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Redirect(w, r, validationSignedOutPath, http.StatusSeeOther)
			return
		}
		id, role, _, ok := sessions.Resolve(cookie.Value)
		if !ok {
			http.Redirect(w, r, validationSignedOutPath, http.StatusSeeOther)
			return
		}
		username, email := strings.TrimSpace(id.Username), strings.TrimSpace(id.Email)
		if username == "" || email == "" || !validImmutableReleaseRevision(revision) {
			Error(w, http.StatusServiceUnavailable, errors.New("authenticated validation metadata is unavailable"))
			return
		}
		normalizedRole := "developer"
		if auth.ParseRole(string(role)) == auth.RoleAdmin {
			normalizedRole = "admin"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := validationPage.Execute(w, map[string]string{
			"Username": username,
			"Email":    email,
			"Role":     normalizedRole,
			"Revision": revision,
		}); err != nil {
			panic("httpx: render validation page: " + err.Error())
		}
	}
}
