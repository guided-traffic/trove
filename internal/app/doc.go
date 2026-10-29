// Package app is the composition root (R-ARCH-001, R-ARCH-024): it builds the
// components of a replica from the configuration, starts them in the order of
// R-ARCH-024, and stops them in the order of its shutdown sequence. It is the
// only package that imports the infrastructure, the domain and the edge
// together.
//
// What runs where is decided by the role (roles.go). The wiring of each
// infrastructure client lives in its own wire_*.go file, so the packages that
// arrive later (jobs, the replica registry, the blob store) add one file and one
// line in bootstrap, nothing else.
package app
