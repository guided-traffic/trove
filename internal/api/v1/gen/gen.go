// Package gen holds the code oapi-codegen derives from api/openapi.yaml: the
// request and response models and the net/http server interface. Handlers in
// the parent package implement ServerInterface, so an operation without a
// handler does not compile. Run `go generate ./internal/api/...`; the output is
// committed and never edited.
package gen

//go:generate go tool oapi-codegen -config cfg.yaml ../../../../api/openapi.yaml
