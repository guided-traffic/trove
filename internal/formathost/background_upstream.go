package formathost

import (
	"context"
	"net/url"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
)

type jobKey struct{}

// WithJobID names the job that runs a background fetch: the upstream log record of every request of [Host.BackgroundUpstream]
// carries it (R-REPO-057). A context without one logs no job.
func WithJobID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, jobKey{}, id)
}

// BackgroundUpstream returns a [discover.Upstream] for one job run of one remote repository: the egress, credential, background
// share (R-REPO-055) and throttling (R-REPO-054) rules of a mirror pass, without a pass, its fence or its window (Q-REPO-272). Its
// Document and Artifact behave as they do for a pass: the same engine, the same slot, the same store. The jobs of the Debian format
// use it: the parse of a remote's indexes (R-DEB-023), the fetch of the release documents a virtual repository's build merges
// (R-DEB-026).
//
// What differs from a pass: the run has a window of its own, which starts at 1 and is held below the repository's background share
// (mirror.Window); a rate-limit answer waits inside the call, and a wait longer than mirror.LongWait ends the run with a
// *mirror.DeferredError for the job to retry at its time. An error of the engine is the one a pass's plan would see (mirror.Host).
// An artifact it stores is counted by no pass. The repository is read by its id (a move does not change it) and must be a remote
// repository. The upstream is for one run; the run's goroutines may share it.
func (h *Host) BackgroundUpstream(ctx context.Context, ref formats.RepoRef) (discover.Upstream, error) {
	eng, ok := h.o.Engine.(MirrorEngine)
	if !ok {
		return nil, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "This process has no remote engine for background fetches.")
	}
	if h.o.Repos == nil {
		return nil, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "This process has no repository reader for background fetches.")
	}
	repo, err := h.o.Repos.ByID(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	m := &MirrorHost{h: h, e: eng}
	rem, err := proxy.NewRemote(repo)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a usable remote repository.", errs.WithCause(err))
	}
	var hostsOf func(*url.URL) []string
	if lister, ok := m.Lister(repo.Format); ok {
		hostsOf = lister.MirrorHosts
	}
	share := proxy.BackgroundShare(rem.Settings.Limits.MaxConcurrentFetches, rem.Settings.Mirror.Concurrency)
	jobID, _ := ctx.Value(jobKey{}).(string)
	pu, err := m.newPassUpstream(repo, rem, mirror.Pass{JobID: jobID, Ceiling: share}, hostsOf)
	if err != nil {
		return nil, err
	}
	return &backgroundUpstream{passUpstream: pu, win: mirror.NewWindow(nil, nil, share)}, nil
}

// backgroundUpstream is a pass's upstream whose requests go through a window of the run's own.
type backgroundUpstream struct {
	*passUpstream
	win *mirror.Window
}

var (
	_ discover.Upstream = (*backgroundUpstream)(nil)
	_ discover.Bounded  = (*backgroundUpstream)(nil)
)

// Get implements [discover.Upstream] through the window.
func (b *backgroundUpstream) Get(ctx context.Context, target *url.URL, accept string, max int64) (res *discover.Response, err error) {
	err = b.win.Do(ctx, func(c context.Context) (e error) {
		res, e = b.passUpstream.Get(c, target, accept, max)
		return e
	})
	return res, err
}

// Document implements [discover.Upstream] through the window.
func (b *backgroundUpstream) Document(ctx context.Context, path, variant string) (doc []byte, found bool, err error) {
	err = b.win.Do(ctx, func(c context.Context) (e error) {
		doc, found, e = b.passUpstream.Document(c, path, variant)
		return e
	})
	return doc, found, err
}

// Artifact implements [discover.Upstream] through the window.
func (b *backgroundUpstream) Artifact(ctx context.Context, path string, max int64) (body []byte, found bool, err error) {
	err = b.win.Do(ctx, func(c context.Context) (e error) {
		body, found, e = b.passUpstream.Artifact(c, path, max)
		return e
	})
	return body, found, err
}
