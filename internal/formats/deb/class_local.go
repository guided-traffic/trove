package deb

import "github.com/guided-traffic/trove/internal/formats"

// The local class (R-DEB-001): a repository that takes uploads of .deb files, lists them in suites and components, and serves the
// signed indexes it builds. It needs no adapter; its operations are the handlers of the routes it serves, which the work package of
// the local class registers (handlers_local.go, handlers_upload.go), and its hooks (hooks.go, inspect.go, serve.go, view.go).
func init() { registerClass(formats.Local, func(f *Format) { f.view = viewAdapter{} }) }
