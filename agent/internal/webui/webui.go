// Package webui embeds the built UI (a copy of packages/ui/dist placed in ./dist by scripts/build-agent.sh).
// Without a UI build, dist holds only a placeholder and Available reports false, so `go build` always works.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed all:dist
var embedded embed.FS

// Same policy as the --static-dir handler in internal/wire.
const csp = "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com"

func root() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return embedded
	}
	return sub
}

// Available reports whether a UI build is embedded.
func Available() bool { return available(root()) }

func available(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, "index.html")
	return err == nil && !st.IsDir()
}

// Handler serves the embedded UI with an SPA fallback to index.html for extension-less paths.
func Handler() http.Handler { return handler(root()) }

func handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		p := path.Clean("/" + r.URL.Path)
		if path.Ext(p) == "" && p != "/" {
			if _, err := fs.Stat(fsys, p[1:]); err != nil {
				r2 := *r
				r2.URL.Path = "/"
				files.ServeHTTP(w, &r2)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
