// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"strings"
	"testing"
)

// TestEgressAlwaysBlocksCloudMetadata: AllowPrivate exists so a connector can reach
// an internal service. It must not also open the one internal address whose entire
// content is the instance's IAM credentials — an HTTP connector pointed at it would
// exfiltrate them into a tenant-readable FetchView.
func TestEgressAlwaysBlocksCloudMetadata(t *testing.T) {
	metadata := []string{
		"169.254.169.254:80",
		"169.254.170.2:80",
		"[fd00:ec2::254]:80",
		"[fe80::a9fe:a9fe]:80",
	}
	for _, allowPrivate := range []bool{false, true} {
		p := EgressPolicy{AllowPrivate: allowPrivate}
		for _, address := range metadata {
			err := p.control("tcp", address, nil)
			if err == nil {
				t.Fatalf("AllowPrivate=%v dialed the metadata service at %s", allowPrivate, address)
			}
			if !strings.Contains(err.Error(), "metadata") {
				t.Fatalf("AllowPrivate=%v blocked %s for the wrong reason: %v", allowPrivate, address, err)
			}
		}
	}
}

// TestEgressAllowPrivateStillReachesInternalHosts guards the escape hatch itself:
// blocking metadata must not have blocked the private hosts it exists to allow.
func TestEgressAllowPrivateStillReachesInternalHosts(t *testing.T) {
	p := EgressPolicy{AllowPrivate: true}
	for _, address := range []string{"127.0.0.1:8080", "10.0.0.5:443", "192.168.1.10:80"} {
		if err := p.control("tcp", address, nil); err != nil {
			t.Fatalf("AllowPrivate should permit %s: %v", address, err)
		}
	}
	blocked := EgressPolicy{}
	for _, address := range []string{"127.0.0.1:8080", "10.0.0.5:443"} {
		if err := blocked.control("tcp", address, nil); err == nil {
			t.Fatalf("the default policy must block %s", address)
		}
	}
}
