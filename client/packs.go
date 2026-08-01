// SPDX-License-Identifier: AGPL-3.0-or-later

package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/e6qu/intraktible/packs/domain"
	"github.com/e6qu/intraktible/packs/projection"
)

// PackView is the platform's public solution-pack read model.
type PackView = projection.PackView

// PackDefineAccepted is the define result (the assigned version).
type PackDefineAccepted struct {
	EventID string `json:"event_id"`
	Seq     uint64 `json:"seq"`
	Version int    `json:"version"`
}

// DefinePack registers a new immutable solution-pack manifest version.
func (c *Client) DefinePack(
	ctx context.Context,
	manifest domain.Manifest,
) (PackDefineAccepted, error) {
	return do[PackDefineAccepted](ctx, c, http.MethodPost, "/v1/packs", manifest)
}

// ListPacks lists solution packs and their install state.
func (c *Client) ListPacks(ctx context.Context) ([]PackView, error) {
	out, err := do[struct {
		Packs []PackView `json:"packs"`
	}](ctx, c, http.MethodGet, "/v1/packs", nil)
	return out.Packs, err
}

// GetPack reads one solution pack (manifests + install state).
func (c *Client) GetPack(ctx context.Context, name string) (PackView, error) {
	return do[PackView](ctx, c, http.MethodGet, "/v1/packs/"+url.PathEscape(name), nil)
}

// InstallPack installs a defined pack version into the workspace.
func (c *Client) InstallPack(ctx context.Context, name string, version int) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/packs/"+url.PathEscape(name)+"/install", map[string]int{"version": version})
}

// UpgradePack upgrades an installed pack to a newer version.
func (c *Client) UpgradePack(ctx context.Context, name string, version int) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/packs/"+url.PathEscape(name)+"/upgrade", map[string]int{"version": version})
}

// RollbackPack rolls an installed pack back to a prior version.
func (c *Client) RollbackPack(ctx context.Context, name string, version int) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/packs/"+url.PathEscape(name)+"/rollback", map[string]int{"version": version})
}

// RetirePack removes an installed pack from the workspace.
func (c *Client) RetirePack(ctx context.Context, name, reason string) (CommandResult, error) {
	return do[CommandResult](ctx, c, http.MethodPost,
		"/v1/packs/"+url.PathEscape(name)+"/retire", map[string]string{"reason": reason})
}
