// Package view is the read model behind the package screens (R-API-029): packages
// across repositories, their versions and the details of a version, as a caller who
// may read only some of them sees them.
//
// A package of the view is a format and a normalized name; the generic rows of
// internal/packages hold it once per repository, so the view aggregates over the
// repositories. Every aggregate - the version count, the downloads, the first and
// the last publication, the repositories - counts only the versions the caller may
// read, and the filter runs in the statement that counts (internal/meta/postgres/
// queries/package_view.sql): the decision point classifies each repository
// ([authz.ReadScope]) and the view passes the answer as data, so a hidden version is
// neither counted nor listed, and its number cannot be derived from any aggregate.
//
// What the generic rows do not hold - the description, the keywords, the licenses,
// the metadata and the README - and the order of versions come from the format's
// adapter ([Adapter]); the view runs no format SQL and knows no format (R-ARCH-008).
// The package imports no edge package: the bridge to formats.ViewAdapter is in
// internal/formathost.
package view
