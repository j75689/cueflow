// Package webui embeds the static dashboard build (web/out, copied to ./dist by `make web`).
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the exported Next.js app: /x/ -> x/index.html, /x -> x.html or x/index.html,
// unknown paths -> 404.html. Hashed assets under /_next/ are cached long-term.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(p, "/")
		if strings.HasPrefix(p, "/_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")

		if exists(root, name) && !isDir(root, name) {
			files.ServeHTTP(w, r)
			return
		}
		for _, candidate := range []string{path.Join(name, "index.html"), name + ".html"} {
			if name == "" {
				candidate = "index.html"
			}
			if exists(root, candidate) {
				serveFile(w, r, root, candidate, http.StatusOK)
				return
			}
		}
		if !exists(root, "index.html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(notBuilt))
			return
		}
		if exists(root, "404.html") {
			serveFile(w, r, root, "404.html", http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	})
}

func exists(root fs.FS, name string) bool {
	if name == "" {
		return false
	}
	_, err := fs.Stat(root, name)
	return err == nil
}

func isDir(root fs.FS, name string) bool {
	st, err := fs.Stat(root, name)
	return err == nil && st.IsDir()
}

func serveFile(w http.ResponseWriter, r *http.Request, root fs.FS, name string, code int) {
	b, err := fs.ReadFile(root, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>CueFlow</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>CueFlow</h1><p>The API is running, but this binary was built without the dashboard.</p>
<p>Run <code>make build</code> (or <code>make web</code> then <code>go build ./cmd/cueflow</code>) to embed it.</p></body>`
