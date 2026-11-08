package dispatch

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// remoteHost is the host of a routing test of a remote repository: it allows every read and its remote engine finds no document, so the
// handler of the format answers what it answers for a missing manifest. A handler that is reached and fails (a panic of a nil host, recovered by
// the route) no longer passes for one that works.
type remoteHost struct{ fakeHost }

func (remoteHost) Authorize(context.Context, *formats.Request, formats.Target) error { return nil }

func (remoteHost) PathsAllowed(_ context.Context, _ *formats.Request, _ formats.Action, paths []string) ([]bool, error) {
	out := make([]bool, len(paths))
	for i := range out {
		out[i] = true
	}
	return out, nil
}

func (remoteHost) Remote() formats.RemoteFetcher { return notFoundRemote{} }

type notFoundRemote struct{ formats.RemoteFetcher }

func (notFoundRemote) Metadata(context.Context, *formats.Request, formats.Path, string) (*formats.RemoteDocument, error) {
	return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no such document.")
}
