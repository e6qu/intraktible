// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"
	"time"
)

// White-box: the KeyStatus predicate is the single authority for "is this key
// usable", so exercise the three states and the revoked-wins-over-expired rule.
func TestManagedAPIKeyStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	active := ManagedAPIKey{}
	if active.Status(now) != KeyActive || !active.Usable(now) {
		t.Fatalf("a key with no expiry/revocation must be active+usable")
	}
	if (ManagedAPIKey{ExpiresAt: &future}).Status(now) != KeyActive {
		t.Fatalf("a key expiring in the future is active")
	}
	if (ManagedAPIKey{ExpiresAt: &past}).Status(now) != KeyExpired {
		t.Fatalf("a key past its expiry is expired")
	}
	if (ManagedAPIKey{ExpiresAt: &past}).Usable(now) {
		t.Fatalf("an expired key must not be usable")
	}
	if (ManagedAPIKey{RevokedAt: &now}).Status(now) != KeyRevoked {
		t.Fatalf("a revoked key is revoked")
	}
	// Revocation wins over expiry.
	if (ManagedAPIKey{RevokedAt: &now, ExpiresAt: &past}).Status(now) != KeyRevoked {
		t.Fatalf("revocation must win over expiry")
	}
}
