package formathost

import (
	"context"
	"io"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// unsupportedUploads is the upload-session port of a host whose sessions are not built yet (R-STOR-011): every call answers
// not_implemented, never as if the session did not exist.
type unsupportedUploads struct{}

var _ formats.UploadSessions = unsupportedUploads{}

func uploadsNotBuilt() error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "Upload sessions are not built yet.")
}

func (unsupportedUploads) Start(context.Context, *formats.Request, formats.RepoRef, string) (formats.UploadSession, error) {
	return formats.UploadSession{}, uploadsNotBuilt()
}

func (unsupportedUploads) Get(context.Context, *formats.Request, formats.SessionRef) (formats.UploadSession, error) {
	return formats.UploadSession{}, uploadsNotBuilt()
}

func (unsupportedUploads) Append(context.Context, *formats.Request, formats.SessionRef, int64, io.Reader) (int64, error) {
	return 0, uploadsNotBuilt()
}

func (unsupportedUploads) Finalize(context.Context, *formats.Request, formats.SessionRef, [32]byte, io.Reader, formats.Commit) (formats.Committed, error) {
	return formats.Committed{}, uploadsNotBuilt()
}

func (unsupportedUploads) Abort(context.Context, *formats.Request, formats.SessionRef) error {
	return uploadsNotBuilt()
}
