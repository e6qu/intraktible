// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"strings"
)

// Cookie Secure and HSTS are conditioned on this policy, configured once at boot.
// The default (both false) is right for local plaintext dev: the session cookie
// still works and no false HSTS is asserted. In production the composition root
// turns these on so cookies are Secure and HSTS is emitted even when TLS terminates
// at a proxy (where the app sees plaintext HTTP and r.TLS is nil).
var (
	forceSecureCookies  bool
	trustForwardedProto bool
)

// ConfigureCookieSecurity sets how Secure/HSTS are decided. forceSecure marks every
// session/flow cookie Secure and always emits HSTS (set it when the deployment is
// only ever reached over HTTPS). trustProxy additionally honors a
// `X-Forwarded-Proto: https` header from a trusted terminating proxy — only enable
// it when such a proxy is actually in front, since the header is client-forgeable.
func ConfigureCookieSecurity(forceSecure, trustProxy bool) {
	forceSecureCookies = forceSecure
	trustForwardedProto = trustProxy
}

// requestIsSecure reports whether the browser-facing connection is HTTPS, so a
// cookie can be marked Secure (and HSTS asserted) correctly both on a direct TLS
// listener and behind a configured TLS-terminating proxy.
func requestIsSecure(r *http.Request) bool {
	if forceSecureCookies || r.TLS != nil {
		return true
	}
	return trustForwardedProto && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
