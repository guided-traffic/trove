// Package manual embeds the pages of the trove manual (docs/manual) into the binary,
// so that the help area of the UI always shows the manual of the running version
// (R-API-054, R-UI-050). internal/manual renders them; this package only holds the
// files, because go:embed cannot reach a directory above the package.
package manual

import "embed"

// Files holds the pages of the two sections, `<section>/<page>.md`.
//
//go:embed user/*.md admin/*.md
var Files embed.FS
