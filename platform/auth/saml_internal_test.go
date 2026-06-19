// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
)

func TestMapSAMLAssertion(t *testing.T) {
	cfg := SAMLConfig{
		Org: "demo", Workspace: "main", GroupsAttribute: "groups",
		GroupRoles:  map[string]Role{"admins": RoleAdmin},
		DefaultRole: RoleViewer,
	}
	assertion := &saml.Assertion{
		Subject: &saml.Subject{NameID: &saml.NameID{Value: "ada@acme.com"}},
		AttributeStatements: []saml.AttributeStatement{{
			Attributes: []saml.Attribute{
				{Name: "groups", Values: []saml.AttributeValue{{Value: "staff"}, {Value: "admins"}}},
			},
		}},
	}
	// NameID is the email (no email attribute configured); the admins group → admin.
	got := mapSAMLAssertion(cfg, assertion)
	if got.Identity.Actor != "ada@acme.com" || got.Identity.Org != "demo" || got.Role != RoleAdmin {
		t.Fatalf("map = %+v", got)
	}

	// A configured email attribute wins over the NameID, and no matching group
	// falls back to the default role.
	cfg.EmailAttribute = "email"
	cfg.GroupRoles = map[string]Role{"nobody": RoleAdmin}
	assertion.AttributeStatements[0].Attributes = append(assertion.AttributeStatements[0].Attributes,
		saml.Attribute{Name: "email", Values: []saml.AttributeValue{{Value: "ada@corp.example"}}})
	got = mapSAMLAssertion(cfg, assertion)
	if got.Identity.Actor != "ada@corp.example" || got.Role != RoleViewer {
		t.Fatalf("map (email attr + default role) = %+v", got)
	}
}

func TestNewSAMLAuthenticator(t *testing.T) {
	certPEM, keyPEM := selfSignedPEM(t)
	idpMetadata := `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example/meta">
	  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
	    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/>
	  </IDPSSODescriptor>
	</EntityDescriptor>`

	a, err := NewSAMLAuthenticator(SAMLConfig{
		Name: "test", EntityID: "https://sp.example/meta", ACSURL: "https://sp.example/acs",
		IDPMetadataXML: idpMetadata, CertPEM: certPEM, KeyPEM: keyPEM,
		Org: "demo", Workspace: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "test" {
		t.Fatalf("name = %q", a.Name())
	}

	// SP-initiated login produces a redirect to the IdP SSO with a SAMLRequest.
	redirectURL, requestID, err := a.AuthnRequest("relay-123")
	if err != nil || requestID == "" {
		t.Fatalf("AuthnRequest err=%v id=%q", err, requestID)
	}
	if !strings.HasPrefix(redirectURL, "https://idp.example/sso?") || !strings.Contains(redirectURL, "SAMLRequest=") {
		t.Fatalf("redirect = %s", redirectURL)
	}

	// SP metadata is serveable and carries the SP entity id.
	md, err := a.Metadata()
	if err != nil || !strings.Contains(string(md), "https://sp.example/meta") {
		t.Fatalf("metadata err=%v: %s", err, md)
	}
}

func selfSignedPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-sp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return string(cert), string(key)
}
