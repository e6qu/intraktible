// SPDX-License-Identifier: AGPL-3.0-or-later

package openapi_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/platform/openapi"
)

// The spec is hand-maintained JSON, so nothing stops a new endpoint from shipping
// undocumented — which is how a dozen integrator-facing routes (resume,
// counterfactual, grants, SLO, deployment scheduling, case actions) came to exist
// with no contract. This test reads the routes the services actually register and
// requires each to appear in the spec, so the two cannot drift apart again.
//
// Routes are read from source rather than from a live mux because http.ServeMux
// does not enumerate its patterns.

// internalRoutes are registered but deliberately absent from the public data-plane
// contract. Each needs a reason, so "undocumented" is a decision and not an
// oversight.
var internalRoutes = map[string]string{
	"GET /openapi.json":     "the contract itself",
	"GET /docs":             "the reference page that renders the contract",
	"GET /healthz":          "liveness probe, not a data-plane call",
	"GET /readyz":           "readiness probe, not a data-plane call",
	"GET /metrics":          "Prometheus scrape endpoint",
	"GET /v1/me":            "session introspection for the console",
	"POST /v1/login":        "console session exchange, not an integrator call",
	"POST /v1/logout":       "console session exchange, not an integrator call",
	"GET /v1/auth/sso":      "browser SSO redirect",
	"GET /v1/auth/callback": "browser SSO redirect",

	// SCIM 2.0 is its own IETF standard (RFC 7643/7644) with a fixed wire contract;
	// an identity provider drives it for user/group provisioning, not an integrator
	// against this product's data plane — the RFC is its spec, not this document.
	"GET /scim/v2/Users":          "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"POST /scim/v2/Users":         "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"GET /scim/v2/Users/{id}":     "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"PATCH /scim/v2/Users/{id}":   "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"DELETE /scim/v2/Users/{id}":  "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"GET /scim/v2/Groups":         "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"POST /scim/v2/Groups":        "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"GET /scim/v2/Groups/{id}":    "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"PUT /scim/v2/Groups/{id}":    "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"PATCH /scim/v2/Groups/{id}":  "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",
	"DELETE /scim/v2/Groups/{id}": "SCIM 2.0 provisioning (RFC 7644), not a data-plane call",

	// Browser SSO redirect / assertion flows (OIDC + SAML): user-agent redirects and
	// IdP callbacks, not JSON API calls an integrator makes with an API key.
	"GET /v1/auth/oidc/providers":                      "browser OIDC discovery for the login page",
	"GET /v1/auth/oidc/{provider}/login":               "browser OIDC redirect flow",
	"GET /v1/auth/oidc/{provider}/callback":            "browser OIDC redirect callback",
	"GET /v1/auth/oidc/{provider}/frontchannel-logout": "OpenID Connect Front-Channel Logout endpoint consumed by the identity provider",
	"POST /v1/auth/oidc/{provider}/backchannel-logout": "OpenID Connect Back-Channel Logout endpoint consumed by the identity provider",
	"GET /v1/auth/signed-out":                          "browser OpenID Connect RP-Initiated Logout landing",
	"GET /v1/auth/signed-out.css":                      "app-local signed-out stylesheet",
	"GET /v1/auth/saml/providers":                      "browser SAML discovery for the login page",
	"GET /v1/auth/saml/{provider}/login":               "browser SAML redirect flow",
	"GET /v1/auth/saml/{provider}/metadata":            "SAML SP metadata document consumed by the IdP",
	"POST /v1/auth/saml/{provider}/acs":                "browser SAML assertion consumer (IdP POST)",

	// Infra / scaffolding surfaces, not part of the integration contract.
	"GET /version":        "build-metadata probe, like /healthz",
	"POST /v1/hello":      "scaffolding slice, not a product endpoint",
	"GET /v1/hello/stats": "scaffolding slice, not a product endpoint",

	// GDPR right-to-erasure / retention: a privacy/compliance control-plane admin
	// surface (data-subject rights), out of scope of the integrator data plane like
	// the RBAC/secrets surfaces this document already excludes.
	"GET /v1/erasure/subjects":                    "privacy/compliance control plane (RTBF admin)",
	"GET /v1/erasure/subjects/{subject}":          "privacy/compliance control plane (RTBF admin)",
	"POST /v1/erasure/subjects/{subject}":         "privacy/compliance control plane (RTBF admin)",
	"POST /v1/erasure/subjects/{subject}/hold":    "privacy/compliance control plane (legal-hold admin)",
	"POST /v1/erasure/subjects/{subject}/release": "privacy/compliance control plane (legal-hold admin)",
	"GET /v1/erasure/holds":                       "privacy/compliance control plane (legal-hold admin)",
	"GET /v1/erasure/retention-policy":            "privacy/compliance control plane (retention admin)",
	"PUT /v1/erasure/retention-policy":            "privacy/compliance control plane (retention admin)",
	"POST /v1/erasure/retention":                  "privacy/compliance control plane (retention admin)",
}

func TestEveryRegisteredRouteIsDocumented(t *testing.T) {
	doc, err := openapi.Parse()
	if err != nil {
		t.Fatal(err)
	}
	documented := make(map[string]bool)
	for path, methods := range doc.Paths {
		for method := range methods {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	routes, err := registeredRoutes(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) < 50 {
		t.Fatalf("only found %d routes — the scanner is not seeing the registrations", len(routes))
	}

	var missing []string
	for _, route := range routes {
		if internalRoutes[route] != "" || documented[route] {
			continue
		}
		missing = append(missing, route)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("%d registered routes are missing from openapi.json:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func TestEveryDocumentedRouteIsRegistered(t *testing.T) {
	doc, err := openapi.Parse()
	if err != nil {
		t.Fatal(err)
	}
	routes, err := registeredRoutes(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool, len(routes))
	for _, route := range routes {
		registered[route] = true
	}

	var phantom []string
	for path, methods := range doc.Paths {
		for method := range methods {
			route := strings.ToUpper(method) + " " + path
			if registered[route] || internalRoutes[route] != "" {
				continue
			}
			phantom = append(phantom, route)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Fatalf("%d documented routes are not registered by any service (a client would 404):\n  %s",
			len(phantom), strings.Join(phantom, "\n  "))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot find the repo root from %s: %v", root, err)
	}
	return root
}

// registeredRoutes collects every "METHOD /path" a service registers, in either of
// the two idioms the codebase uses: mux.HandleFunc("GET /v1/x", h) and the
// httpx.Route{Method: "GET", Pattern: "/v1/x"} table.
func registeredRoutes(root string) ([]string, error) {
	seen := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "web", ".git", "temp", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		return routesInFile(path, seen)
	})
	if err != nil {
		return nil, err
	}
	routes := make([]string, 0, len(seen))
	for route := range seen {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	return routes, nil
}

func routesInFile(path string, seen map[string]bool) error {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if route, ok := handleFuncRoute(node); ok {
				seen[route] = true
			}
		case *ast.CompositeLit:
			if route, ok := routeLiteral(node); ok {
				seen[route] = true
			}
		}
		return true
	})
	return nil
}

// handleFuncRoute matches mux.HandleFunc("GET /v1/flows", h) and Handle(...).
func handleFuncRoute(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") || len(call.Args) == 0 {
		return "", false
	}
	pattern, ok := stringLit(call.Args[0])
	if !ok {
		return "", false
	}
	method, path, found := strings.Cut(pattern, " ")
	if !found || !strings.HasPrefix(path, "/") {
		return "", false // a pattern with no method is not a data-plane route
	}
	return method + " " + path, true
}

// routeLiteral matches httpx.Route{Method: "GET", Pattern: "/v1/agents"}.
func routeLiteral(lit *ast.CompositeLit) (string, bool) {
	var method, pattern string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		value, ok := stringLit(kv.Value)
		if !ok {
			continue
		}
		switch key.Name {
		case "Method":
			method = value
		case "Pattern":
			pattern = value
		}
	}
	if method == "" || !strings.HasPrefix(pattern, "/") {
		return "", false
	}
	return method + " " + pattern, true
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
