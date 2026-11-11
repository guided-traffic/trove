package ansible

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The route ansible.version.docs, the docs-blob document of a version (R-ANS-035), is declared for all three classes (routes.go,
// match.go, paths.go) and answers 501 ansible_not_implemented until the handlers of the documentation land. The file that builds them
// replaces this one: the local and virtual handlers read the rows, the remote handler is a metadata document of the engine.
func init() {
	for _, class := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		registerHandler(class, routeDocs, func(*Format) formats.HandlerFunc {
			return func(_ context.Context, rq *formats.Request, _ http.ResponseWriter) error {
				return notImplemented(routeDocs, rq.Repo.Class)
			}
		})
	}
}
