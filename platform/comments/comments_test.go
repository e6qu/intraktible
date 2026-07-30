// SPDX-License-Identifier: AGPL-3.0-or-later

package comments_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/e6qu/intraktible/platform/comments"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/store"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestCommentThreadOverHTTP(t *testing.T) {
	log, st := testutil.NewLogStore(t)
	svc := comments.New(comments.NewHandler(log), st)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "alice"}
	api := testutil.StartAPI(t, log, st, "test-key", id, svc.Routes, comments.Projector{})

	// Two comments on one subject; an empty body is rejected.
	api.Request(t, http.MethodPost, "/v1/comments/deployment_request/r1", map[string]any{"body": "looks risky"}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/comments/deployment_request/r1", map[string]any{"body": "checked the backtest, ok"}, http.StatusCreated, nil)
	api.Request(t, http.MethodPost, "/v1/comments/deployment_request/r1", map[string]any{"body": "   "}, http.StatusBadRequest, nil)
	api.Request(t, http.MethodPost, "/v1/comments/deployment_request/r1", map[string]any{
		"body": "customer SSN 123-45-6789",
	}, http.StatusBadRequest, nil)
	// A different subject keeps its own thread.
	api.Request(t, http.MethodPost, "/v1/comments/decision/d9", map[string]any{"body": "why declined?"}, http.StatusCreated, nil)

	type comment struct {
		Body   string `json:"body"`
		Author string `json:"author"`
	}
	var got struct {
		Comments []comment `json:"comments"`
	}
	if !testutil.Eventually(t, func() bool {
		got.Comments = nil
		api.Request(t, http.MethodGet, "/v1/comments/deployment_request/r1", nil, http.StatusOK, &got)
		return len(got.Comments) == 2
	}) {
		t.Fatalf("thread did not materialize: %+v", got.Comments)
	}
	// Chronological order, attributed to the caller.
	if got.Comments[0].Body != "looks risky" || got.Comments[1].Body != "checked the backtest, ok" {
		t.Fatalf("comments out of order: %+v", got.Comments)
	}
	if got.Comments[0].Author != "alice" {
		t.Fatalf("comment not attributed: %+v", got.Comments[0])
	}

	// A reply carries its parent comment's id (one level of threading).
	var firstID struct {
		Comments []struct {
			CommentID string `json:"comment_id"`
		} `json:"comments"`
	}
	api.Request(t, http.MethodGet, "/v1/comments/deployment_request/r1", nil, http.StatusOK, &firstID)
	parent := firstID.Comments[0].CommentID
	api.Request(t, http.MethodPost, "/v1/comments/deployment_request/r1",
		map[string]any{"body": "agree, the retry masks it", "parent_id": parent}, http.StatusCreated, nil)
	var threaded struct {
		Comments []struct {
			Body     string `json:"body"`
			ParentID string `json:"parent_id"`
		} `json:"comments"`
	}
	if !testutil.Eventually(t, func() bool {
		threaded.Comments = nil
		api.Request(t, http.MethodGet, "/v1/comments/deployment_request/r1", nil, http.StatusOK, &threaded)
		return len(threaded.Comments) == 3
	}) {
		t.Fatalf("reply did not materialize: %+v", threaded.Comments)
	}
	var reply int
	for _, c := range threaded.Comments {
		if c.ParentID == parent {
			reply++
		}
	}
	if reply != 1 {
		t.Fatalf("expected exactly one reply to %s: %+v", parent, threaded.Comments)
	}

	// Review discussions have an explicit, replayable resolution state.
	resolvePath := "/v1/comments/deployment_request/r1/" + parent + "/resolve"
	api.Request(t, http.MethodPost, resolvePath, map[string]any{}, http.StatusOK, nil)
	// Lost-response retry is a no-op success, not a duplicate transition.
	api.Request(t, http.MethodPost, resolvePath, map[string]any{}, http.StatusOK, nil)
	var resolution struct {
		Comments []struct {
			CommentID  string `json:"comment_id"`
			Resolved   bool   `json:"resolved"`
			ResolvedBy string `json:"resolved_by"`
		} `json:"comments"`
	}
	if !testutil.Eventually(t, func() bool {
		resolution.Comments = nil
		api.Request(
			t, http.MethodGet, "/v1/comments/deployment_request/r1",
			nil, http.StatusOK, &resolution,
		)
		for _, comment := range resolution.Comments {
			if comment.CommentID == parent {
				return comment.Resolved && comment.ResolvedBy == "alice"
			}
		}
		return false
	}) {
		t.Fatalf("comment did not resolve: %+v", resolution.Comments)
	}
	replayed := store.NewMemory()
	if _, err := projection.New(
		log, replayed, comments.Projector{},
	).RebuildTo(context.Background(), 0); err != nil {
		t.Fatalf("rebuild resolved discussion: %v", err)
	}
	replayedThread, err := comments.List(
		context.Background(), replayed, id, "deployment_request", "r1",
	)
	if err != nil {
		t.Fatalf("read replayed discussion: %v", err)
	}
	resolvedAfterReplay := false
	for _, comment := range replayedThread {
		if comment.CommentID == parent {
			resolvedAfterReplay = comment.Resolved && comment.ResolvedBy == "alice"
		}
	}
	if !resolvedAfterReplay {
		t.Fatalf("resolved state did not survive replay: %+v", replayedThread)
	}
	api.Request(
		t, http.MethodPost,
		"/v1/comments/deployment_request/r1/"+parent+"/reopen",
		map[string]any{}, http.StatusOK, nil,
	)
	if !testutil.Eventually(t, func() bool {
		resolution.Comments = nil
		api.Request(
			t, http.MethodGet, "/v1/comments/deployment_request/r1",
			nil, http.StatusOK, &resolution,
		)
		for _, comment := range resolution.Comments {
			if comment.CommentID == parent {
				return !comment.Resolved && comment.ResolvedBy == ""
			}
		}
		return false
	}) {
		t.Fatalf("comment did not reopen: %+v", resolution.Comments)
	}
	api.Request(
		t, http.MethodPost,
		"/v1/comments/deployment_request/r1/unknown/resolve",
		map[string]any{}, http.StatusBadRequest, nil,
	)

	// The other subject's thread is isolated.
	var other struct {
		Comments []comment `json:"comments"`
	}
	api.Request(t, http.MethodGet, "/v1/comments/decision/d9", nil, http.StatusOK, &other)
	if len(other.Comments) != 1 || other.Comments[0].Body != "why declined?" {
		t.Fatalf("subject isolation broken: %+v", other.Comments)
	}
}
