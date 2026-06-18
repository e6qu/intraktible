// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

func define(ctx context.Context, t *testing.T, s store.Store, id identity.Identity, v connectors.ConnectorView) {
	t.Helper()
	v.Org, v.Workspace = id.Org, id.Workspace
	if err := store.PutDoc(ctx, s, connectors.CollectionConnectors, store.Key(id.Org, id.Workspace, v.Name), v); err != nil {
		t.Fatal(err)
	}
}

func TestMockBureauDeterministic(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{Name: "bureau", Type: domain.ConnectorMockBureau})

	params := json.RawMessage(`{"subject":"Acme Corp"}`)
	r1, err := connectors.Invoke(ctx, s, id, "bureau", params)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := connectors.Invoke(ctx, s, id, "bureau", params)
	if err != nil {
		t.Fatal(err)
	}
	if string(r1) != string(r2) {
		t.Fatalf("mock bureau not deterministic: %s vs %s", r1, r2)
	}
	var out struct {
		Subject   string `json:"subject"`
		RiskScore int    `json:"risk_score"`
	}
	if err := json.Unmarshal(r1, &out); err != nil {
		t.Fatal(err)
	}
	if out.Subject != "Acme Corp" || out.RiskScore < 0 || out.RiskScore > 100 {
		t.Fatalf("unexpected bureau response: %s", r1)
	}
}

func TestHTTPConnector(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"score":42}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "rest", Type: domain.ConnectorHTTP, Config: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	})

	// httptest binds loopback, which the default egress policy blocks — opt in.
	resp, err := connectors.InvokeWith(ctx, s, id, "rest", nil, connectors.EgressPolicy{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK    bool `json:"ok"`
		Score int  `json:"score"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Score != 42 {
		t.Fatalf("http connector response: %s", resp)
	}
}

func TestHTTPConnectorNon2xxFailsLoudly(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "rest", Type: domain.ConnectorHTTP, Config: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	})
	if _, err := connectors.InvokeWith(ctx, s, id, "rest", nil, connectors.EgressPolicy{AllowPrivate: true}); err == nil {
		t.Fatal("expected a non-2xx fetch to error")
	}
}

func TestHTTPConnectorBlocksLoopbackByDefault(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"secret":true}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "internal", Type: domain.ConnectorHTTP, Config: json.RawMessage(`{"url":"` + srv.URL + `"}`),
	})

	// Default policy (zero value) must refuse to dial a loopback target — SSRF guard.
	if _, err := connectors.Invoke(ctx, s, id, "internal", nil); err == nil {
		t.Fatal("expected the default egress policy to block a loopback fetch")
	}
	// With the operator opt-in, the same fetch succeeds.
	if _, err := connectors.InvokeWith(ctx, s, id, "internal", nil, connectors.EgressPolicy{AllowPrivate: true}); err != nil {
		t.Fatalf("AllowPrivate should permit loopback: %v", err)
	}
}

func TestSQLConnector(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + dir + "/bureau.db"

	// Seed a tiny database the connector will read.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE scores(subject TEXT PRIMARY KEY, risk INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO scores VALUES('acme', 73),('globex', 12)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	cfg := `{"dsn":"` + dsn + `","query":"SELECT subject, risk FROM scores WHERE subject = :subject","args":["subject"]}`
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "scores", Type: domain.ConnectorSQL, Config: json.RawMessage(cfg),
	})

	resp, err := connectors.Invoke(ctx, s, id, "scores", json.RawMessage(`{"subject":"acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Rows []struct {
			Subject string `json:"subject"`
			Risk    int    `json:"risk"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].Subject != "acme" || out.Rows[0].Risk != 73 {
		t.Fatalf("unexpected sql connector response: %s", resp)
	}
}

func TestSQLConnectorRejectsUnknownDriver(t *testing.T) {
	if _, err := connectors.Invoke(context.Background(), seedSQLDef(t, `{"driver":"postgres","dsn":"x","query":"SELECT 1"}`),
		identity.Identity{Org: "demo", Workspace: "main"}, "pg", nil); err == nil {
		t.Fatal("expected an unavailable driver to fail loudly")
	}
}

// seedSQLDef stores a sql connector definition named "pg" and returns the store.
func seedSQLDef(t *testing.T, cfg string) store.Store {
	t.Helper()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main"}
	define(context.Background(), t, s, id, connectors.ConnectorView{
		Name: "pg", Type: domain.ConnectorSQL, Config: json.RawMessage(cfg),
	})
	return s
}

func TestInvokeUnknownConnector(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	if _, err := connectors.Invoke(ctx, s, id, "ghost", nil); err == nil {
		t.Fatal("expected error for unknown connector")
	}
}

func TestRedactConfigMasksCredentials(t *testing.T) {
	cfg := json.RawMessage(`{"driver":"sqlite","dsn":"user:p@ss@host/db","query":"SELECT 1","nested":{"api_key":"sk-123","keep":"ok"}}`)
	out := connectors.RedactConfig(cfg)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["dsn"] != "[redacted]" {
		t.Fatalf("dsn not redacted: %v", m["dsn"])
	}
	// Non-credential fields are preserved.
	if m["driver"] != "sqlite" || m["query"] != "SELECT 1" {
		t.Fatalf("non-secret fields altered: %v", m)
	}
	// Redaction recurses into nested objects.
	nested, _ := m["nested"].(map[string]any)
	if nested["api_key"] != "[redacted]" || nested["keep"] != "ok" {
		t.Fatalf("nested redaction wrong: %v", nested)
	}
}

func TestRedactedViewLeavesStoredConfigIntact(t *testing.T) {
	v := connectors.ConnectorView{Name: "db", Type: "sql", Config: json.RawMessage(`{"dsn":"secret"}`)}
	r := v.Redacted()
	if string(v.Config) != `{"dsn":"secret"}` {
		t.Fatalf("original config mutated: %s", v.Config)
	}
	if !json.Valid(r.Config) || string(r.Config) == string(v.Config) {
		t.Fatalf("redacted copy not masked: %s", r.Config)
	}
}

func TestEncryptDecryptSecrets(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := json.RawMessage(`{"driver":"sqlite","dsn":"file:secret.db","nested":{"api_key":"sk-123","keep":"ok"}}`)
	enc, err := connectors.EncryptSecrets(cfg, kr)
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) == string(cfg) || strings.Contains(string(enc), "file:secret.db") || strings.Contains(string(enc), "sk-123") {
		t.Fatalf("encrypted config leaked plaintext: %s", enc)
	}
	dec, err := connectors.DecryptSecrets(enc, kr)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil {
		t.Fatal(err)
	}
	nested := got["nested"].(map[string]any)
	if got["dsn"] != "file:secret.db" || nested["api_key"] != "sk-123" || nested["keep"] != "ok" {
		t.Fatalf("decrypted config mismatch: %s", dec)
	}
}

func TestKeyringRotation(t *testing.T) {
	oldKey := []byte("0123456789abcdef0123456789abcdef")
	newKey := []byte("fedcba9876543210fedcba9876543210")
	cfg := json.RawMessage(`{"token":"sk-secret","host":"db.example"}`)

	// Seal under the old key alone.
	oldRing, err := connectors.NewKeyring(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := connectors.EncryptSecrets(cfg, oldRing)
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: new key is primary, old key retained for decryption.
	rotated, err := connectors.NewKeyring(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := connectors.DecryptSecrets(sealed, rotated)
	if err != nil {
		t.Fatalf("rotation must keep old values readable: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil || got["token"] != "sk-secret" {
		t.Fatalf("decrypted mismatch after rotation: %s", dec)
	}

	// New values are sealed under the NEW key.
	reSealed, err := connectors.EncryptSecrets(cfg, rotated)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reSealed), connectors.KeyFingerprint(newKey)) {
		t.Fatalf("re-sealed value not under the new key: %s", reSealed)
	}

	// Once the old key is dropped, values it sealed fail loudly (never silently
	// pass through), while values under the surviving key still open.
	newOnly, err := connectors.NewKeyring(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.DecryptSecrets(sealed, newOnly); err == nil {
		t.Fatal("a value sealed under a dropped key must not decrypt")
	}
	if _, err := connectors.DecryptSecrets(reSealed, newOnly); err != nil {
		t.Fatalf("re-sealed value should open under the new key alone: %v", err)
	}

	// Back-compat: a value sealed before key ids were recorded (no "key" field)
	// still opens via the try-each-key path.
	legacy := json.RawMessage(strings.Replace(
		string(sealed), `"key":"`+connectors.KeyFingerprint(oldKey)+`",`, "", 1))
	if string(legacy) == string(sealed) {
		t.Fatal("test setup: key field not stripped")
	}
	if _, err := connectors.DecryptSecrets(legacy, oldRing); err != nil {
		t.Fatalf("legacy (no key id) value should still decrypt: %v", err)
	}
}

func TestInvokeWithEncryptedSQLConnector(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + dir + "/scores.db"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE scores(subject TEXT PRIMARY KEY, risk INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO scores VALUES('acme', 88)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := json.RawMessage(`{"dsn":"` + dsn + `","query":"SELECT risk FROM scores WHERE subject = :subject","args":["subject"]}`)
	enc, err := connectors.EncryptSecrets(cfg, kr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), dsn) {
		t.Fatalf("encrypted sql config leaked dsn: %s", enc)
	}

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "scores", Type: domain.ConnectorSQL, Config: enc,
	})
	resp, err := connectors.InvokeWithSecrets(ctx, s, id, "scores", json.RawMessage(`{"subject":"acme"}`), connectors.EgressPolicy{}, kr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), "88") {
		t.Fatalf("encrypted connector did not fetch expected row: %s", resp)
	}
}

func TestEgressClientGuardsLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The default (zero-value) policy must refuse to dial the loopback test server.
	def := connectors.EgressPolicy{}
	blocked, err := def.Client(2 * time.Second).Get(srv.URL)
	if err == nil {
		_ = blocked.Body.Close()
		t.Fatal("default egress client should block a loopback target (SSRF guard)")
	}
	// AllowPrivate opts in.
	resp, err := connectors.EgressPolicy{AllowPrivate: true}.Client(2 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatalf("AllowPrivate client should reach loopback: %v", err)
	}
	_ = resp.Body.Close()
}
