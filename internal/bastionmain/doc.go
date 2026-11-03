// Package bastionmain holds the seams of the main installation's side of the trove Bastion (R-BAS-004 to
// R-BAS-017, docs/developer/bastion.md): the types and interfaces that the work packages 1 to 3 build against
// in parallel and that the composition root wires. It is a domain package. The wire contract with the Bastion
// is internal/bastion/iface, which this package imports; the Bastion's own code (internal/bastion/...) imports
// nothing of the main installation, so none of this package is visible to it (R-BAS-001).
//
// The seams, who implements each and who uses it:
//
//   - [Recognition], [WithRecognition], [RecognitionFrom]: the result of recognizing a Bastion request. Set
//     by the recognition stage (package 2), read by the marking stage and the route observer (package 3), by
//     the shared authorization step of the interface routes (internal/api/v1, built) and by the interface
//     handlers (package 2).
//   - [TokenAuthenticator]: a raw Bastion token to an enabled registration. Implemented by package 1,
//     consumed by the recognition stage (package 2).
//   - [ShapeResolver]: a subject's read shape on repositories (R-BAS-012). Implemented by package 3, used
//     by the marking (package 3) and by the check operation (package 2).
//   - [RouteObserver], [RouteMatch], [RouteDecision]: what the format dispatcher tells the marking stage
//     about the matched route (package 3 implements, internal/api/dispatch calls, built).
//   - [Stages]: the two chain stages a composition root passes to httpserver.Chain.Bastion.
//   - [ErrNotImplemented] and [Stub]: the 501 of an operation whose work package is not built.
//
// docs/developer/bastion.md has the table that names, per seam, the file, the implementing package and the
// tests that pass when it is filled.
package bastionmain
