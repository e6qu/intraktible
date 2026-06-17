// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web embeds the built UI into the binary so a single artifact serves
// API + UI. The committed assets/ holds a placeholder index.html so `go build`
// always works; `make web` overwrites assets/ with the real SvelteKit build for a
// full artifact (the Dockerfile does this in an isolated layer). The UI is an SPA,
// so Handler falls back to index.html for client-side routes.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// all: is required — SvelteKit emits its JS/CSS under _app/, and a bare
// //go:embed skips files/dirs whose names begin with '_' or '.', which would
// embed index.html but none of the assets (a blank page).
//
//go:embed all:assets
var assets embed.FS

// Handler serves the embedded UI at the root with SPA fallback: a request for a
// real embedded file is served as-is; anything else returns index.html (200) so
// client-side routes like /engine or /cases/{id} load the app shell. It is mounted
// after the API routes, so it only sees non-API paths.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // embed is compile-time; a failure here is a build bug
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := strings.TrimPrefix(r.URL.Path, "/"); name != "" && exists(sub, name) {
			files.ServeHTTP(w, r)
			return
		}
		// Not a real file → serve the SPA shell (index.html) with a 200, by
		// routing the file server at the root.
		shell := r.Clone(r.Context())
		shell.URL.Path = "/"
		files.ServeHTTP(w, shell)
	})
}

// exists reports whether name is a regular file in the embedded FS.
func exists(fsys fs.FS, name string) bool {
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}
