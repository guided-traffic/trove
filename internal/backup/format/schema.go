package format

import _ "embed"

// ManifestSchema is the JSON Schema of manifest.json (R-BAK-003). Tests validate every manifest they write against it, so
// the Go type and the schema cannot drift.
//
//go:embed manifest.schema.json
var ManifestSchema []byte
