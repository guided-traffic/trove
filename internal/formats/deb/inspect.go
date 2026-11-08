package deb

import (
	"context"
	"io"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// arMagic opens every ar archive, the container of a Debian package (deb(5)).
const arMagic = "!<arch>\n"

// Inspect reads an upload that enters the repository (R-DEB-007): a package file is untrusted input and is read within the bounds of
// R-DEB-034 before anything uses it. The skeleton checks the one thing every package file has, the ar signature: input that is no ar
// archive answers 422 deb_invalid_archive. The reading of the package (the ar members, the control archive, the control file) is the
// work package of the local class (apt-work-packages.md, WP3), which replaces this file; until then a well-formed archive answers 501.
func (f *Format) Inspect(_ context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	r, err := in.Body.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	head, err := untrusted.ReadAll(io.LimitReader(r, int64(len(arMagic))), int64(len(arMagic)))
	if err != nil {
		return nil, err
	}
	if string(head) != arMagic {
		return nil, errs.E(errs.Unprocessable, errs.CodeDebInvalidArchive, "The upload is not an ar archive, the container of a Debian package.")
	}
	return nil, notImplemented()
}
