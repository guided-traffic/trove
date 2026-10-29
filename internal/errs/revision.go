package errs

import (
	"errors"
	"strconv"
)

// AnyRevision is the expected revision of a deliberate blind write
// (If-Match: *, R-API-012): the update has no revision condition. Every revision
// a row can hold is at least 1, so 0 never matches and is what a tag the server
// never issued becomes.
const AnyRevision int64 = -1

// StaleRevision is the cause of the 412 of a mismatching If-Match: the stored
// revision differs from the one the caller read.
type StaleRevision struct{ Current int64 }

func (e *StaleRevision) Error() string {
	return "the stored revision is " + strconv.FormatInt(e.Current, 10)
}

// Stale is the one domain error of a mismatching revision (R-API-012): 412
// precondition_failed with the extension currentEtag (the current revision as a
// strong tag, R-API-009) and [StaleRevision] as the cause. what names the
// resource in the detail, for example "The exposure rule".
func Stale(what string, current int64) error {
	return E(PreconditionFailed, CodePreconditionFailed,
		what+" changed since you loaded it; reload it and try again.",
		WithCause(&StaleRevision{Current: current}),
		WithExtension("currentEtag", `"`+strconv.FormatInt(current, 10)+`"`))
}

// CurrentRevision returns the current revision of an error built by [Stale].
func CurrentRevision(err error) (int64, bool) {
	var st *StaleRevision
	if errors.As(err, &st) {
		return st.Current, true
	}
	return 0, false
}
