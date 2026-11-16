// Package web embeds the built admin UI (web/dist, produced by `make web`).
//
// The embed pattern `all:dist*` also matches the committed
// dist-fallback.html, so the Go build never fails on a fresh clone where the
// UI has not been built yet (web/dist is gitignored). In that case Dist
// returns nil and the server serves the fallback page instead.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist*
var embedded embed.FS

// Dist returns the built UI (contents of web/dist), or nil when the UI was
// not built before compiling.
func Dist() fs.FS {
	if _, err := fs.Stat(embedded, "dist/index.html"); err != nil {
		return nil
	}
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// FallbackHTML is served for every UI route when Dist returns nil.
func FallbackHTML() []byte {
	b, _ := embedded.ReadFile("dist-fallback.html")
	return b
}
