// SPDX-License-Identifier: AGPL-3.0-or-later

package decisionengine_test

import (
	"context"

	"github.com/e6qu/intraktible/platform/effect"
	"github.com/e6qu/intraktible/platform/identity"
)

// The in-memory providers used across command tests are deterministic fixtures.
// Declaring that property explicitly keeps recovery semantics part of every
// provider contract instead of letting test doubles bypass it.
func (stubConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*recordingConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*fakeConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*recordingShadowConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*countedConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ProviderIdempotent, nil
}

func (*erroringConnector) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.AtLeastOnce, nil
}

func (stubAgent) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*recordingAgent) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.ReplaySafe, nil
}

func (*erroringAgent) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.AtLeastOnce, nil
}

func (*erroringModel) EffectDelivery(context.Context, identity.Identity, string) (effect.Delivery, error) {
	return effect.AtLeastOnce, nil
}
