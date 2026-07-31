// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/metrics"
	"github.com/e6qu/intraktible/platform/store"
)

const schedulerActor = "agent-governance-scheduler"

// Scheduler drives exact-release activation, expiry, and severe-incident
// containment from projected work queues. Commands re-fold the event log before
// acting, so a stale projection can at worst cause a refused retry, never bypass
// a gate.
type Scheduler struct {
	store store.Store
	cmd   *Handler
	now   func() time.Time
}

func NewScheduler(st store.Store, cmd *Handler) *Scheduler {
	return &Scheduler{
		store: st, cmd: cmd, now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Scheduler) WithNow(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

type TickSummary struct {
	Activated            int `json:"activated"`
	Expired              int `json:"expired"`
	CircuitsOpened       int `json:"circuits_opened"`
	SafetyContained      int `json:"safety_contained"`
	ReviewsExpired       int `json:"reviews_expired"`
	ToolApprovalsExpired int `json:"tool_approvals_expired"`
}

func (s *Scheduler) Tick(ctx context.Context) (TickSummary, error) {
	deployments, err := ListAllDeployments(ctx, s.store)
	if err != nil {
		return TickSummary{}, fmt.Errorf("agent governance scheduler: list deployments: %w", err)
	}
	now := s.now()
	var summary TickSummary
	toolApprovals, err := ListAllToolApprovals(ctx, s.store)
	if err != nil {
		return summary, fmt.Errorf(
			"agent governance scheduler: list tool approvals: %w", err,
		)
	}
	for _, approval := range toolApprovals {
		if now.Before(approval.ExpiresAt) {
			continue
		}
		id := identity.Identity{
			Org: approval.Org, Workspace: approval.Workspace, Actor: schedulerActor,
		}
		if _, err := s.cmd.ExpireToolApproval(ctx, id, approval.ApprovalID); err != nil {
			return summary, err
		}
		summary.ToolApprovalsExpired++
	}
	releases, err := ListAllReleases(ctx, s.store)
	if err != nil {
		return summary, fmt.Errorf("agent governance scheduler: list releases: %w", err)
	}
	for _, release := range releases {
		if release.Status != ReleaseReviewRequested || release.Review == nil ||
			now.Before(release.Review.ExpiresAt) {
			continue
		}
		id := identity.Identity{
			Org: release.Org, Workspace: release.Workspace, Actor: schedulerActor,
		}
		if _, err := s.cmd.ExpireReview(
			ctx, id, release.TemplateID, release.Release, release.Review.RequestID,
		); err != nil {
			return summary, err
		}
		summary.ReviewsExpired++
	}
	openedCritical := make(map[string]bool)
	for _, deployment := range deployments {
		if deployment.Status != DeploymentScheduled &&
			deployment.Status != DeploymentActive {
			continue
		}
		id := identity.Identity{
			Org: deployment.Org, Workspace: deployment.Workspace, Actor: schedulerActor,
		}
		opened, _, err := s.cmd.EvaluateDeploymentCircuit(
			ctx, id, deployment.DeploymentID,
		)
		if err != nil {
			return summary, err
		}
		if opened {
			summary.CircuitsOpened++
			openedCritical[tenantReleaseRef(
				deployment.Org, deployment.Workspace,
				deployment.TemplateID, deployment.Release,
			)] = true
		}
	}
	incidents, err := ListAllIncidents(ctx, s.store)
	if err != nil {
		return summary, fmt.Errorf("agent governance scheduler: list incidents: %w", err)
	}
	critical := make(map[string]bool)
	for _, incident := range incidents {
		if incident.Severity == SeverityCritical {
			critical[tenantReleaseRef(
				incident.Org, incident.Workspace, incident.TemplateID, incident.Release,
			)] = true
		}
	}
	for ref := range openedCritical {
		critical[ref] = true
	}
	for _, deployment := range deployments {
		id := identity.Identity{
			Org: deployment.Org, Workspace: deployment.Workspace, Actor: schedulerActor,
		}
		if (deployment.Status == DeploymentScheduled || deployment.Status == DeploymentActive) &&
			critical[tenantReleaseRef(
				deployment.Org, deployment.Workspace,
				deployment.TemplateID, deployment.Release,
			)] {
			if _, err := s.cmd.PauseDeployment(
				ctx, id, deployment.DeploymentID, "automatic containment: critical safety incident",
			); err != nil {
				return summary, err
			}
			summary.SafetyContained++
			continue
		}
		if (deployment.Status == DeploymentScheduled || deployment.Status == DeploymentActive) &&
			deployment.ExpiresAt != nil && !now.Before(*deployment.ExpiresAt) {
			if _, err := s.cmd.PauseDeployment(
				ctx, id, deployment.DeploymentID, "deployment window expired",
			); err != nil {
				return summary, err
			}
			summary.Expired++
			continue
		}
		if deployment.Status == DeploymentScheduled &&
			(deployment.ActivateAt == nil || !now.Before(*deployment.ActivateAt)) {
			if _, err := s.cmd.ActivateDeployment(ctx, id, deployment.DeploymentID); err != nil {
				return summary, err
			}
			summary.Activated++
		}
	}
	return summary, nil
}

func tenantReleaseRef(org, workspace, templateID string, release int) string {
	return org + "\x00" + workspace + "\x00" + releaseRef(templateID, release)
}

func (s *Scheduler) Run(ctx context.Context, interval time.Duration, report func(error)) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			summary, err := s.Tick(ctx)
			report(err)
			if err != nil {
				slog.Error("agent governance scheduler: tick", "err", err)
				metrics.RecordSchedulerTick("agent_governance", "error")
				continue
			}
			if summary.Activated > 0 || summary.Expired > 0 ||
				summary.CircuitsOpened > 0 || summary.SafetyContained > 0 ||
				summary.ReviewsExpired > 0 ||
				summary.ToolApprovalsExpired > 0 {
				slog.Info(
					"agent governance scheduler",
					"activated", summary.Activated,
					"expired", summary.Expired,
					"circuits_opened", summary.CircuitsOpened,
					"safety_contained", summary.SafetyContained,
					"reviews_expired", summary.ReviewsExpired,
					"tool_approvals_expired", summary.ToolApprovalsExpired,
				)
			}
			metrics.RecordSchedulerTick("agent_governance", "ok")
		}
	}
}
