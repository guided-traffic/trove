package untrusted

import (
	"io"

	"github.com/guided-traffic/trove/internal/errs"
)

// ArMember is one member of an ar archive in the order the archive holds it: its name without padding and the size its header
// declares.
type ArMember struct {
	Name string
	Size int64
}

// ArReader reads the members of an ar archive, the container of a Debian package (deb(5), R-DEB-007), within the bounds of
// [Limits]: the number of members, the length of a name, member sizes that add up to the archive (R-DEB-034). It reads members
// one after the other; a member's body is a section of the archive and is never read into memory by the reader itself.
//
// The declaration is the contract of the work package that builds the readers (apt-work-packages.md, WP1): the methods below
// answer not_implemented until it lands, and nothing calls them before then.
type ArReader struct{}

// NewAr checks the global header of the archive and its member headers against lim and returns the reader.
func NewAr(io.ReaderAt, int64, Limits) (*ArReader, error) {
	return nil, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The ar reader is not built yet.")
}

// Next advances to the next member and returns it; io.EOF after the last.
func (*ArReader) Next() (ArMember, error) {
	return ArMember{}, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The ar reader is not built yet.")
}

// Open returns the body of the member Next returned last, bounded by its declared size.
func (*ArReader) Open() (io.Reader, error) {
	return nil, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "The ar reader is not built yet.")
}
