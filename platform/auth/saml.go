// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"

	"github.com/crewjam/saml"

	"github.com/e6qu/intraktible/platform/identity"
)

// SAMLConfig configures one SAML 2.0 identity provider (the SP side). Users
// authenticated through it log into the bound (Org, Workspace); their role comes
// from a groups attribute.
type SAMLConfig struct {
	Name           string // url-safe identifier
	EntityID       string // this SP's entity id
	ACSURL         string // this SP's Assertion Consumer Service URL
	MetadataURL    string // this SP's metadata URL (advisory; defaults to ACSURL)
	IDPMetadataXML string // the IdP's SAML metadata document
	CertPEM        string // SP certificate (PEM)
	KeyPEM         string // SP private key (PEM)
	Org            string
	Workspace      string
	// EmailAttribute names the assertion attribute carrying the email; if empty or
	// absent, the Subject NameID is used. GroupsAttribute + GroupRoles map group
	// membership to a role (highest match wins); DefaultRole applies otherwise.
	EmailAttribute  string
	GroupsAttribute string
	GroupRoles      map[string]Role
	DefaultRole     Role
}

// SAMLAuthenticator runs the SAML 2.0 SP flow for one provider.
type SAMLAuthenticator struct {
	cfg SAMLConfig
	sp  *saml.ServiceProvider
}

// NewSAMLAuthenticator builds the SP from the IdP metadata and the SP key pair.
func NewSAMLAuthenticator(cfg SAMLConfig) (*SAMLAuthenticator, error) {
	if cfg.Name == "" || cfg.ACSURL == "" || cfg.IDPMetadataXML == "" {
		return nil, fmt.Errorf("auth: saml provider needs name, acs_url, and idp metadata")
	}
	if cfg.Org == "" || cfg.Workspace == "" {
		return nil, fmt.Errorf("auth: saml %q requires org and workspace", cfg.Name)
	}
	if cfg.DefaultRole.Rank() == 0 {
		cfg.DefaultRole = RoleViewer
	}
	keyPair, err := tls.X509KeyPair([]byte(cfg.CertPEM), []byte(cfg.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("auth: saml %q key pair: %w", cfg.Name, err)
	}
	keyPair.Leaf, err = x509.ParseCertificate(keyPair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("auth: saml %q certificate: %w", cfg.Name, err)
	}
	rsaKey, ok := keyPair.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("auth: saml %q key must be RSA", cfg.Name)
	}
	idpMeta, err := parseIDPMetadata([]byte(cfg.IDPMetadataXML))
	if err != nil {
		return nil, fmt.Errorf("auth: saml %q idp metadata: %w", cfg.Name, err)
	}
	acs, err := url.Parse(cfg.ACSURL)
	if err != nil {
		return nil, fmt.Errorf("auth: saml %q acs url: %w", cfg.Name, err)
	}
	meta := acs
	if cfg.MetadataURL != "" {
		if m, err := url.Parse(cfg.MetadataURL); err == nil {
			meta = m
		}
	}
	sp := &saml.ServiceProvider{
		Key:         rsaKey,
		Certificate: keyPair.Leaf,
		IDPMetadata: idpMeta,
		AcsURL:      *acs,
		MetadataURL: *meta,
		EntityID:    cfg.EntityID,
	}
	return &SAMLAuthenticator{cfg: cfg, sp: sp}, nil
}

// parseIDPMetadata decodes IdP SAML metadata — a single EntityDescriptor or an
// EntitiesDescriptor aggregate (take the first entity) — without pulling the
// heavier samlsp helper.
func parseIDPMetadata(data []byte) (*saml.EntityDescriptor, error) {
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(data, &ed); err == nil && ed.EntityID != "" {
		return &ed, nil
	}
	var eds saml.EntitiesDescriptor
	if err := xml.Unmarshal(data, &eds); err != nil {
		return nil, err
	}
	for i := range eds.EntityDescriptors {
		if eds.EntityDescriptors[i].EntityID != "" {
			return &eds.EntityDescriptors[i], nil
		}
	}
	return nil, fmt.Errorf("no entity descriptor in metadata")
}

// Name is the provider's identifier (used in URLs).
func (a *SAMLAuthenticator) Name() string { return a.cfg.Name }

// AuthnRequest returns the IdP redirect URL for an SP-initiated login plus the
// AuthnRequest ID, which the ACS validates as the response's InResponseTo.
func (a *SAMLAuthenticator) AuthnRequest(relayState string) (redirectURL, requestID string, err error) {
	req, err := a.sp.MakeAuthenticationRequest(
		a.sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", "", fmt.Errorf("auth: saml %q authn request: %w", a.cfg.Name, err)
	}
	u, err := req.Redirect(relayState, a.sp)
	if err != nil {
		return "", "", fmt.Errorf("auth: saml %q redirect: %w", a.cfg.Name, err)
	}
	return u.String(), req.ID, nil
}

// Metadata is the SP metadata XML to register with the IdP.
func (a *SAMLAuthenticator) Metadata() ([]byte, error) {
	return xml.MarshalIndent(a.sp.Metadata(), "", "  ")
}

// SAMLLogin is a verified assertion mapped to an identity + role.
type SAMLLogin struct {
	Identity identity.Identity
	Role     Role
}

// ParseACS validates the SAMLResponse in req (signature via the IdP cert,
// conditions, audience, and InResponseTo against possibleRequestIDs) and maps
// the assertion to an identity + role.
func (a *SAMLAuthenticator) ParseACS(req *http.Request, possibleRequestIDs []string) (SAMLLogin, error) {
	assertion, err := a.sp.ParseResponse(req, possibleRequestIDs)
	if err != nil {
		return SAMLLogin{}, fmt.Errorf("auth: saml %q parse response: %w", a.cfg.Name, err)
	}
	return mapSAMLAssertion(a.cfg, assertion), nil
}

// mapSAMLAssertion turns a verified assertion into an identity + role. It is a
// pure function of the config and the assertion (no crypto), so it is unit
// tested directly.
func mapSAMLAssertion(cfg SAMLConfig, assertion *saml.Assertion) SAMLLogin {
	email := firstAttr(assertion, cfg.EmailAttribute)
	if email == "" && assertion.Subject != nil && assertion.Subject.NameID != nil {
		email = assertion.Subject.NameID.Value
	}
	role := cfg.DefaultRole
	for _, g := range attrValues(assertion, cfg.GroupsAttribute) {
		if r, ok := cfg.GroupRoles[g]; ok && r.Rank() > role.Rank() {
			role = r
		}
	}
	return SAMLLogin{
		Identity: identity.Identity{Org: cfg.Org, Workspace: cfg.Workspace, Actor: email},
		Role:     role,
	}
}

func firstAttr(assertion *saml.Assertion, name string) string {
	if vs := attrValues(assertion, name); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func attrValues(assertion *saml.Assertion, name string) []string {
	if name == "" {
		return nil
	}
	var out []string
	for _, st := range assertion.AttributeStatements {
		for _, at := range st.Attributes {
			if at.Name == name || at.FriendlyName == name {
				for _, v := range at.Values {
					out = append(out, v.Value)
				}
			}
		}
	}
	return out
}
