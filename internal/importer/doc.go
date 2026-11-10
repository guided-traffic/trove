// Package importer copies the packages of an existing npm or NuGet registry into a trove local repository (R-IMP-001 to
// R-IMP-010).
//
// The importer is a client of both registries. It reads the source the way a package manager does and writes the target
// through the target's own publish protocol with a token, so every rule of a publish applies to an imported version: the
// permission check on the repository and the path, the digest verification, the immutability rule, the audit event, the
// vulnerability evaluation and the storage quota (R-IMP-004). It has no database access and no other way into trove's
// storage.
//
// A format is one implementation of [Format]; the runner, the report, the state file and the rate limit are shared
// (R-IMP-009).
package importer
