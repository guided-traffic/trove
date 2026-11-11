package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// The documentation operations of the screens (R-ANS-036, R-UI-060 to R-UI-064, Q-API-260 to Q-API-263): the tree, one entry and one file
// of the documentation of a package version. Each is declared wiki content in the document (x-trove-part: wiki, [Operation.Part]) and
// reads rows only, never an upstream.
//
// Status: the contract of work package WP0 (docs/projektplan/galaxy-docs-work-packages.md): the operations, their schemas and their
// parameters are in api/openapi.yaml and the generated server interface; the handlers answer 503 unavailable until the step that builds
// them has landed (the contract test of R-API-002 refuses any other status of 500 or above on a GET).

// docsNotBuilt is the answer of an operation of the documentation until its handler is built.
func docsNotBuilt(w http.ResponseWriter, r *http.Request, operation string) {
	httpserver.Problem(w, r, unavailable("The operation "+operation))
}

// GetPackageVersionDocs implements GET /api/v1/packages/{packageId}/versions/{version}/docs (R-ANS-036).
func (s *Server) GetPackageVersionDocs(w http.ResponseWriter, r *http.Request, _ gen.PackageId, _ gen.PackageVersionPath, _ gen.GetPackageVersionDocsParams) {
	docsNotBuilt(w, r, "getPackageVersionDocs")
}

// GetPackageVersionDocEntry implements GET /api/v1/packages/{packageId}/versions/{version}/docs/entries/{type}/{name} (R-ANS-036).
func (s *Server) GetPackageVersionDocEntry(w http.ResponseWriter, r *http.Request, _ gen.PackageId, _ gen.PackageVersionPath, _, _ string, _ gen.GetPackageVersionDocEntryParams) {
	docsNotBuilt(w, r, "getPackageVersionDocEntry")
}

// GetPackageVersionDocFile implements GET /api/v1/packages/{packageId}/versions/{version}/docs/files/{file} (R-ANS-036).
func (s *Server) GetPackageVersionDocFile(w http.ResponseWriter, r *http.Request, _ gen.PackageId, _ gen.PackageVersionPath, _ string, _ gen.GetPackageVersionDocFileParams) {
	docsNotBuilt(w, r, "getPackageVersionDocFile")
}
