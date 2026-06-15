// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/hello/domain"
)

func TestSayHelloValidate(t *testing.T) {
	if err := (domain.SayHello{Name: "ada"}).Validate(); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	for _, blank := range []string{"", "   ", "\t\n"} {
		if err := (domain.SayHello{Name: blank}).Validate(); err == nil {
			t.Fatalf("blank name %q should be rejected", blank)
		}
	}
}

func TestRecordTrims(t *testing.T) {
	got := domain.Record(domain.SayHello{Name: "  ada  "})
	if got.Name != "ada" {
		t.Fatalf("Record did not trim: %q", got.Name)
	}
}
