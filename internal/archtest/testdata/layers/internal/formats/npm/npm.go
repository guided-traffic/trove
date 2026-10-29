package npm

import (
	"example.test/fix/internal/blob"
	"example.test/fix/internal/errs"
	"example.test/fix/internal/formats"
	"example.test/fix/internal/formats/npm/parse"
	"example.test/fix/internal/meta/postgres"
)

// Name is the format ID.
const Name = "npm"

// Bad reaches infrastructure and the object store from a format; the contract,
// errs and its own subpackage are fine.
var (
	_ = postgres.Store{}
	_ = blob.Store{}
	_ formats.Format
	_ errs.Kind
	_ = parse.Version
)
