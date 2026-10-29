// Package v1 implements the management API under /api/v1/ (R-API-001). The
// contract is api/openapi.yaml; the handlers implement the interface that
// oapi-codegen derives from it into the gen subpackage, so an operation without
// a handler does not compile.
//
// Workflow: edit api/openapi.yaml, run `go generate ./internal/api/...` (which
// refreshes the embedded copy and the generated code), then implement the new
// method of gen.ServerInterface. docs/developer/http-api.md has the details.
package v1

//go:generate cp ../../../api/openapi.yaml openapi.yaml
