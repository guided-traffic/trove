package repository

// FormatInfo is what the repository model needs of a format: the classes it
// accepts, its layout and the facts that decide which members of the
// configuration document apply (R-REPO-003, R-REPO-004). The composition root
// builds it from the format descriptor; this package does not import
// internal/formats, which is above it.
type FormatInfo struct {
	Name string
	// Classes are the classes the implementation accepts.
	Classes []Class
	Layout  string
	// ProtocolBasePath is the client address template, {key} the repository key.
	ProtocolBasePath string
	PatternSubject   PatternSubject
	// PackageNames: ownership, claims and name patterns apply (R-AUTHZ-018).
	PackageNames    bool
	PriorityMembers bool
	PinsAndClaims   bool
	Caps            bool
}

// Supports reports whether the format accepts the class.
func (f FormatInfo) Supports(c Class) bool {
	for _, k := range f.Classes {
		if k == c {
			return true
		}
	}
	return false
}

// FormatCatalog is the format catalog as the repository model consumes it
// (R-REPO-003): create and update validate the format, its classes and its
// layout against it.
type FormatCatalog interface {
	// Format returns the entry of a format name, or false for an unknown format.
	Format(name string) (FormatInfo, bool)
}

// validClass reports whether c is one of the three classes of R-REPO-001.
func validClass(c Class) bool {
	return c == Local || c == Remote || c == Virtual
}
