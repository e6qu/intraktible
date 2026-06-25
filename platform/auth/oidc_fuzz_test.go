// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/json"
	"testing"

	"github.com/crewjam/saml"
)

// FuzzGroupsFromClaim asserts the groups-claim reader never panics on an
// arbitrary claims blob. The claims map is decoded from a verified ID token, but
// the IdP controls the values and shapes (array, single string, nested junk),
// and the role mapping must degrade to "no groups" rather than crash.
func FuzzGroupsFromClaim(f *testing.F) {
	for _, s := range []string{
		`{"groups":["admins","staff"]}`,
		`{"groups":"admins"}`,
		`{"groups":[1,2,{"x":true}]}`,
		`{"groups":null}`,
		`{"groups":{}}`,
		`{}`,
		`{"cognito:groups":["a"]}`,
		`{"groups":[["nested"]]}`,
	} {
		f.Add(s, "groups")
	}
	f.Fuzz(func(t *testing.T, claimsJSON, key string) {
		if !json.Valid([]byte(claimsJSON)) {
			return
		}
		var claims map[string]any
		if err := json.Unmarshal([]byte(claimsJSON), &claims); err != nil {
			return
		}
		groups := groupsFromClaim(claims, key) // must not panic
		for _, g := range groups {
			_ = g
		}
		// roleFor walks the same claim shape and indexes a config map; exercise it too.
		a := &OIDCAuthenticator{cfg: OIDCConfig{
			GroupsClaim: key,
			GroupRoles:  map[string]Role{"admins": RoleAdmin},
			DefaultRole: RoleViewer,
		}}
		_ = a.roleFor(claims) // must not panic
	})
}

// FuzzMapSAMLAssertion asserts the assertion→identity mapping never panics on
// arbitrary attribute/subject shapes. The assertion fields are attacker-supplied
// in the IdP response (only the signature is verified, not the field contents),
// so the mapping must tolerate nil subjects, nil NameIDs, and junk attributes.
func FuzzMapSAMLAssertion(f *testing.F) {
	for _, blob := range []string{
		`{"subject_nameid":"ada@acme.com","email":"","groups":["admins"]}`,
		`{"subject_nameid":"","email":"x@y.z","groups":[]}`,
		`{"subject_nameid":"","email":"","groups":["nobody"]}`,
		`{"groups":["admins","admins","staff"]}`,
	} {
		f.Add(blob)
	}
	f.Fuzz(func(t *testing.T, blob string) {
		if !json.Valid([]byte(blob)) {
			return
		}
		var spec struct {
			SubjectNameID string   `json:"subject_nameid"`
			Email         string   `json:"email"`
			Groups        []string `json:"groups"`
		}
		if err := json.Unmarshal([]byte(blob), &spec); err != nil {
			return
		}
		assertion := assertionFromFuzzSpec(spec.SubjectNameID, spec.Email, spec.Groups)
		cfg := SAMLConfig{
			Org: "demo", Workspace: "main",
			EmailAttribute:  "email",
			GroupsAttribute: "groups",
			GroupRoles:      map[string]Role{"admins": RoleAdmin},
			DefaultRole:     RoleViewer,
		}
		_ = mapSAMLAssertion(cfg, assertion) // must not panic
	})
}

func assertionFromFuzzSpec(nameID, email string, groups []string) *saml.Assertion {
	a := &saml.Assertion{}
	if nameID != "" {
		a.Subject = &saml.Subject{NameID: &saml.NameID{Value: nameID}}
	}
	var attrs []saml.Attribute
	if email != "" {
		attrs = append(attrs, saml.Attribute{
			Name: "email", Values: []saml.AttributeValue{{Value: email}},
		})
	}
	if len(groups) > 0 {
		vals := make([]saml.AttributeValue, 0, len(groups))
		for _, g := range groups {
			vals = append(vals, saml.AttributeValue{Value: g})
		}
		attrs = append(attrs, saml.Attribute{Name: "groups", Values: vals})
	}
	if len(attrs) > 0 {
		a.AttributeStatements = []saml.AttributeStatement{{Attributes: attrs}}
	}
	return a
}
