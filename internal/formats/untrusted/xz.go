package untrusted

import (
	"io"

	"github.com/ulikunitz/xz"

	"github.com/guided-traffic/trove/internal/errs"
)

// NewXZReader returns a reader of the xz stream r whose decompressed bytes are capped at lim.UncompressedBytes: a stream that
// decompresses to more fails with TooLarge instead of ending quietly (R-DEB-034). The xz module is only ever read through this
// bounded reader (its advisory CVE-2021-29482 is a decompression-bomb class issue, Q-DEB-24).
//
// The work package of the package-file libraries extends the checks around it (apt-work-packages.md, WP1).
func NewXZReader(r io.Reader, lim Limits) (io.Reader, error) {
	xr, err := xz.NewReader(r)
	if err != nil {
		return nil, Refused("The content is not a valid xz stream.", errs.WithCause(err))
	}
	return Limit(xr, effective(lim).UncompressedBytes), nil
}
