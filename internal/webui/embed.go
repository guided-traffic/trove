package webui

import (
	"embed"
	"io/fs"
)

// dist holds the embedded UI. The committed dist/index.html is a placeholder, so `go build`
// works without Node.js. `npm run build:embed` in web/ adds one directory per locale
// (dist/en/, dist/de/, ...), which are ignored by git and picked up by release builds.
//
//go:embed all:dist
var dist embed.FS

// embeddedRoot returns the embedded UI rooted at dist/.
func embeddedRoot() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
