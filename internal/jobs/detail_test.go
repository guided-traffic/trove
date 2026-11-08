package jobs

import (
	"errors"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// reworded wraps an error with a text of its own that does not contain the wrapped error's text.
type reworded struct{ err error }

func (r reworded) Error() string {
	return "the run failed: " + fmt.Sprint(len(r.err.Error())) + " bytes of cause"
}
func (r reworded) Unwrap() error { return r.err }

func TestJobDetail(t *testing.T) {
	db := errors.New(`FATAL: password authentication failed for user "trove" (SQLSTATE 28P01)`)
	unavailable := errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "The principal cannot be resolved right now.", errs.WithCause(db), errs.WithOp("principal.load"))
	const shown = "database_unavailable: The principal cannot be resolved right now."
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"an error written for a caller shows its code and detail, not its cause", unavailable, shown},
		{"so does one marked permanent", Permanent(unavailable), shown},
		{"a wrapper keeps its own words, and the error it wraps shows no cause", fmt.Errorf("items-delete: batch: %w", unavailable), "items-delete: batch: " + shown},
		{"a wrapper that rewords the text shows the code and detail only", reworded{unavailable}, shown},
		{"a code without a detail shows the code", errs.E(errs.Forbidden, errs.CodeForbidden, ""), "forbidden"},
		{"an error written for a caller without a cause shows as it is", fmt.Errorf("x: %w", errs.E(errs.Forbidden, errs.CodeForbidden, "no")), "x: forbidden: no"},
		{"any other error shows its text as its producer wrote it", fmt.Errorf("archive: %w", errors.New("not a zip archive")), "archive: not a zip archive"},
	} {
		t.Run("R-OPS-019 "+tc.name, func(t *testing.T) {
			if got := jobDetail(tc.err); got != tc.want {
				t.Fatalf("jobDetail = %q, want %q", got, tc.want)
			}
		})
	}
}
