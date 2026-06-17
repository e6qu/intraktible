// SPDX-License-Identifier: AGPL-3.0-or-later

// Package connectors is the Context Layer's connector subsystem: a projector that
// folds connector definitions + fetch history out of the event stream, the Connect
// interface with reference implementations (an arbitrary-HTTP connector and a
// deterministic mock bureau), and the read-side that invokes a defined connector.
// The invocation is an effect performed by the shell and recorded as an event, so
// the stored response — never a re-fetch — is what replay reads.
package connectors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free); registers "sqlite"

	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/context-layer/events"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// Collections held by this read model.
const (
	CollectionConnectors = "context_connectors"
	CollectionFetches    = "context_connector_fetches"
)

// fetchTimeout bounds an HTTP connector call.
const fetchTimeout = 10 * time.Second

// ConnectorView is the materialized read model for one connector definition.
type ConnectorView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Config    json.RawMessage `json:"config,omitempty"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// FetchView is one recorded connector invocation.
type FetchView struct {
	Org       string          `json:"org"`
	Workspace string          `json:"workspace"`
	FetchID   string          `json:"fetch_id"`
	Connector string          `json:"connector"`
	Params    json.RawMessage `json:"params,omitempty"`
	Response  json.RawMessage `json:"response"`
	Seq       uint64          `json:"seq"`
	At        time.Time       `json:"at"`
}

// Connector fetches external data for a set of params, returning a JSON document.
type Connector interface {
	Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// EgressPolicy governs which network targets the HTTP connector may reach. The
// HTTP connector dials an operator-configured URL (the Custom Connect feature),
// so without controls it is a server-side request forgery (SSRF) vector: a
// malicious or mistaken config could make the server fetch internal metadata
// endpoints (169.254.169.254), localhost admin ports, or RFC1918 hosts.
//
// The zero value is fail-safe: it blocks loopback, private, link-local,
// unspecified, and multicast targets. AllowPrivate is the operator's explicit
// opt-in for deployments whose connectors legitimately reach internal hosts.
type EgressPolicy struct {
	// AllowPrivate permits dialing loopback/private/link-local addresses. Default
	// false. Set via INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE at the composition root.
	AllowPrivate bool
}

// control is a net.Dialer Control hook: it runs after DNS resolution with the
// concrete IP about to be dialed, so it also defeats DNS-rebinding (a name that
// resolves to a public IP on validation but a private one at connect time).
func (p EgressPolicy) control(_, address string, _ syscall.RawConn) error {
	if p.AllowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("context-layer: http connector egress: parse address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("context-layer: http connector egress: unresolved address %q", address)
	}
	if blockedIP(ip) {
		return fmt.Errorf("context-layer: http connector blocked egress to %s "+
			"(loopback/private/link-local); set INTRAKTIBLE_CONNECTOR_ALLOW_PRIVATE to allow", ip)
	}
	return nil
}

// Client builds an http.Client that enforces this egress policy at dial time (so
// it guards every redirect hop and resists DNS rebinding). It is the reusable
// SSRF-safe client for any outbound caller (HTTP connectors, webhook delivery).
func (p EgressPolicy) Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: p.control}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: dialer.DialContext},
	}
}

// blockedIP reports whether ip is in a range the default policy refuses to dial.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

// Projector folds connector definitions + fetches into read models.
type Projector struct{}

// Name identifies the projector.
func (Projector) Name() string { return "context_connectors" }

// Collections lists the store collections this projector owns.
func (Projector) Collections() []string { return []string{CollectionConnectors, CollectionFetches} }

// Apply maintains the connector definition + fetch-history read models.
func (Projector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	switch e.Type {
	case events.TypeConnectorDefined:
		return applyDefined(ctx, e, s)
	case events.TypeConnectorFetched:
		return applyFetched(ctx, e, s)
	default:
		return nil
	}
}

func applyDefined(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ConnectorDefined
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("context-layer: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	v := ConnectorView{
		Org: e.Org, Workspace: e.Workspace,
		Name: p.Name, Type: p.Type, Config: p.Config, UpdatedAt: e.Time,
	}
	return store.PutDoc(ctx, s, CollectionConnectors, store.Key(e.Org, e.Workspace, p.Name), v)
}

func applyFetched(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	var p events.ConnectorFetched
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("context-layer: decode %s seq %d: %w", e.Type, e.Seq, err)
	}
	v := FetchView{
		Org: e.Org, Workspace: e.Workspace,
		FetchID: p.FetchID, Connector: p.Connector, Params: p.Params,
		Response: p.Response, Seq: e.Seq, At: p.At,
	}
	// Key by connector + zero-padded seq so a connector's fetches list in order.
	key := store.Key(e.Org, e.Workspace, fmt.Sprintf("%s/%020d", p.Connector, e.Seq))
	return store.PutDoc(ctx, s, CollectionFetches, key, v)
}

// Read returns one connector definition for id's tenant.
func Read(ctx context.Context, s store.Store, id identity.Identity, name string) (ConnectorView, bool, error) {
	return store.GetDoc[ConnectorView](ctx, s, CollectionConnectors, store.Key(id.Org, id.Workspace, name))
}

// List returns the tenant's connector definitions, optionally filtered by type.
func List(ctx context.Context, s store.Store, id identity.Identity, connType string) ([]ConnectorView, error) {
	return store.QueryDocs(ctx, s, CollectionConnectors, store.Key(id.Org, id.Workspace, ""),
		func(c ConnectorView) bool { return connType == "" || c.Type == connType },
		func(a, b ConnectorView) bool { return a.Name < b.Name })
}

// redactKeys are config field names whose values are credentials and must never
// leave the server. The connector fetch path reads the real config via Read; the
// HTTP boundary serves redacted copies (see Redacted), so the UI/API never see
// secrets even though the stored projection keeps them.
var redactKeys = map[string]bool{
	"dsn": true, "password": true, "passwd": true, "pwd": true,
	"secret": true, "secret_key": true, "token": true, "access_token": true,
	"api_key": true, "apikey": true, "access_key": true, "private_key": true,
	"authorization": true, "auth": true, "credential": true, "credentials": true,
}

// RedactConfig returns a copy of a connector config JSON with credential values
// masked. Non-object / unparseable config is returned unchanged.
func RedactConfig(config json.RawMessage) json.RawMessage {
	if len(config) == 0 {
		return config
	}
	var v any
	if err := json.Unmarshal(config, &v); err != nil {
		return config
	}
	redacted := redactValue(v)
	out, err := json.Marshal(redacted)
	if err != nil {
		return config
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if redactKeys[strings.ToLower(k)] {
				t[k] = "[redacted]"
			} else {
				t[k] = redactValue(val)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	default:
		return v
	}
}

// Redacted returns a copy of the view with its config credentials masked — the
// safe shape to serve over HTTP.
func (c ConnectorView) Redacted() ConnectorView {
	c.Config = RedactConfig(c.Config)
	return c
}

// ListFetches returns a connector's recorded invocations, newest first.
func ListFetches(ctx context.Context, s store.Store, id identity.Identity, name string) ([]FetchView, error) {
	all, err := store.ListDocs[FetchView](ctx, s, CollectionFetches, store.Key(id.Org, id.Workspace, name+"/"))
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Seq > all[j].Seq })
	return all, nil
}

// Invoke looks up a connector definition, builds the connector, and fetches —
// performing the external effect. Recording the result is the caller's job. It
// uses the fail-safe default egress policy; use InvokeWith for a custom one.
func Invoke(ctx context.Context, s store.Store, id identity.Identity, name string, params json.RawMessage) (json.RawMessage, error) {
	return InvokeWith(ctx, s, id, name, params, EgressPolicy{})
}

// InvokeWith is Invoke with an explicit egress policy for the HTTP connector.
func InvokeWith(ctx context.Context, s store.Store, id identity.Identity, name string, params json.RawMessage, egress EgressPolicy) (json.RawMessage, error) {
	def, ok, err := Read(ctx, s, id, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("context-layer: unknown connector %q", name)
	}
	c, err := build(def, egress)
	if err != nil {
		return nil, err
	}
	return c.Fetch(ctx, params)
}

// Provider adapts the connector subsystem to a name+params→response lookup,
// suitable as a decision-engine connector source (it satisfies that engine's
// ConnectorProvider port structurally, without this package importing it). The
// fetch is performed but not recorded here — the decision records the response in
// its own event stream (in DecisionStarted's data and the Connect node's output).
type Provider struct {
	Store  store.Store
	Egress EgressPolicy
}

// Fetch invokes the named connector with params under the provider's egress policy.
func (p Provider) Fetch(ctx context.Context, id identity.Identity, connector string, params json.RawMessage) (json.RawMessage, error) {
	return InvokeWith(ctx, p.Store, id, connector, params, p.Egress)
}

// build constructs a Connector from its definition.
func build(def ConnectorView, egress EgressPolicy) (Connector, error) {
	switch def.Type {
	case domain.ConnectorHTTP:
		return newHTTP(def.Config, egress)
	case domain.ConnectorSQL:
		return newSQL(def.Config)
	case domain.ConnectorMockBureau:
		return mockBureau{}, nil
	default:
		return nil, fmt.Errorf("context-layer: connector %q has unsupported type %q", def.Name, def.Type)
	}
}

// --- HTTP connector ---

type httpConfig struct {
	URL    string `json:"url"`
	Method string `json:"method"`
}

type httpConnector struct {
	url    string
	method string
	client *http.Client
}

func newHTTP(config json.RawMessage, egress EgressPolicy) (httpConnector, error) {
	var cfg httpConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return httpConnector{}, fmt.Errorf("context-layer: http connector config: %w", err)
		}
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return httpConnector{}, fmt.Errorf("context-layer: http connector needs an http(s) url, got %q", cfg.URL)
	}
	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}
	// The egress policy is enforced at dial time (after DNS resolution) so it
	// guards every redirect hop and resists DNS rebinding.
	return httpConnector{url: cfg.URL, method: method, client: egress.Client(fetchTimeout)}, nil
}

// Fetch calls the configured endpoint (sending params as the JSON body for
// non-GET methods) and returns the JSON response, failing loudly on a non-2xx
// status or a non-JSON body.
func (h httpConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var body io.Reader
	if h.method != http.MethodGet && len(params) > 0 {
		body = bytes.NewReader(params)
	}
	// The URL is operator-configured per connector; calling it is the feature.
	req, err := http.NewRequestWithContext(ctx, h.method, h.url, body) // #nosec G107
	if err != nil {
		return nil, fmt.Errorf("context-layer: http connector request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("context-layer: http connector fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("context-layer: http connector read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("context-layer: http connector status %d", resp.StatusCode)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("context-layer: http connector returned non-JSON body")
	}
	return json.RawMessage(b), nil
}

// --- SQL connector ---

// maxSQLRows bounds how many rows a SQL connector returns, so a broad query can
// never blow up memory or the recorded event.
const maxSQLRows = 1000

type sqlConfig struct {
	Driver string   `json:"driver"` // database/sql driver name; only "sqlite" is built in
	DSN    string   `json:"dsn"`    // driver-specific data source name
	Query  string   `json:"query"`  // a SELECT with named placeholders (:name)
	Args   []string `json:"args"`   // param names bound positionally from the params object
}

type sqlConnector struct {
	cfg sqlConfig
}

func newSQL(config json.RawMessage) (sqlConnector, error) {
	var cfg sqlConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return sqlConnector{}, fmt.Errorf("context-layer: sql connector config: %w", err)
		}
	}
	if cfg.Driver == "" {
		cfg.Driver = "sqlite"
	}
	if cfg.Driver != "sqlite" {
		// Only the pure-Go sqlite driver is compiled in; other drivers need a build
		// that imports them. Fail loudly rather than open a nil driver.
		return sqlConnector{}, fmt.Errorf("context-layer: sql connector driver %q is not available (only \"sqlite\" is built in)", cfg.Driver)
	}
	if cfg.DSN == "" || cfg.Query == "" {
		return sqlConnector{}, fmt.Errorf("context-layer: sql connector needs a dsn and a query")
	}
	return sqlConnector{cfg: cfg}, nil
}

// Fetch opens the configured database, runs the parameterized query (binding the
// declared args from the params object as values — never string-interpolated, so
// caller params cannot inject SQL), and returns {"rows": [...]} as JSON.
func (c sqlConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	db, err := sql.Open(c.cfg.Driver, c.cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector open: %w", err)
	}
	defer func() { _ = db.Close() }()

	args, err := bindArgs(c.cfg.Args, params)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, c.cfg.Query, args...)
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(map[string]any{"rows": out})
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector marshal: %w", err)
	}
	return b, nil
}

// bindArgs maps each declared arg name to a named query parameter, reading its
// value from the params object (a missing name binds to nil).
func bindArgs(names []string, params json.RawMessage) ([]any, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var p map[string]any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("context-layer: sql connector params: %w", err)
		}
	}
	args := make([]any, 0, len(names))
	for _, name := range names {
		args = append(args, sql.Named(name, p[name]))
	}
	return args, nil
}

// scanRows reads up to maxSQLRows rows into a slice of column→value maps.
func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector columns: %w", err)
	}
	var out []map[string]any
	for rows.Next() {
		if len(out) >= maxSQLRows {
			return nil, fmt.Errorf("context-layer: sql connector query returned more than %d rows", maxSQLRows)
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("context-layer: sql connector scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			// []byte (text/blob) decodes to a JSON string, not a base64 blob.
			if b, ok := cells[i].([]byte); ok {
				row[name] = string(b)
			} else {
				row[name] = cells[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("context-layer: sql connector rows: %w", err)
	}
	return out, nil
}

// --- Mock bureau connector ---

// mockBureau is a deterministic reference connector: it derives a stable risk
// score (and a sanctioned flag) from the params' "subject" (or the whole params
// blob), so flows can be built and tested without any external dependency.
type mockBureau struct{}

func (mockBureau) Fetch(_ context.Context, params json.RawMessage) (json.RawMessage, error) {
	subject := string(params)
	var p struct {
		Subject string `json:"subject"`
	}
	if json.Valid(params) {
		_ = json.Unmarshal(params, &p)
		if p.Subject != "" {
			subject = p.Subject
		}
	}
	sum := sha256.Sum256([]byte(subject))
	score := int(binary.BigEndian.Uint32(sum[:4]) % 101) // 0..100, deterministic
	out := map[string]any{
		"subject":    subject,
		"risk_score": score,
		"sanctioned": score >= 90,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("context-layer: mock bureau marshal: %w", err)
	}
	return b, nil
}
