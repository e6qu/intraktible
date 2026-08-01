// SPDX-License-Identifier: AGPL-3.0-or-later

package httpx

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

// IPAllowlist is a middleware that admits only requests from the configured
// CIDR ranges. An empty allowlist admits everyone (the default). It is the
// deployment-time network control for restricting /v1 access to known sources
// (a VPC, a trusted proxy, or an API gateway) without relying solely on the
// application-layer auth. The client IP is read from X-Forwarded-For when the
// trusted-proxy flag is set (see ConfigureCookieSecurity), or from the direct
// TCP connection otherwise.
type IPAllowlist struct {
	cidrs []*net.IPNet
}

// ParseIPAllowlist parses a comma-separated CIDR list (e.g. "10.0.0.0/8,
// 192.168.1.0/24"). A single bare IP is treated as /32. An empty string
// returns an allowlist that admits everyone.
func ParseIPAllowlist(value string) (*IPAllowlist, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return &IPAllowlist{}, nil
	}
	var cidrs []*net.IPNet
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if !strings.Contains(entry, "/") {
			entry += "/32"
		}
		_, ipNet, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, errors.New("httpx: invalid CIDR " + entry + ": " + err.Error())
		}
		cidrs = append(cidrs, ipNet)
	}
	return &IPAllowlist{cidrs: cidrs}, nil
}

// Middleware wraps next so that only requests from the allowlisted CIDRs reach
// it. When no CIDRs are configured, it passes through (the default open state).
func (a *IPAllowlist) Middleware(next http.Handler) http.Handler {
	if a == nil || len(a.cidrs) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(clientIP(r))
		if ip == nil {
			Error(w, http.StatusForbidden, errors.New("httpx: could not parse client IP"))
			return
		}
		allowed := false
		for _, cidr := range a.cidrs {
			if cidr.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			Error(w, http.StatusForbidden, errors.New("httpx: client IP "+ip.String()+" is not allowlisted"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
