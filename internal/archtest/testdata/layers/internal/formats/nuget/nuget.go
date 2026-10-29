package nuget

import "example.test/fix/internal/formats/npm"

// Bad imports a sibling format.
var _ = npm.Name
