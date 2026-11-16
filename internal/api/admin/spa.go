package admin

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// spaHandler serves the embedded single-page app: existing files are served
// as-is, every other path gets index.html so client-side routing works.
// /admin/api/* never reaches this handler (the API router owns that prefix).
type spaHandler struct {
	ui       fs.FS
	fallback []byte
}

func newSPAHandler(ui fs.FS, fallback []byte) *spaHandler {
	if len(fallback) == 0 {
		fallback = []byte("<!doctype html><title>Kadr</title><p>Admin UI not built.</p>")
	}
	return &spaHandler{ui: ui, fallback: fallback}
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/admin/api") {
		http.NotFound(w, r)
		return
	}
	if h.ui == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.fallback))
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if h.serveFile(w, r, name) {
		return
	}
	// Missing hashed assets must 404, not return HTML with status 200.
	if strings.HasPrefix(name, "assets/") {
		http.NotFound(w, r)
		return
	}
	if !h.serveFile(w, r, "index.html") {
		http.NotFound(w, r)
	}
}

func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.ui.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return false
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		rs = bytes.NewReader(b)
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	// Embedded files have a zero ModTime; ServeContent then skips Last-Modified.
	http.ServeContent(w, r, name, st.ModTime(), rs)
	return true
}
