package formats

import (
	"example.test/fix/internal/blob"
	"example.test/fix/internal/errs"
)

// Format stands in for the contract; importing the object store is wrong.
type Format interface{ Kind() errs.Kind }

var _ = blob.Store{}
