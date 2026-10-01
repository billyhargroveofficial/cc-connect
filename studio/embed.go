// Package studio supplies the production React assets to the standalone server.
package studio

import (
	"embed"
	"io/fs"
)

// Build the React client before building the release binary. The tracked .keep
// allows Go tests and API-only development before the first frontend build.
//
//go:embed all:dist
var assets embed.FS

func Assets() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err) // the embedded directory is a compile-time invariant
	}
	return sub
}
