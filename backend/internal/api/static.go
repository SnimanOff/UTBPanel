package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Static serves the built frontend from dir. Unknown paths get index.html
// (the SPA router handles them). Hashed assets are cached for a year,
// index.html never.
func Static(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/api/") || p == "/api" {
			WriteError(w, http.StatusNotFound, "not_found", "no such API route")
			return
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if fi, err := os.Stat(full); err != nil || fi.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}
