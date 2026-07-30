// SPDX-License-Identifier: AGPL-3.0-or-later

package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
	"github.com/e6qu/intraktible/case-manager/command"
	"github.com/e6qu/intraktible/case-manager/domain"
	"github.com/e6qu/intraktible/case-manager/events"
	"github.com/e6qu/intraktible/case-manager/service"
	"github.com/e6qu/intraktible/platform/identity"
	"github.com/e6qu/intraktible/platform/projection"
	"github.com/e6qu/intraktible/platform/testutil"
)

func TestAttachmentStorageReferenceRequiresAuditedAccess(t *testing.T) {
	ctx := context.Background()
	log, st := testutil.NewLogStore(t)
	id := identity.Identity{Org: "demo", Workspace: "main", Actor: "operator"}
	handler := command.NewHandler(log)
	caseID, _, err := handler.RequestReview(ctx, id, domain.RequestReview{
		CompanyName: "Acme", CaseType: "legacy",
	})
	if err != nil {
		t.Fatal(err)
	}
	const storageRef = "s3://approved/cases/registry"
	if _, err := handler.RegisterAttachment(ctx, id, events.CaseAttachmentRegistered{
		CaseID: caseID, AttachmentID: "registry", Name: "registry.pdf",
		MediaType: "application/pdf", Size: 42,
		SHA256:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StorageRef: storageRef,
	}); err != nil {
		t.Fatal(err)
	}
	if err := projection.New(log, st, cases.Projector{}).Start(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	service.New(handler, st).Routes(mux)

	read := httptest.NewRecorder()
	readRequest := httptest.NewRequest(http.MethodGet, "/v1/cases/"+caseID, http.NoBody)
	mux.ServeHTTP(read, readRequest.WithContext(identity.With(readRequest.Context(), id)))
	if read.Code != http.StatusOK {
		t.Fatalf("case read status = %d, body=%s", read.Code, read.Body.String())
	}
	var view cases.CaseView
	if err := json.Unmarshal(read.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Attachments) != 1 || view.Attachments[0].StorageRef != "" {
		t.Fatalf("ordinary case read leaked storage capability: %+v", view.Attachments)
	}

	body, err := json.Marshal(map[string]string{"purpose": "case review"})
	if err != nil {
		t.Fatal(err)
	}
	access := httptest.NewRecorder()
	accessRequest := httptest.NewRequest(
		http.MethodPost, "/v1/cases/"+caseID+"/attachments/registry/access", bytes.NewReader(body),
	)
	accessRequest.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(access, accessRequest.WithContext(identity.With(accessRequest.Context(), id)))
	if access.Code != http.StatusAccepted {
		t.Fatalf("attachment access status = %d, body=%s", access.Code, access.Body.String())
	}
	var response struct {
		StorageRef string `json:"storage_ref"`
	}
	if err := json.Unmarshal(access.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.StorageRef != storageRef {
		t.Fatalf("audited storage_ref = %q, want %q", response.StorageRef, storageRef)
	}
	recorded, err := log.Read(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	accessEvents := 0
	for _, event := range recorded {
		if event.Type == events.TypeCaseAttachmentAccessed {
			accessEvents++
		}
	}
	if accessEvents != 1 {
		t.Fatalf("attachment access events = %d, want one", accessEvents)
	}
}
