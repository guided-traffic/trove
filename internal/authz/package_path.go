package authz

import "github.com/guided-traffic/trove/internal/formats/catalog"

// The path of a question about a package as a whole (R-AUTHZ-005, Q-AUTHZ-250). A
// protocol request states the canonical path of what it serves or writes: the format
// plug-in puts it into its target, and the conformance kit fails a format whose handler
// authorizes a package without one. The decision point also asks questions of its own
// about a package as a whole: a row of a listing (ReadScope.MayRead), the ownership of
// a name, a check of the caller that names a package and no path. Those are decided on
// the canonical path of the package's document, the path the protocol's package document
// is decided on, so a path pattern narrows a listing exactly as it narrows the document
// a client would fetch. A coordinate without a path does not match a path dimension
// with any pattern (matchPath), so a question that cannot be given a path fails closed.

// documentPathIn returns the canonical path of the package document of a normalized
// name in a format of the catalog; "" for a format without package documents, one the
// catalog does not know and a string the format takes for no name.
func documentPathIn(reg *catalog.Registry, format, pkg string) string {
	if reg == nil || pkg == "" {
		return ""
	}
	e, ok := reg.Lookup(format)
	if !ok || e.DocumentPath == nil {
		return ""
	}
	p, ok := e.DocumentPath(pkg)
	if !ok {
		return ""
	}
	return p
}

// packagePath is documentPathIn on the engine's catalog.
func (e *Engine) packagePath(format, pkg string) string {
	return documentPathIn(e.o.Formats, format, pkg)
}

// atPackage completes a coordinate that names a package and no path with the path of
// the package's document, the form of the decision point's own package-level
// questions. The format is the coordinate's, else the one of the repository the index
// holds under its name. A coordinate with a path, one without a package, and one whose
// format has no package documents stay as they are.
func (e *Engine) atPackage(idx *Index, c Coordinate) Coordinate {
	if c.Package == "" || c.Path != "" {
		return c
	}
	format := c.Format
	if format == "" && idx != nil {
		if r := idx.repo(c.Repository); r != nil {
			format = r.Format
		}
	}
	c.Path = e.packagePath(format, c.Package)
	return c
}
