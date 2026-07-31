// SPDX-License-Identifier: AGPL-3.0-or-later

package governance

import (
	"testing"

	"github.com/e6qu/intraktible/case-manager/cases"
)

func TestAnnotateAssistEvidenceIncludesNewAttachments(t *testing.T) {
	view, err := annotateAssistEvidence(
		AssistView{
			AssistID: "assist-1", CaseID: "case-1",
			EvidenceSeq: 10, CurrentEvidenceSeq: 10,
		},
		cases.CaseView{
			CaseID: "case-1",
			Evidence: []cases.EvidenceLink{{
				EvidenceID: "decision-1", LinkedSeq: 10,
			}},
			Attachments: []cases.Attachment{{
				AttachmentID: "document-1", RegisteredSeq: 14,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.CurrentEvidenceSeq != 14 || !view.EvidenceStale {
		t.Fatalf("annotated assist = %+v", view)
	}
}

func TestAnnotateAssistEvidenceRejectsProjectionRegression(t *testing.T) {
	_, err := annotateAssistEvidence(
		AssistView{AssistID: "assist-1", CaseID: "case-1", EvidenceSeq: 10},
		cases.CaseView{
			CaseID: "case-1",
			Evidence: []cases.EvidenceLink{{
				EvidenceID: "decision-1", LinkedSeq: 9,
			}},
		},
	)
	if err == nil {
		t.Fatal("expected regressed case evidence projection to fail")
	}
}
