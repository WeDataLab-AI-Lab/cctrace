package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var staticFiles embed.FS

// HasDashboard reports whether the embedded assets hold a real web build.
//
// The test and lint make targets create internal/web/dist/.gitkeep so that the
// embed directive above has something to match on a fresh checkout. That
// placeholder makes every build path succeed — `make build`, CI's
// `go build ./...`, and both Dockerfiles, none of which build the web assets
// themselves — while producing a server that serves an empty dashboard.
// Callers use this to refuse to start rather than ship that silently.
func HasDashboard() bool {
	return hasDashboard(staticFiles)
}

// A real export emits an entry document that loads the hashed asset tree under
// dist/_next, and the tree itself. Checking that both exist is not enough: an
// emptied or truncated index.html next to a leftover asset tree still serves a
// blank page. Requiring the document to reference the tree ties the two
// together, so the pair has to come from one build.
//
// This verifies that a build produced these files, not that the UI is correct —
// no cheap check can do the latter, and corruption after a verified build is
// outside what a startup guard can see.
func hasDashboard(fsys fs.FS) bool {
	index, err := fs.ReadFile(fsys, "dist/index.html")
	if err != nil || !bytes.Contains(index, []byte("/_next/")) {
		return false
	}
	fi, err := fs.Stat(fsys, "dist/_next")
	return err == nil && fi.IsDir()
}

// Handler returns an http.Handler that serves the embedded static files.
func Handler() http.Handler {
	dist, err := fs.Sub(staticFiles, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(dist))
}
