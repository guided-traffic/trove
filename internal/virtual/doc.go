// Package virtual is the virtual engine (R-REPO-019, R-REPO-020, R-REPO-048, R-ARCH-015): for one
// request to a virtual repository it decides which members take part for this caller
// ([Resolver.Plan]), walks the tiers of an artifact ([Resolver.Artifact]), merges the documents of
// the members through the merged-metadata cache ([Resolver.Metadata]), resolves the deploy target
// ([Resolver.DeployTarget]), decides the shadowing check of a first publish
// ([Resolver.ShadowCheck]) and explains a resolution without transferring content
// ([Resolver.Explain]).
//
// The package knows no format: a format's part is the [Merger], which formathost builds from the
// format's VirtualAdapter, and its tiers are the [Tiers] that formathost builds from the Host. It
// imports no engine either: the remote engine is the [Remotes] interface, declared here by what this
// package asks of it, so the proxy engine and the fakes of the tests are interchangeable.
// docs/developer/virtual.md lists the signatures.
//
// The package keeps one piece of state, the merged-metadata cache of a replica (R-ARCH-018). Every
// entry is self-validating: it records the rev_seq of each eligible local member and is dropped when
// one differs, whether or not a notification has arrived (R-ARCH-015, Q-ARCH-9).
package virtual
