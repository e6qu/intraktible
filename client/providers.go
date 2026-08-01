// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/e6qu/intraktible/providers/domain"
	"github.com/e6qu/intraktible/providers/projection"
)

// ProviderView / ProviderHealth are the platform's public provider read models.
type (
	ProviderView   = projection.ProviderView
	ProviderHealth = projection.HealthView
)

// ProviderInstallAccepted is the install result (the assigned version).
type ProviderInstallAccepted struct {
	EventID string `json:"event_id"`
	Seq     uint64 `json:"seq"`
	Version int    `json:"version"`
}

// InstallProvider registers a new immutable provider manifest version.
func (c *Client) InstallProvider(
	ctx context.Context,
	name, connector, description string,
	conformance domain.Conformance,
) (ProviderInstallAccepted, error) {
	return do[ProviderInstallAccepted](ctx, c, http.MethodPost, "/v1/providers", map[string]any{
		"name": name, "connector": connector, "description": description,
		"conformance": conformance,
	})
}

// ListProviders lists provider manifest versions and lifecycle state.
func (c *Client) ListProviders(ctx context.Context) ([]ProviderView, error) {
	out, err := do[struct {
		Providers []ProviderView `json:"providers"`
	}](ctx, c, http.MethodGet, "/v1/providers", nil)
	return out.Providers, err
}

// GetProvider reads one provider manifest version.
func (c *Client) GetProvider(ctx context.Context, name string, version int) (ProviderView, error) {
	return do[ProviderView](ctx, c, http.MethodGet, providerPath(name, version), nil)
}

// ConfigureProvider configures a provider version for an environment.
func (c *Client) ConfigureProvider(
	ctx context.Context,
	name string,
	version int,
	config domain.Configuration,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/configure", config)
}

// TestProvider records a conformance test against a provider version.
func (c *Client) TestProvider(
	ctx context.Context,
	name string,
	version int,
	evidence domain.TestEvidence,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/test", evidence)
}

// ApproveProvider approves a tested provider version for deployment (four-eyes).
func (c *Client) ApproveProvider(
	ctx context.Context,
	name string,
	version int,
	requestID, reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/approve",
		map[string]string{"request_id": requestID, "reason": reason})
}

// DeployProvider deploys an approved provider version to an environment.
func (c *Client) DeployProvider(
	ctx context.Context,
	name string,
	version int,
	environment domain.Environment,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/deploy",
		map[string]any{"environment": environment})
}

// PauseProvider pauses a deployed provider version.
func (c *Client) PauseProvider(
	ctx context.Context,
	name string,
	version int,
	environment domain.Environment,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/pause",
		map[string]any{"environment": environment, "reason": reason})
}

// ResumeProvider resumes a paused provider version.
func (c *Client) ResumeProvider(
	ctx context.Context,
	name string,
	version int,
	environment domain.Environment,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/resume",
		map[string]any{"environment": environment})
}

// UpgradeProvider upgrades an environment to a newer approved provider version.
func (c *Client) UpgradeProvider(
	ctx context.Context,
	name string,
	toVersion int,
	environment domain.Environment,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/providers/"+url.PathEscape(name)+"/upgrade",
		map[string]any{"to_version": toVersion, "environment": environment})
}

// RetireProvider retires a provider version from an environment.
func (c *Client) RetireProvider(
	ctx context.Context,
	name string,
	version int,
	environment domain.Environment,
	reason string,
) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost, providerPath(name, version)+"/retire",
		map[string]any{"environment": environment, "reason": reason})
}

// ProviderHealth lists provider-environment operational health.
func (c *Client) ProviderHealth(ctx context.Context) ([]ProviderHealth, error) {
	out, err := do[struct {
		Health []ProviderHealth `json:"health"`
	}](ctx, c, http.MethodGet, "/v1/providers/health", nil)
	return out.Health, err
}

func providerPath(name string, version int) string {
	return "/v1/providers/" + url.PathEscape(name) + "/" + strconv.Itoa(version)
}
