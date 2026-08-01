// SPDX-License-Identifier: AGPL-3.0-or-later

// Package command implements provider lifecycle validation and event emission.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/providers/domain"
	"github.com/e6qu/intraktible/providers/events"
)

// Handler validates and emits provider lifecycle events.
type Handler struct {
	log eventlog.Log
	now func() time.Time
	mu  sync.Mutex
}

// NewHandler constructs a Handler with the system clock.
func NewHandler(log eventlog.Log) *Handler {
	return &Handler{log: log, now: func() time.Time { return time.Now().UTC() }}
}

// WithNow overrides the command clock for deterministic tests and seed builds.
func (h *Handler) WithNow(now func() time.Time) *Handler {
	h.now = now
	return h
}

// versionState folds one provider version's lifecycle per environment.
type versionState struct {
	manifest     domain.Manifest
	configured   map[domain.Environment]domain.Configuration
	tested       map[domain.Environment]bool
	approved     bool
	deployed     map[domain.Environment]bool
	paused       map[domain.Environment]bool
	retired      map[domain.Environment]bool
	currentStage domain.Stage
}

// foldVersions folds the providers stream into all versions of one provider
// name, keyed by version number.
func (h *Handler) foldVersions(ctx context.Context, name string) (map[int]*versionState, error) {
	envelopes, err := h.log.Read(ctx, 0)
	if err != nil {
		return nil, err
	}
	versions := make(map[int]*versionState)
	ensure := func(v int, manifest domain.Manifest) *versionState {
		st, ok := versions[v]
		if !ok {
			st = &versionState{
				manifest:   manifest,
				configured: map[domain.Environment]domain.Configuration{},
				tested:     map[domain.Environment]bool{},
				deployed:   map[domain.Environment]bool{},
				paused:     map[domain.Environment]bool{},
				retired:    map[domain.Environment]bool{},
			}
			versions[v] = st
		}
		return st
	}
	for _, e := range envelopes {
		if e.Stream != events.StreamProviders {
			continue
		}
		switch e.Type {
		case events.TypeProviderInstalled:
			var p events.Installed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Manifest.Name != name {
				continue
			}
			ensure(p.Manifest.Version, p.Manifest)
		case events.TypeProviderConfigured:
			var p events.Configured
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			st.configured[p.Configuration.Environment] = p.Configuration
		case events.TypeProviderTested:
			var p events.Tested
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			if p.Evidence.Passed {
				for _, env := range []domain.Environment{
					domain.EnvSandbox, domain.EnvStaging, domain.EnvProduction,
				} {
					st.tested[env] = true
				}
			}
		case events.TypeProviderApproved:
			var p events.Approved
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			st.approved = true
		case events.TypeProviderDeployed:
			var p events.Deployed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			st.deployed[p.Environment] = true
			delete(st.paused, p.Environment)
		case events.TypeProviderPaused:
			var p events.Paused
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			st.paused[p.Environment] = true
		case events.TypeProviderResumed:
			var p events.Resumed
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			delete(st.paused, p.Environment)
		case events.TypeProviderUpgraded:
			var p events.Upgraded
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			// The upgrade moves the environment from the old to the new version.
			from := ensure(p.FromVersion, domain.Manifest{Name: name, Version: p.FromVersion})
			to := ensure(p.ToVersion, domain.Manifest{Name: name, Version: p.ToVersion})
			delete(from.deployed, p.Environment)
			to.deployed[p.Environment] = true
		case events.TypeProviderRetired:
			var p events.Retired
			if err := decode(e, &p); err != nil {
				return nil, err
			}
			if p.Name != name {
				continue
			}
			st := ensure(p.Version, domain.Manifest{Name: name, Version: p.Version})
			delete(st.deployed, p.Environment)
			st.retired[p.Environment] = true
		}
	}
	return versions, nil
}

// Install registers a new immutable provider manifest version. The version is
// computed (max existing + 1) so versions are strictly increasing.
func (h *Handler) Install(
	ctx context.Context,
	id identity.Identity,
	name string,
	manifest domain.Manifest,
) (int, eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	versions, err := h.foldVersions(ctx, name)
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	next := 1
	for v := range versions {
		if v >= next {
			next = v + 1
		}
	}
	manifest.Version = next
	manifest.Name = name
	if err := manifest.Validate(); err != nil {
		return 0, eventlog.Envelope{}, err
	}
	manifest.DefinedBy = id.Actor
	manifest.DefinedAt = h.now()
	env, err := h.appendUnique(ctx, id, events.TypeProviderInstalled, events.Installed{
		Manifest: manifest,
	}, "providers.install\x00"+name+"\x00"+fmt.Sprint(next))
	if err != nil {
		return 0, eventlog.Envelope{}, err
	}
	return next, env, nil
}

// Configure records a per-environment configuration for a provider version.
func (h *Handler) Configure(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	config domain.Configuration,
) (eventlog.Envelope, error) {
	if !config.Environment.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("providers: invalid environment %q", config.Environment)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if st.currentStage == domain.StageRetired {
		return eventlog.Envelope{}, fmt.Errorf("providers: version %d of %q is retired", version, name)
	}
	config.ConfiguredBy = id.Actor
	config.ConfiguredAt = h.now()
	return h.appendUnique(ctx, id, events.TypeProviderConfigured, events.Configured{
		Name: name, Version: version, Configuration: config,
	}, "providers.configure\x00"+name+"\x00"+fmt.Sprint(version)+"\x00"+string(config.Environment))
}

// Test records a conformance/validation run against a provider version. Only a
// passing test advances the lifecycle toward approval.
func (h *Handler) Test(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	evidence domain.TestEvidence,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.requireVersion(ctx, name, version); err != nil {
		return eventlog.Envelope{}, err
	}
	if !evidence.Passed {
		return eventlog.Envelope{}, errors.New("providers: a failing test does not advance the lifecycle")
	}
	evidence.TestedBy = id.Actor
	evidence.TestedAt = h.now()
	return h.appendUnique(ctx, id, events.TypeProviderTested, events.Tested{
		Name: name, Version: version, Evidence: evidence,
	}, "")
}

// Approve is the four-eyes gate: an independent actor (not the installer/author)
// approves a tested provider version for deployment.
func (h *Handler) Approve(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	requestID, reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("providers: approval reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !st.tested[domain.EnvProduction] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q must pass its conformance test before approval", version, name,
		)
	}
	if id.Actor == st.manifest.DefinedBy {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: four-eyes — %q cannot approve their own provider version", id.Actor,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderApproved, events.Approved{
		Name: name, Version: version,
		Approval: domain.Approval{
			RequestID: requestID, ApprovedBy: id.Actor, Reason: reason, ApprovedAt: h.now(),
		},
	}, "providers.approve\x00"+name+"\x00"+fmt.Sprint(version)+"\x00"+requestID)
}

// Deploy activates an approved provider version in an environment.
func (h *Handler) Deploy(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	environment domain.Environment,
) (eventlog.Envelope, error) {
	if !environment.Valid() {
		return eventlog.Envelope{}, fmt.Errorf("providers: invalid environment %q", environment)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !st.approved {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q must be approved before deployment", version, name,
		)
	}
	if _, configured := st.configured[environment]; !configured {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is not configured for %s", version, name, environment,
		)
	}
	if st.deployed[environment] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is already deployed in %s", version, name, environment,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderDeployed, events.Deployed{
		Name: name, Version: version, Environment: environment, DeployedBy: id.Actor,
	}, "providers.deploy\x00"+name+"\x00"+fmt.Sprint(version)+"\x00"+string(environment))
}

// Pause suspends a deployed provider version in an environment.
func (h *Handler) Pause(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	environment domain.Environment,
	reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("providers: pause reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !st.deployed[environment] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is not deployed in %s", version, name, environment,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderPaused, events.Paused{
		Name: name, Version: version, Environment: environment, Reason: reason, PausedBy: id.Actor,
	}, "")
}

// Resume un-pauses a paused provider version.
func (h *Handler) Resume(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	environment domain.Environment,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if !st.paused[environment] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is not paused in %s", version, name, environment,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderResumed, events.Resumed{
		Name: name, Version: version, Environment: environment, ResumedBy: id.Actor,
	}, "")
}

// Upgrade moves an environment from the current deployed version to a newer
// approved version.
func (h *Handler) Upgrade(
	ctx context.Context,
	id identity.Identity,
	name string,
	toVersion int,
	environment domain.Environment,
) (eventlog.Envelope, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	versions, err := h.foldVersions(ctx, name)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	from, ok := versions[toVersion-1]
	if !ok || !from.deployed[environment] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: no version of %q is deployed in %s to upgrade from", name, environment,
		)
	}
	to, ok := versions[toVersion]
	if !ok || !to.approved {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q must be approved before upgrading to it", toVersion, name,
		)
	}
	if _, configured := to.configured[environment]; !configured {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is not configured for %s", toVersion, name, environment,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderUpgraded, events.Upgraded{
		Name: name, FromVersion: from.manifest.Version, ToVersion: toVersion,
		Environment: environment, UpgradedBy: id.Actor,
	}, "providers.upgrade\x00"+name+"\x00"+string(environment)+"\x00"+fmt.Sprint(toVersion))
}

// Retire removes a provider version from service in an environment.
func (h *Handler) Retire(
	ctx context.Context,
	id identity.Identity,
	name string,
	version int,
	environment domain.Environment,
	reason string,
) (eventlog.Envelope, error) {
	if reason == "" {
		return eventlog.Envelope{}, errors.New("providers: retirement reason is required")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, err := h.requireVersion(ctx, name, version)
	if err != nil {
		return eventlog.Envelope{}, err
	}
	if st.retired[environment] {
		return eventlog.Envelope{}, fmt.Errorf(
			"providers: version %d of %q is already retired in %s", version, name, environment,
		)
	}
	return h.appendUnique(ctx, id, events.TypeProviderRetired, events.Retired{
		Name: name, Version: version, Environment: environment, Reason: reason, RetiredBy: id.Actor,
	}, "providers.retire\x00"+name+"\x00"+fmt.Sprint(version)+"\x00"+string(environment))
}

// requireVersion folds and returns a provider version, failing when it does not exist.
func (h *Handler) requireVersion(
	ctx context.Context,
	name string,
	version int,
) (*versionState, error) {
	versions, err := h.foldVersions(ctx, name)
	if err != nil {
		return nil, err
	}
	st, ok := versions[version]
	if !ok {
		return nil, fmt.Errorf("providers: unknown provider %q version %d", name, version)
	}
	return st, nil
}

func (h *Handler) appendUnique(
	ctx context.Context,
	id identity.Identity,
	typ string,
	payload any,
	claim string,
) (eventlog.Envelope, error) {
	return eventlog.AppendClaim(ctx, h.log, id, events.StreamProviders, typ, h.now(), payload, claim)
}

func decode(envelope eventlog.Envelope, payload any) error {
	if err := json.Unmarshal(envelope.Payload, payload); err != nil {
		return fmt.Errorf("providers: decode %s seq %d: %w", envelope.Type, envelope.Seq, err)
	}
	return nil
}
