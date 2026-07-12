// SPDX-License-Identifier: AGPL-3.0-or-later

package fairlending

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/store"
)

// StreamSettings is the event stream for a workspace's adverse-action settings.
const StreamSettings = "fairlending.adverse_settings"

// TypeSettingsSet records a replacement of the workspace adverse-action settings.
const TypeSettingsSet = "fairlending.adverse_settings_set"

// SettingsCollection holds the workspace adverse-action settings (one doc).
const SettingsCollection = "fairlending_adverse_settings"

// settingsDocID is the fixed per-workspace key for the settings doc.
const settingsDocID = "adverse-action"

// Settings is the workspace creditor identification an adverse-action notice needs
// to satisfy ECOA / Reg B — the notice must name and locate the creditor. It is not
// decision data, so it is configured once per workspace, not per decision.
type Settings struct {
	CreditorName    string `json:"creditor_name"`
	CreditorAddress string `json:"creditor_address"`
	CreditorPhone   string `json:"creditor_phone"`
	// EnforcementAgency identifies the federal agency named in the ECOA notice. It
	// varies by creditor type; an empty value renders the generic FTC reference.
	EnforcementAgency string `json:"enforcement_agency"`
}

// SettingsView is the stored settings with its provenance.
type SettingsView struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Settings
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// SettingsProjector folds the settings stream into the per-workspace doc.
type SettingsProjector struct{}

// Name identifies the projector.
func (SettingsProjector) Name() string { return SettingsCollection }

// Collections lists the store collection this projector owns.
func (SettingsProjector) Collections() []string { return []string{SettingsCollection} }

// Apply maintains the workspace settings doc from each settings-set event.
func (SettingsProjector) Apply(ctx context.Context, e eventlog.Envelope, s store.Store) error {
	if e.Type != TypeSettingsSet {
		return nil
	}
	var p Settings
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("fairlending_adverse_settings: decode set seq %d: %w", e.Seq, err)
	}
	v := SettingsView{Org: e.Org, Workspace: e.Workspace, Settings: p, UpdatedAt: e.Time, UpdatedBy: e.Actor}
	return store.PutDoc(ctx, s, SettingsCollection, store.Key(e.Org, e.Workspace, settingsDocID), v)
}

// ReadSettings returns the workspace adverse-action settings (false when unset).
func ReadSettings(ctx context.Context, s store.Store, id identity.Identity) (SettingsView, bool, error) {
	return store.GetDoc[SettingsView](ctx, s, SettingsCollection, store.Key(id.Org, id.Workspace, settingsDocID))
}

// SetSettings replaces the workspace adverse-action settings. CreditorName is
// required — a notice without a named creditor is not a valid Reg B notice.
func (h *Handler) SetSettings(ctx context.Context, id identity.Identity, st Settings) (eventlog.Envelope, error) {
	if err := id.Valid(); err != nil {
		return eventlog.Envelope{}, err
	}
	st.CreditorName = strings.TrimSpace(st.CreditorName)
	st.CreditorAddress = strings.TrimSpace(st.CreditorAddress)
	st.CreditorPhone = strings.TrimSpace(st.CreditorPhone)
	st.EnforcementAgency = strings.TrimSpace(st.EnforcementAgency)
	if st.CreditorName == "" {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: creditor_name is required")
	}
	b, err := json.Marshal(st)
	if err != nil {
		return eventlog.Envelope{}, fmt.Errorf("fairlending: marshal settings: %w", err)
	}
	return h.log.Append(ctx, eventlog.Envelope{
		Org: id.Org, Workspace: id.Workspace, Actor: id.Actor,
		Stream: StreamSettings, Type: TypeSettingsSet, Time: h.now(), Payload: b,
	})
}
