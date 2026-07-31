// SPDX-License-Identifier: AGPL-3.0-or-later

package client_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/e6qu/intraktible/client"
	"github.com/e6qu/intraktible/tenancy/domain"
)

type tenancyRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip tenancyRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestGoSDKTenancySurface(t *testing.T) {
	var requests []string
	transport := tenancyRoundTrip(func(request *http.Request) (*http.Response, error) {
		if got := request.Header.Get("X-Api-Key"); got != "tenancy-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		path := request.URL.EscapedPath()
		requests = append(requests, request.Method+" "+path)
		body := `{}`
		switch path {
		case "/v1/platform/orgs":
			if request.Method == http.MethodGet {
				body = `{"orgs":[]}`
			} else {
				body = `{"event_id":"org","seq":1,"org_key":"acme","admin_key_id":"k1","admin_key_secret":"s1"}`
			}
		case "/v1/platform/orgs/acme":
			body = `{"key":"acme","status":"active"}`
		case "/v1/platform/orgs/acme/configure",
			"/v1/platform/orgs/acme/suspend",
			"/v1/platform/orgs/acme/resume",
			"/v1/platform/orgs/acme/delete",
			"/v1/orgs/acme/workspaces",
			"/v1/orgs/acme/workspaces/west/configure",
			"/v1/orgs/acme/workspaces/west/suspend",
			"/v1/orgs/acme/workspaces/west/resume",
			"/v1/orgs/acme/workspaces/west/delete",
			"/v1/orgs/acme/workspaces/west/memberships",
			"/v1/orgs/acme/workspaces/west/memberships/carol/revoke":
			body = `{"event_id":"command","seq":2}`
		case "/v1/orgs/acme/workspaces/west":
			body = `{"org_key":"acme","key":"west","status":"active"}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	sdk := client.New(
		"https://intraktible.example", "tenancy-key",
		client.WithHTTPClient(&http.Client{Transport: transport}),
	)
	ctx := context.Background()

	if _, err := sdk.CreateOrganization(ctx, client.TenancyOrgCreateRequest{
		Key: "acme", Display: "Acme", AdminActor: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ListOrganizations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.GetOrganization(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ConfigureOrganization(ctx, "acme", domain.OrganizationConfig{Plan: "enterprise"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.SuspendOrganization(ctx, "acme", "audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ResumeOrganization(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.DeleteOrganization(ctx, "acme", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.CreateWorkspace(ctx, "acme", "west", "West", domain.WorkspaceConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ListWorkspaces(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.GetWorkspace(ctx, "acme", "west"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ConfigureWorkspace(ctx, "acme", "west", domain.WorkspaceConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.SuspendWorkspace(ctx, "acme", "west", "audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ResumeWorkspace(ctx, "acme", "west"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.DeleteWorkspace(ctx, "acme", "west", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ListMemberships(ctx, "acme", "west"); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.GrantMembership(ctx, "acme", "west", "carol", domain.MembershipEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.RevokeMembership(ctx, "acme", "west", "carol", "offboard"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 17 {
		t.Fatalf("issued %d requests, want 17: %v", len(requests), requests)
	}
}
