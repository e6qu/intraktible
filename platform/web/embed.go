// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web embeds the built UI into the binary so a single artifact serves
// API + UI. Phase 0 ships a minimal placeholder page; the SvelteKit build will
// emit into assets/ and be embedded the same way.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets
var assets embed.FS

// Handler serves the embedded UI at the root.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // embed is compile-time; a failure here is a build bug
	}
	return http.FileServer(http.FS(sub))
}
