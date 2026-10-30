package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The tarball download (R-NPM-015). The handler is built with the documents; until
// then the route answers 501. TarballFile, VersionOfFile and TarballPath (paths.go)
// are the file-name rules it applies.

func (f *Format) handleTarball(context.Context, *formats.Request, http.ResponseWriter) error {
	return pendingHandler("The tarball download")
}
