package oci

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
)

// The operations of blob uploads and the cross-repository mount (R-OCI-011, R-OCI-012). They answer 501 until the work package of the
// uploads replaces this file with handlers of its own; the route table names the methods and is not edited.

func (f *Format) handleUploadStart(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("start a blob upload or mount a blob (R-OCI-011, R-OCI-012)")(ctx, rq, w)
}

func (f *Format) handleUploadStatus(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("query a blob upload (R-OCI-011)")(ctx, rq, w)
}

func (f *Format) handleUploadPatch(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("send a chunk of a blob (R-OCI-011)")(ctx, rq, w)
}

func (f *Format) handleUploadPut(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("finish a blob upload (R-OCI-011)")(ctx, rq, w)
}

func (f *Format) handleUploadCancel(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	return stub("cancel a blob upload (R-OCI-011)")(ctx, rq, w)
}
