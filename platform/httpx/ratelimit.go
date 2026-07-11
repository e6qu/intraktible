// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// rateLimiter is a per-client-IP token-bucket limiter. It fronts the credential-
// sensitive endpoints (login, SSO callbacks, the SCIM bearer surface) so an
// attacker cannot brute-force keys/tokens or stuff credentials unthrottled, and it
// caps a single source's request rate as a coarse DoS backstop. Limiters are kept
// per IP with a last-seen timestamp and swept so the map can't grow unboundedly.
type rateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientLimiter
	rps      rate.Limit
	burst    int
	lastGC   time.Time
	nowFn    func() time.Time
	idleEvit time.Duration
}

type clientLimiter struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimit returns middleware that allows `rps` requests per second per client
// IP with a token-bucket `burst`. Over the limit → 429 with a Retry-After.
func NewRateLimit(rps float64, burst int) func(http.HandlerFunc) http.HandlerFunc {
	rl := &rateLimiter{
		clients:  make(map[string]*clientLimiter),
		rps:      rate.Limit(rps),
		burst:    burst,
		nowFn:    time.Now,
		idleEvit: 10 * time.Minute,
	}
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !rl.allow(clientIP(r)) {
				w.Header().Set("Retry-After", "1")
				JSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded; retry shortly"})
				return
			}
			next(w, r)
		}
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.nowFn()
	if now.Sub(rl.lastGC) > rl.idleEvit {
		for k, c := range rl.clients {
			if now.Sub(c.seen) > rl.idleEvit {
				delete(rl.clients, k)
			}
		}
		rl.lastGC = now
	}
	c, ok := rl.clients[ip]
	if !ok {
		c = &clientLimiter{lim: rate.NewLimiter(rl.rps, rl.burst)}
		rl.clients[ip] = c
	}
	c.seen = now
	return c.lim.Allow()
}

// clientIP is the request's source address. It honors X-Forwarded-For only when a
// trusted terminating proxy is configured (the same switch that governs
// Secure-cookie detection), since the header is otherwise client-forgeable — an
// attacker could rotate it to defeat the limiter.
func clientIP(r *http.Request) string {
	if trustForwardedProto {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
