// SPDX-License-Identifier: AGPL-3.0-or-later

package command_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	modelingcmd "github.com/e6qu/intraktible/modeling/command"
	"github.com/e6qu/intraktible/modeling/domain"
	"github.com/e6qu/intraktible/modeling/events"
	modelprojection "github.com/e6qu/intraktible/modeling/projection"
	"github.com/e6qu/intraktible/platform/eventlog"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestJobPauseResumeProgressAndReviewedRetryAreReplayable(t *testing.T) {
	ctx := context.Background()
	log, _ := testutil.NewLogStore(t)
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	const jobID = "backfill-job"
	if _, err := eventlog.AppendJSON(
		ctx, log, id.Org, id.Workspace, "modeler", events.StreamModeling,
		events.TypeBackfillRequested, now, events.BackfillRequested{
			JobID: jobID, BackfillID: "backfill-1",
			Request:     domain.BackfillRequest{EntityType: "applicant"},
			RequestHash: "request-hash",
		},
	); err != nil {
		t.Fatal(err)
	}
	handler := modelingcmd.NewHandler(log).WithNow(func() time.Time { return now })
	attempt, _, err := handler.ClaimJob(ctx, id, jobID, "worker-1", time.Minute)
	if err != nil || attempt != 1 {
		t.Fatalf("first claim attempt=%d err=%v", attempt, err)
	}
	if _, err := handler.ReportJobProgress(
		ctx, id, jobID, attempt, "worker-1", 2, 4, "materializing", 10, 0,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ReportJobProgress(
		ctx, id, jobID, attempt, "worker-1", 1, 4, "regressed", 9, 0,
	); err == nil || !strings.Contains(err.Error(), "monotonic") {
		t.Fatalf("progress regression error = %v", err)
	}
	if _, err := handler.PauseJob(ctx, id, jobID, "warehouse maintenance"); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.FinishPause(ctx, id, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.ResumeJob(ctx, id, jobID, "warehouse available"); err != nil {
		t.Fatal(err)
	}
	attempt, _, err = handler.ClaimJob(ctx, id, jobID, "worker-2", time.Minute)
	if err != nil || attempt != 2 {
		t.Fatalf("resumed claim attempt=%d err=%v", attempt, err)
	}
	if _, err := handler.FailJob(
		ctx, id, jobID, attempt, "worker-2", errors.New("invalid source"), false,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RetryJob(ctx, id, jobID, "source contract corrected"); err != nil {
		t.Fatal(err)
	}
	attempt, _, err = handler.ClaimJob(ctx, id, jobID, "worker-3", time.Minute)
	if err != nil || attempt != 3 {
		t.Fatalf("retried claim attempt=%d err=%v", attempt, err)
	}
	if _, err := handler.FailJob(
		ctx, id, jobID, attempt, "worker-3", errors.New("still invalid"), true,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.RetryJob(ctx, id, jobID, "try again"); err == nil ||
		!strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("exhausted retry error = %v", err)
	}

	st := store.NewMemory()
	if _, err := projection.New(log, st, modelprojection.Projector{}).RebuildTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	job, found, err := modelprojection.ReadJob(ctx, st, id, jobID)
	if err != nil || !found {
		t.Fatalf("job found=%v err=%v", found, err)
	}
	if job.State != "failed" || job.Attempt != 3 || job.Phase != "failed" ||
		job.ProgressPercent != 0 {
		t.Fatalf("replayed job = %+v", job)
	}
}
