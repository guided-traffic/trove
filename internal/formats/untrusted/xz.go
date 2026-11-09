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
	return NewXZReaderMax(r, effective(lim).UncompressedBytes)
}

// NewXZReaderMax is [NewXZReader] with the cap given in bytes, for a reader whose bound is its own: the parse of a remote's index
// reads up to 1 GiB (R-DEB-023, R-DEB-034), which the shared defaults never allow.
func NewXZReaderMax(r io.Reader, max int64) (io.Reader, error) {
	xr, err := xz.NewReader(r)
	if err != nil {
		return nil, Refused("The content is not a valid xz stream.", errs.WithCause(err))
	}
	return Limit(xr, max), nil
}
