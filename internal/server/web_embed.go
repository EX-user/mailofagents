package server

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFiles embed.FS

// assetlinksJSON is the Digital Asset Links statement served at
// /.well-known/assetlinks.json (v0.6.26 TWA domain verification). The embed
// true source is internal/server/wellknown/assetlinks.json —
// deploy/twa/assetlinks.json is the TWA build reference; keep the two in
// sync when the signing certificate changes (go:embed cannot escape the
// package directory).
//
//go:embed wellknown/assetlinks.json
var assetlinksJSON []byte

// staticSubFS strips the "static/" prefix so the embedded files are served at
// their own names (e.g. /static/style.css → static/style.css in the embed).
var staticSubFS fs.FS

func init() {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// static/ is compiled in; Sub cannot fail at runtime.
		panic("server: embed fs.Sub failed: " + err.Error())
	}
	staticSubFS = sub
}
