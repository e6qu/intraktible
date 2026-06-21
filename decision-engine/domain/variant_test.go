// SPDX-License-Identifier: AGPL-3.0-or-later

package domain_test

import (
	"testing"

	"github.com/e6qu/intraktible/decision-engine/domain"
)

func TestVariantValid(t *testing.T) {
	for _, v := range []domain.Variant{domain.VariantChampion, domain.VariantChallenger} {
		if !v.Valid() {
			t.Fatalf("variant %q should be valid", v)
		}
	}
	for _, v := range []domain.Variant{"", "control", "Champion"} {
		if v.Valid() {
			t.Fatalf("variant %q should be invalid", v)
		}
	}
}
