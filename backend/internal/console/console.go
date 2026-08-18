// Package console carries the parent web console as files compiled into the server binary.
//
// Embedding rather than shipping a directory is a deployment decision with a security consequence:
// the container image has no writable web root, so there is no path where a running pod serves
// different JavaScript than the one that was built and signed. It also means the console and the API
// can never be at different versions — there is one artefact.
//
// There is deliberately no build step. The files under assets/ are hand-written and served
// byte-for-byte, so what a reviewer reads here is exactly what a browser executes.
package console

import (
	"embed"
	"io/fs"
)

//go:embed assets
var assets embed.FS

// FS returns the console's files rooted at the console itself, so "index.html" — not
// "assets/index.html" — is the path a caller uses.
func FS() (fs.FS, error) {
	return fs.Sub(assets, "assets")
}
