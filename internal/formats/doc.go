// Package formats is the contract of the package formats and their registry
// (R-ARCH-008, R-ARCH-010, R-REPO-003).
//
// A format is a [Format]: its [Descriptor], its routes below /api/<id>/<projectKey>/<repoKey>/,
// the classification of its paths, the storage hooks that keep its package rows
// in step with the items (OnCommit, OnItemsRemoved), the adapters of the remote
// and the virtual class, the exposure and view adapters, the client setup
// snippets and the rendering of an error in the protocol's own shape. It meets the
// core through the [Host]: authorization, the upload pipeline, the item and
// package stores, the remote engine, virtual resolution and the link builder; the
// Host holds no request state, the [Request] carries it.
//
// The facts of a descriptor, the format's package-name rule, its exposure facts and
// the schema of its repository settings are the format catalog
// (internal/formats/catalog, a leaf): Register puts them there, and the repository
// model and the decision point read them there, so no layer keeps a copy. The
// conformance kit (internal/formats/formattest) runs every format's routes, errors,
// hooks and headers through the contract (R-ARCH-010).
//
// ContractVersion is 1, and the contract is frozen at it since the end of milestone
// M3 (E3.3, ADR-141). A change of the exported surface is an ADR of its own: the
// surface file internal/formats/testdata/contract.txt fails the test until the ADR
// and the regenerated file come in one change. A change adds - an optional interface
// that CapabilitiesOf finds, a default in Base or RemoteBase, a service of the Host -
// and the version rises only when the meaning of a method changes or a method leaves,
// with every format moved in the same change (R-ARCH-025, Q-ARCH-256, Q-ARCH-280).
//
// A format imports this package and the leaf packages catalog, errs, config and
// observability, and the untrusted-content limits (internal/formats/untrusted);
// the allowlist of R-ARCH-008 is enforced by archtest. It opens no connection, runs
// no SQL and touches no object store. The core types it needs are spelled by this
// package (aliases), so it imports no internal/authz, internal/repository or
// internal/meta.
package formats
