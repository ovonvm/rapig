// Package web embeds the built single page application into the binary.
//
// The Vite build writes web/dist; scripts/build.sh runs it before `go build`,
// and web/dist/.keep keeps the embed pattern valid on a fresh checkout. When
// dist/index.html is absent (plain `go build` before any frontend build) the
// HTTP server reports frontend_not_built instead of failing to compile.
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the SPA file system rooted at the dist directory.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: open embedded dist: %w", err)
	}
	return sub, nil
}
