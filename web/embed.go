// Package web embeds the built frontend into the binary (SPEC.md §8).
//
// The embedded directory is Vite's output, and it is committed: `go build` has
// to work in a clone with no Node toolchain. Source maps are excluded, since
// they would be four times the size of the bundle and are only useful against
// sources the reader already has. Re-run `npm run build` after changing the
// frontend and commit the result.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built frontend rooted at its index.html.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: the embed directive guarantees dist exists.
		panic(err)
	}
	return sub
}

// Built reports whether a real frontend build is embedded. Without one the
// binary still runs — the API works, and the UI is replaced by instructions.
func Built() bool {
	_, err := fs.Stat(dist, "dist/index.html")
	return err == nil
}
