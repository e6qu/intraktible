// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/e6qu/intraktible/context-layer/connectors"
	"github.com/e6qu/intraktible/context-layer/domain"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/kms"
	"github.com/e6qu/intraktible/platform/secretbox"
	"github.com/e6qu/intraktible/platform/store"
)

// loc is a stable test SecretLocation; seal and open must use the same one for the
// AAD binding to authenticate. The connector fetch path derives it from the stored
// definition's org/workspace/name (see InvokeWithSecrets), so tests that round-trip
// through it must match those identifiers.
func loc(connector string) connectors.SecretLocation {
	return connectors.SecretLocation{Org: "demo", Workspace: "main", Connector: connector}
}

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

func TestGraphQLConnector(t *testing.T) {
	ctx := context.Background()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"applicant":{"score":73}}}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "gql", Type: domain.ConnectorGraphQL,
		Config: json.RawMessage(`{"url":"` + srv.URL + `","query":"query($id:ID!){applicant(id:$id){score}}"}`),
	})

	resp, err := connectors.InvokeWith(ctx, s, id, "gql", json.RawMessage(`{"id":"a1"}`), connectors.EgressPolicy{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	// The decide input was sent as the GraphQL variables.
	if !strings.Contains(gotBody, `"variables":{"id":"a1"}`) || !strings.Contains(gotBody, `"query"`) {
		t.Fatalf("graphql request body: %s", gotBody)
	}
	if !strings.Contains(string(resp), `"score":73`) {
		t.Fatalf("graphql response: %s", resp)
	}
}

func TestGraphQLConnectorFailsOnErrors(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	}))
	defer srv.Close()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "gql", Type: domain.ConnectorGraphQL,
		Config: json.RawMessage(`{"url":"` + srv.URL + `","query":"{x}"}`),
	})
	if _, err := connectors.InvokeWith(ctx, s, id, "gql", nil, connectors.EgressPolicy{AllowPrivate: true}); err == nil {
		t.Fatal("expected a GraphQL errors response to fail loudly")
	}
}

func TestStaticConnector(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	define(ctx, t, s, id, connectors.ConnectorView{
		Name: "flags", Type: domain.ConnectorStatic,
		Config: json.RawMessage(`{"data":{"min_score":650,"on":true}}`),
	})
	resp, err := connectors.Invoke(ctx, s, id, "flags", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), `"min_score":650`) {
		t.Fatalf("static connector response: %s", resp)
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
	if _, err := connectors.Invoke(context.Background(), seedSQLDef(t, `{"driver":"oracle","dsn":"x","query":"SELECT 1"}`),
		identity.Identity{Org: "demo", Workspace: "main"}, "pg", nil); err == nil {
		t.Fatal("expected an unavailable driver to fail loudly")
	}
}

// TestSQLConnectorPostgres exercises the Postgres driver against a real database
// pointed to by INTRAKTIBLE_TEST_POSTGRES (a pgx DSN); skipped otherwise. It proves
// the positional-arg binding and the read-only transaction (a write is refused).
func TestSQLConnectorPostgres(t *testing.T) {
	dsn := os.Getenv("INTRAKTIBLE_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set INTRAKTIBLE_TEST_POSTGRES (a pgx DSN) to run the Postgres connector test")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// A permanent table: the connector opens its own connection, so a TEMP table
	// (per-connection) wouldn't be visible to it.
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS itk_scores(subject TEXT PRIMARY KEY, risk INT)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS itk_scores`) })
	if _, err := db.ExecContext(ctx, `INSERT INTO itk_scores VALUES('acme',73) ON CONFLICT (subject) DO UPDATE SET risk=EXCLUDED.risk`); err != nil {
		t.Fatal(err)
	}

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	cfg := `{"driver":"postgres","dsn":"` + dsn + `","query":"SELECT subject, risk FROM itk_scores WHERE subject = $1","args":["subject"]}`
	define(ctx, t, s, id, connectors.ConnectorView{Name: "pg", Type: domain.ConnectorSQL, Config: json.RawMessage(cfg)})
	resp, err := connectors.Invoke(ctx, s, id, "pg", json.RawMessage(`{"subject":"acme"}`))
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
		t.Fatalf("unexpected postgres connector response: %s", resp)
	}

	// A read-only connector must refuse a data-modifying statement.
	badCfg := `{"driver":"postgres","dsn":"` + dsn + `","query":"CREATE TABLE itk_evil(x int)"}`
	define(ctx, t, s, id, connectors.ConnectorView{Name: "evil", Type: domain.ConnectorSQL, Config: json.RawMessage(badCfg)})
	if _, err := connectors.Invoke(ctx, s, id, "evil", nil); err == nil {
		t.Fatal("a write in a read-only connector tx must fail loudly")
	}
}

func TestSanctionsConnector(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	cfg := `{"watchlist":[` +
		`{"name":"Vladimir Ivanov","list":"OFAC-SDN","program":"UKRAINE-EO13662"},` +
		`{"name":"Acme Trading LLC","list":"EU"}` +
		`]}`
	define(ctx, t, s, id, connectors.ConnectorView{Name: "screen", Type: domain.ConnectorSanctions, Config: json.RawMessage(cfg)})

	screen := func(name string) struct {
		Matched bool `json:"matched"`
		Matches []struct {
			Name  string  `json:"name"`
			List  string  `json:"list"`
			Score float64 `json:"score"`
		} `json:"matches"`
		Screened int `json:"screened"`
	} {
		resp, err := connectors.Invoke(ctx, s, id, "screen", json.RawMessage(`{"name":"`+name+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Matched bool `json:"matched"`
			Matches []struct {
				Name  string  `json:"name"`
				List  string  `json:"list"`
				Score float64 `json:"score"`
			} `json:"matches"`
			Screened int `json:"screened"`
		}
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Exact match (word order / case ignored) flags with score 1.
	hit := screen("IVANOV, Vladimir")
	if !hit.Matched || len(hit.Matches) != 1 || hit.Matches[0].Score != 1 || hit.Matches[0].List != "OFAC-SDN" {
		t.Fatalf("exact screen = %+v", hit)
	}
	if hit.Screened != 2 {
		t.Fatalf("screened count = %d, want 2", hit.Screened)
	}
	// A subset of tokens ("Vladimir Ivanov" vs a middle name) still flags.
	if sub := screen("Vladimir Petrovich Ivanov"); !sub.Matched {
		t.Fatalf("subset name should flag: %+v", sub)
	}
	// An unrelated name does not flag.
	if clean := screen("Jane Smith"); clean.Matched {
		t.Fatalf("unrelated name should not flag: %+v", clean)
	}
	// Deterministic: same input, byte-identical output (replay-safe).
	r1, _ := connectors.Invoke(ctx, s, id, "screen", json.RawMessage(`{"name":"Vladimir Ivanov"}`))
	r2, _ := connectors.Invoke(ctx, s, id, "screen", json.RawMessage(`{"name":"Vladimir Ivanov"}`))
	if string(r1) != string(r2) {
		t.Fatalf("screening not deterministic: %s vs %s", r1, r2)
	}
	// An empty watchlist is rejected at define time.
	if _, err := connectors.Invoke(ctx, seedConnDef(t, domain.ConnectorSanctions, `{"watchlist":[]}`), id, "c", json.RawMessage(`{"name":"x"}`)); err == nil {
		t.Fatal("an empty watchlist must fail loudly")
	}
}

func TestCreditBureauConnectorNormalizes(t *testing.T) {
	ctx := context.Background()
	// A bureau-shaped response with a nested score and provider-specific field names.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sekret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"riskModel":{"score":720},"grade":"A","reasons":["R01","R02"]}`))
	}))
	defer srv.Close()

	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "dev"}
	cfg := `{"provider":"experian","base_url":"` + srv.URL + `","path":"/v1/inquiry",` +
		`"auth":{"type":"bearer","token":"sekret"},` +
		`"score_field":"riskModel.score","band_field":"grade","reasons_field":"reasons"}`
	define(ctx, t, s, id, connectors.ConnectorView{Name: "bureau", Type: domain.ConnectorCreditBureau, Config: json.RawMessage(cfg)})

	resp, err := connectors.InvokeWith(ctx, s, id, "bureau", json.RawMessage(`{"ssn":"000-00-0000"}`), connectors.EgressPolicy{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Provider    string   `json:"provider"`
		Score       float64  `json:"score"`
		Band        string   `json:"band"`
		ReasonCodes []string `json:"reason_codes"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "experian" || out.Score != 720 || out.Band != "A" || len(out.ReasonCodes) != 2 {
		t.Fatalf("normalized bureau response = %+v (%s)", out, resp)
	}

	// A response missing the score fails loudly (a phantom-0 score would silently drive a decision).
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"grade":"A"}`))
	}))
	defer bad.Close()
	badCfg := `{"provider":"equifax","base_url":"` + bad.URL + `","path":"/x","score_field":"score"}`
	define(ctx, t, s, id, connectors.ConnectorView{Name: "bad", Type: domain.ConnectorCreditBureau, Config: json.RawMessage(badCfg)})
	if _, err := connectors.InvokeWith(ctx, s, id, "bad", nil, connectors.EgressPolicy{AllowPrivate: true}); err == nil {
		t.Fatal("a bureau response with no score must fail loudly")
	}
}

// seedConnDef stores a connector definition named "c" of the given type and returns
// the store — a helper for the fail-loud define/invoke cases.
func seedConnDef(t *testing.T, typ domain.ConnectorType, cfg string) store.Store {
	t.Helper()
	s := store.NewMemory()
	id := identity.Identity{Org: "demo", Workspace: "main"}
	define(context.Background(), t, s, id, connectors.ConnectorView{Name: "c", Type: typ, Config: json.RawMessage(cfg)})
	return s
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
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(enc) == string(cfg) || strings.Contains(string(enc), "file:secret.db") || strings.Contains(string(enc), "sk-123") {
		t.Fatalf("encrypted config leaked plaintext: %s", enc)
	}
	dec, err := connectors.DecryptSecrets(enc, kr, loc("c1"))
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

// TestEncryptSecretsSealsEnvelopeLookalike guards the strict isSecretEnvelope shape:
// a credential value that is an OBJECT merely carrying the $intraktible_sealed marker
// (plus other fields) is NOT a real sealed envelope and must still be sealed — not
// passed through in the clear.
func TestEncryptSecretsSealsEnvelopeLookalike(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	// A "password" whose value carries the marker AND an extra "evil" plaintext field.
	cfg := json.RawMessage(`{"password":{"$intraktible_sealed":"intraktible.sealed.v1","value":"x","evil":"leak-me"}}`)
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "leak-me") {
		t.Fatalf("envelope-lookalike credential was passed through in the clear: %s", enc)
	}
}

// A legacy v1 connector envelope (sealed before AAD binding, no aad) still opens —
// and opens regardless of the location passed, since v1 carries no binding. This is
// the backward-compatibility guarantee for all already-stored connector configs.
func TestDecryptSecretsOpensLegacyV1(t *testing.T) {
	rawKey := []byte("0123456789abcdef0123456789abcdef")
	kr, err := connectors.NewKeyring(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secretbox.NewAESGCMSecretBox(rawKey)
	sealedBytes, _ := box.Encrypt([]byte(`"sk-legacy"`), nil) // v1: no aad
	env := secretbox.Envelope{
		Version: secretbox.Version,
		Key:     secretbox.KeyFingerprint(rawKey),
		Value:   base64.StdEncoding.EncodeToString(sealedBytes),
	}
	envJSON, _ := json.Marshal(env)
	cfg, err := json.Marshal(map[string]any{"token": json.RawMessage(envJSON)})
	if err != nil {
		t.Fatal(err)
	}
	// Opens even with a non-matching location — v1 ignores the aad.
	dec, err := connectors.DecryptSecrets(cfg, kr, loc("any-connector"))
	if err != nil {
		t.Fatalf("legacy v1 connector envelope must still decrypt: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil || got["token"] != "sk-legacy" {
		t.Fatalf("legacy v1 decrypt mismatch: %s", dec)
	}
}

// A v2 connector seal opens with the correct location AAD and FAILS to open with a
// different one — the replay defense. Different org, workspace, or connector name
// all change the AAD, so each must reject the ciphertext.
func TestEncryptSecretsAADReplayDefense(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := json.RawMessage(`{"token":"sk-secret","host":"db.example"}`)
	sealed, err := connectors.EncryptSecrets(cfg, kr, loc("scores"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sealed), secretbox.VersionAAD) {
		t.Fatalf("a connector seal should carry the v2 marker: %s", sealed)
	}
	// Correct location opens.
	if _, err := connectors.DecryptSecrets(sealed, kr, loc("scores")); err != nil {
		t.Fatalf("matching location must open: %v", err)
	}
	wrong := []connectors.SecretLocation{
		{Org: "other", Workspace: "main", Connector: "scores"},
		{Org: "demo", Workspace: "other", Connector: "scores"},
		{Org: "demo", Workspace: "main", Connector: "renamed"},
	}
	for _, w := range wrong {
		if _, err := connectors.DecryptSecrets(sealed, kr, w); err == nil {
			t.Fatalf("a transplanted location must not open: %+v", w)
		}
	}
}

// A connector envelope transplanted to a DIFFERENT FIELD of the same connector
// fails authentication: each field's AAD includes its path, so moving the sealed
// "token" value into a "password" slot (same key, wrong field AAD) cannot open.
func TestEncryptSecretsFieldTransplantFails(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := connectors.EncryptSecrets(
		json.RawMessage(`{"token":"sk-secret"}`), kr, loc("scores"))
	if err != nil {
		t.Fatal(err)
	}
	// Lift the sealed "token" envelope and graft it under "password".
	var m map[string]any
	if err := json.Unmarshal(sealed, &m); err != nil {
		t.Fatal(err)
	}
	transplanted, err := json.Marshal(map[string]any{"password": m["token"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connectors.DecryptSecrets(transplanted, kr, loc("scores")); err == nil {
		t.Fatal("a field-transplanted envelope must not authenticate")
	}
}

func TestKMSKeyringRoundTrip(t *testing.T) {
	kr := connectors.NewKMSKeyring("kms:test", kms.Fake{})
	cfg := json.RawMessage(`{"dsn":"file:secret.db","token":"sk-123","keep":"ok"}`)
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "sk-123") || strings.Contains(string(enc), "file:secret.db") {
		t.Fatalf("KMS-sealed config leaked plaintext: %s", enc)
	}
	if !strings.Contains(string(enc), "kms:test") {
		t.Fatalf("envelope should record the KMS key id: %s", enc)
	}
	dec, err := connectors.DecryptSecrets(enc, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil {
		t.Fatal(err)
	}
	if got["dsn"] != "file:secret.db" || got["token"] != "sk-123" || got["keep"] != "ok" {
		t.Fatalf("KMS decrypt mismatch: %s", dec)
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
	sealed, err := connectors.EncryptSecrets(cfg, oldRing, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: new key is primary, old key retained for decryption.
	rotated, err := connectors.NewKeyring(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := connectors.DecryptSecrets(sealed, rotated, loc("c1"))
	if err != nil {
		t.Fatalf("rotation must keep old values readable: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil || got["token"] != "sk-secret" {
		t.Fatalf("decrypted mismatch after rotation: %s", dec)
	}

	// New values are sealed under the NEW key.
	reSealed, err := connectors.EncryptSecrets(cfg, rotated, loc("c1"))
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
	if _, err := connectors.DecryptSecrets(sealed, newOnly, loc("c1")); err == nil {
		t.Fatal("a value sealed under a dropped key must not decrypt")
	}
	if _, err := connectors.DecryptSecrets(reSealed, newOnly, loc("c1")); err != nil {
		t.Fatalf("re-sealed value should open under the new key alone: %v", err)
	}

	// Back-compat: a value sealed before key ids were recorded (no "key" field)
	// still opens via the try-each-key path.
	legacy := json.RawMessage(strings.Replace(
		string(sealed), `"key":"`+connectors.KeyFingerprint(oldKey)+`",`, "", 1))
	if string(legacy) == string(sealed) {
		t.Fatal("test setup: key field not stripped")
	}
	if _, err := connectors.DecryptSecrets(legacy, oldRing, loc("c1")); err != nil {
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
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("scores"))
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

// TestHeaderValuesAreSecretsWhateverTheyAreCalled: an HTTP connector's headers map
// takes arbitrary keys, so a credential travels under a name no denylist can guess
// ("X-Api-Key"). Every header value must be sealed at rest and masked on read, while
// the header names — useful, and not secret — stay visible.
func TestHeaderValuesAreSecretsWhateverTheyAreCalled(t *testing.T) {
	cfg := json.RawMessage(`{"url":"https://api.example.com","method":"GET","headers":{"X-Api-Key":"sk-live-123","Accept":"application/json"}}`)

	out := connectors.RedactConfig(cfg)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "sk-live-123") {
		t.Fatalf("a header credential survived redaction: %s", out)
	}
	headers, ok := m["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers should stay an object: %v", m["headers"])
	}
	if headers["X-Api-Key"] != "[redacted]" || headers["Accept"] != "[redacted]" {
		t.Fatalf("every header value must be masked: %v", headers)
	}
	if m["url"] != "https://api.example.com" {
		t.Fatalf("non-secret routing config must survive: %v", m)
	}

	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := connectors.EncryptSecrets(cfg, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "sk-live-123") {
		t.Fatalf("a header credential was stored in cleartext: %s", enc)
	}
	// And the fetch path still gets the real value back.
	dec, err := connectors.DecryptSecrets(enc, kr, loc("c1"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(dec, &got); err != nil {
		t.Fatal(err)
	}
	decHeaders := got["headers"].(map[string]any)
	if decHeaders["X-Api-Key"] != "sk-live-123" || decHeaders["Accept"] != "application/json" {
		t.Fatalf("headers did not survive the seal/open round trip: %s", dec)
	}
}

// TestHeaderContainerSealRoundTripsEveryShape guards the seal↔open symmetry for a
// secret container whose value is not the usual object: a bare string or an array
// under `headers` must still open to exactly what was sealed, and re-sealing an
// already-sealed config must be idempotent (no nested double-sealing that would
// strand the credential unreadable).
func TestHeaderContainerSealRoundTripsEveryShape(t *testing.T) {
	kr, err := connectors.NewKeyring([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	shapes := map[string]string{
		"object":        `{"url":"https://x","headers":{"X-Api-Key":"sk-live-1"}}`,
		"bare string":   `{"url":"https://x","headers":"sk-live-2"}`,
		"array":         `{"url":"https://x","headers":["sk-live-3","sk-live-4"]}`,
		"nested object": `{"url":"https://x","headers":{"auth":{"token":"sk-live-5"}}}`,
	}
	for name, cfg := range shapes {
		t.Run(name, func(t *testing.T) {
			sealed, err := connectors.EncryptSecrets(json.RawMessage(cfg), kr, loc("c1"))
			if err != nil {
				t.Fatalf("seal: %v", err)
			}
			for _, secret := range []string{"sk-live-1", "sk-live-2", "sk-live-3", "sk-live-4", "sk-live-5"} {
				if strings.Contains(string(sealed), secret) {
					t.Fatalf("a header credential survived sealing: %s", sealed)
				}
			}
			// Re-sealing an already-sealed config must be idempotent.
			resealed, err := connectors.EncryptSecrets(sealed, kr, loc("c1"))
			if err != nil {
				t.Fatalf("reseal: %v", err)
			}
			// Opening either the once- or twice-sealed config returns the original.
			for _, s := range []json.RawMessage{sealed, resealed} {
				opened, err := connectors.DecryptSecrets(s, kr, loc("c1"))
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				var got, want any
				if err := json.Unmarshal(opened, &got); err != nil {
					t.Fatalf("opened config is not JSON: %v", err)
				}
				if err := json.Unmarshal([]byte(cfg), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("round trip changed the config:\n got:  %s\n want: %s", opened, cfg)
				}
			}
		})
	}
}
