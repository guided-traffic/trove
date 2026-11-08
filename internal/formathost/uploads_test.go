package formathost

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// Until the upload sessions and the checksum deploy of a format's request are built, the host's ports answer not_implemented: a
// format never meets a session that silently does not exist (R-STOR-011, ADR-161).
func TestTheUploadSessionPortAnswersNotImplementedUntilBuilt(t *testing.T) {
	h := newTestHost(t, &fakeDecider{})
	rq, id := testRequest(formats.Upload), formats.SessionRef{ID: uuid.New(), Binding: "app"}
	u := h.Uploads()
	ctx := context.Background()
	check := func(what string, err error) {
		t.Helper()
		if errs.KindOf(err) != errs.NotImplemented {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err := u.Start(ctx, rq, formats.RepoRef{}, "app")
	check("Start", err)
	_, err = u.Get(ctx, rq, id)
	check("Get", err)
	_, err = u.Append(ctx, rq, id, 0, strings.NewReader("x"))
	check("Append", err)
	_, err = u.Finalize(ctx, rq, id, [32]byte{}, nil, formats.Commit{})
	check("Finalize", err)
	check("Abort", u.Abort(ctx, rq, id))
}
