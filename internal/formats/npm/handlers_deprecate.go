package npm

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/publish"
)

// metadataUpdate is the PUT of a package document without an attachment: npm
// deprecate (R-NPM-018, P1) and the partial update of npm owner (PKG-NPM-022,
// deferred). The body was scanned within the metadata limit of a publish. Until deprecation is built the
// answer is the 404 that R-NPM-043 prescribes for an unimplemented PUT: npm's fetch layer retries a PUT on
// any 5xx, so a 501 here cost `npm deprecate` 70 seconds of retries (found by the client matrix, E2.1).
func (f *Format) metadataUpdate(_ context.Context, _ *formats.Request, _ http.ResponseWriter, _ name.Name, env *publish.Envelope) error {
	if len(env.Versions) == 0 {
		return manifest.BadPublish("maintainer lists are not supported; ownership is managed in trove")
	}
	return errs.E(errs.NotFound, errs.CodeNotFound, "Deprecating a version is not available yet.")
}
