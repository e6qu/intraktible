// SPDX-License-Identifier: AGPL-3.0-or-later

package effect_test

import (
	"context"
	"testing"

	"github.com/e6qu/intraktible/platform/effect"
)

func TestRequestRoundTripAndValidation(t *testing.T) {
	if _, err := effect.WithRequest(context.Background(), effect.Request{}); err == nil {
		t.Fatal("empty request was accepted")
	}
	ctx, err := effect.WithRequest(context.Background(), effect.Request{Key: "fx-1", Attempt: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := effect.FromContext(ctx)
	if !ok || got.Key != "fx-1" || got.Attempt != 2 {
		t.Fatalf("request = %+v, ok=%v", got, ok)
	}
}

func TestDeliveryValues(t *testing.T) {
	for _, delivery := range []effect.Delivery{
		effect.ReplaySafe, effect.ProviderIdempotent, effect.AtLeastOnce,
	} {
		if !delivery.Valid() {
			t.Fatalf("%q is not valid", delivery)
		}
	}
	if effect.Delivery("exactly_once").Valid() {
		t.Fatal("unsupported exactly-once claim was accepted")
	}
}
