// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"

	"golang.org/x/oauth2"
)

func TestDiscoveredAuthStyle(t *testing.T) {
	for _, test := range []struct {
		name    string
		methods []string
		want    oauth2.AuthStyle
	}{
		{name: "Shauth client secret post", methods: []string{"client_secret_post"}, want: oauth2.AuthStyleInParams},
		{name: "HTTP Basic", methods: []string{"client_secret_basic"}, want: oauth2.AuthStyleInHeader},
		{name: "post preferred when both are advertised", methods: []string{"client_secret_basic", "client_secret_post"}, want: oauth2.AuthStyleInParams},
		{name: "provider discovery omitted methods", want: oauth2.AuthStyleAutoDetect},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := discoveredAuthStyle(test.methods); got != test.want {
				t.Fatalf("discoveredAuthStyle(%v) = %v, want %v", test.methods, got, test.want)
			}
		})
	}
}
