// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"net/http"
	"strconv"
)

// Backpressure sheds read load when the projection lag (event-log head minus
// applied seq) exceeds maxLag, so clients back off instead of consuming
// increasingly stale read models while a replica catches up. A threshold of 0
// disables the gate (the default). The gate applies only to safe (read) methods:
// writes must always be admitted (they advance the log, and blocking them would
// deadlock a recovering replica's catch-up). It answers 503 with Retry-After.
func Backpressure(
	applied, head func() uint64,
	maxLag uint64,
) Middleware {
	if maxLag == 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				h, a := head(), applied()
				if h > a && h-a > maxLag {
					w.Header().Set("Retry-After", "2")
					JSON(w, http.StatusServiceUnavailable, map[string]any{
						"status": "lagging", "applied": a, "head": h,
						"lag": h - a, "max_lag": maxLag,
						"error": "projection is more than " + strconv.FormatUint(maxLag, 10) +
							" events behind; backing off writes is safe to retry shortly",
					})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
