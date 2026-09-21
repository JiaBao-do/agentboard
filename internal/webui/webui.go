// Package webui embeds the browser UI: static HTML, CSS and JavaScript
// bootstrap, plus app.wasm (the UI itself, written in Go) and the matching
// wasm_exec.js from the Go toolchain that built it.
//
// The built files are committed so that "go install" works without a
// WebAssembly build step. Regenerate them after changing cmd/agentboard-ui
// or model with:
//
//	go generate ./internal/webui
package webui

import (
	"embed"
	"io/fs"
)

//go:generate go run ../buildui

//go:embed dist
var dist embed.FS

// FS returns the UI files, rooted so that index.html is at the top level.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time; cannot fail
	}
	return sub
}
