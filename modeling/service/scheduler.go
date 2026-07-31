// SPDX-License-Identifier: AGPL-3.0-or-later

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	platformscheduler "github.com/e6qu/intraktible/platform/scheduler"
	"github.com/e6qu/intraktible/platform/store"
)

// Scheduler owns model-data lifecycle sweeps that must run on the singleton
// scheduler tier rather than on API or worker replicas.
type Scheduler struct {
	cmd    *command.Handler
	store  store.Store
	now    func() time.Time
	leader *platformscheduler.Leader
}

// SchedulerSummary reports durable transitions, not merely scanned records.
type SchedulerSummary struct {
	SnapshotsExpired   int `json:"snapshots_expired"`
	FreshnessOpened    int `json:"freshness_opened"`
	FreshnessRecovered int `json:"freshness_recovered"`
}

// NewScheduler builds the modeling retention and quality scheduler.
func NewScheduler(cmd *command.Handler, st store.Store) *Scheduler {
	return &Scheduler{
		cmd: cmd, store: st, now: func() time.Time { return time.Now().UTC() },
	}
}

// WithNow overrides lifecycle time for deterministic tests and demo seeds.
func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// WithLeader elects one leader per sweep epoch across redundant replicas.
func (s *Scheduler) WithLeader(ldr *platformscheduler.Leader) *Scheduler {
	s.leader = ldr
	return s
}

// Tick expires retained snapshot bodies and reconciles source freshness.
func (s *Scheduler) Tick(ctx context.Context) (SchedulerSummary, error) {
	var summary SchedulerSummary
	expired, err := s.expireSnapshots(ctx)
	if err != nil {
		return summary, err
	}
	summary.SnapshotsExpired = expired
	opened, recovered, err := s.reconcileFreshness(ctx)
	if err != nil {
		return summary, err
	}
	summary.FreshnessOpened, summary.FreshnessRecovered = opened, recovered
	return summary, nil
}

func (s *Scheduler) expireSnapshots(ctx context.Context) (int, error) {
	records, err := s.store.List(ctx, modelprojection.CollectionSnapshots, "")
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, record := range records {
		var view modelprojection.SnapshotView
		if err := json.Unmarshal(record.Doc, &view); err != nil {
			return expired, fmt.Errorf("modeling scheduler: decode snapshot %q: %w", record.Key, err)
		}
		if view.State == "expired" || s.now().Before(view.Manifest.ExpiresAt) {
			continue
		}
		id, err := identity.New(view.Org, view.Workspace, "modeling-retention-scheduler")
		if err != nil {
			return expired, err
		}
		if _, err := s.cmd.ExpireSnapshot(
			ctx, id, view.Manifest.SnapshotID, "dataset retention elapsed",
		); err != nil {
			if errors.Is(err, eventlog.ErrConflict) ||
				errors.Is(err, context.Canceled) {
				if errors.Is(err, context.Canceled) {
					return expired, err
				}
				continue
			}
			return expired, err
		}
		expired++
	}
	return expired, nil
}

func (s *Scheduler) reconcileFreshness(ctx context.Context) (int, int, error) {
	records, err := s.store.List(ctx, modelprojection.CollectionSchemas, "")
	if err != nil {
		return 0, 0, err
	}
	wanted := make(map[string]bool)
	opened := 0
	for _, record := range records {
		var schema modelprojection.SchemaView
		if err := json.Unmarshal(record.Doc, &schema); err != nil {
			return opened, 0, fmt.Errorf("modeling scheduler: decode schema %q: %w", record.Key, err)
		}
		active, ok := schema.Active()
		if !ok || active.Spec.Quality.FreshnessSeconds == 0 {
			continue
		}
		baseline := time.Time{}
		health, found, err := modelprojection.ReadSourceHealth(
			ctx, s.store,
			identity.Identity{Org: schema.Org, Workspace: schema.Workspace},
			schema.Ref,
		)
		if err != nil {
			return opened, 0, err
		}
		if found {
			baseline = health.LastReceivedAt
		}
		if baseline.IsZero() && active.ApprovedAt != nil {
			baseline = *active.ApprovedAt
		}
		if baseline.IsZero() {
			return opened, 0, fmt.Errorf(
				"modeling scheduler: active schema %s has no freshness baseline",
				schema.Ref.Key(),
			)
		}
		deadline := baseline.Add(
			time.Duration(active.Spec.Quality.FreshnessSeconds) * time.Second,
		)
		if !s.now().After(deadline) {
			continue
		}
		incidentID := freshnessIncidentID(
			schema.Org, schema.Workspace, schema.Ref, active.Version, baseline,
		)
		wanted[store.Key(schema.Org, schema.Workspace, incidentID)] = true
		id, err := identity.New(schema.Org, schema.Workspace, "modeling-quality-scheduler")
		if err != nil {
			return opened, 0, err
		}
		_, err = s.cmd.OpenFreshnessIncident(
			ctx, id, incidentID, schema.Ref, active.Version, active.Hash,
			active.Spec.Quality.Action, baseline, deadline,
		)
		if errors.Is(err, eventlog.ErrConflict) {
			continue
		}
		if err != nil {
			return opened, 0, err
		}
		opened++
	}

	incidents, err := s.store.List(ctx, modelprojection.CollectionQualityIncidents, "")
	if err != nil {
		return opened, 0, err
	}
	recovered := 0
	for _, record := range incidents {
		var incident modelprojection.QualityIncident
		if err := json.Unmarshal(record.Doc, &incident); err != nil {
			return opened, recovered, fmt.Errorf(
				"modeling scheduler: decode incident %q: %w", record.Key, err,
			)
		}
		if incident.IncidentType != "freshness" || incident.Status != "open" ||
			wanted[record.Key] {
			continue
		}
		id, err := identity.New(
			incident.Org, incident.Workspace, "modeling-quality-scheduler",
		)
		if err != nil {
			return opened, recovered, err
		}
		if _, err := s.cmd.RecoverFreshnessIncident(
			ctx, id, incident.ObservationID,
		); err != nil {
			if errors.Is(err, eventlog.ErrConflict) {
				continue
			}
			return opened, recovered, err
		}
		recovered++
	}
	return opened, recovered, nil
}

func freshnessIncidentID(
	org string,
	workspace string,
	ref domain.SchemaRef,
	version int,
	baseline time.Time,
) string {
	sum := sha256.Sum256([]byte(
		org + "\x00" + workspace + "\x00" + ref.Key() + "\x00" +
			fmt.Sprint(version) + "\x00" + baseline.UTC().Format(time.RFC3339Nano),
	))
	return "freshness-" + hex.EncodeToString(sum[:12])
}

// Run executes the lifecycle sweep on the shared scheduler cadence.
func (s *Scheduler) Run(
	ctx context.Context,
	interval time.Duration,
	report func(error),
) {
	platformscheduler.RunLeader(
		ctx, s.leader, interval, "modeling_lifecycle", "modeling lifecycle", report,
		s.Tick,
		func(summary SchedulerSummary) {
			if summary != (SchedulerSummary{}) {
				slog.Info(
					"modeling lifecycle scheduler tick",
					"snapshots_expired", summary.SnapshotsExpired,
					"freshness_opened", summary.FreshnessOpened,
					"freshness_recovered", summary.FreshnessRecovered,
				)
			}
		},
	)
}
