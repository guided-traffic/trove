package ansible

import "github.com/guided-traffic/trove/internal/formats"

// The local class (R-ANS-001): a repository that takes the publish of collection archives, validates them, stores them and serves the
// Galaxy documents it computes from the rows of its data port. It needs no adapter; its operations are the handlers of the routes it
// serves, which handlers_local.go registers, and its hooks (hooks.go, inspect.go, view.go).
func init() { registerClass(formats.Local, func(f *Format) { f.view = viewAdapter{} }) }
