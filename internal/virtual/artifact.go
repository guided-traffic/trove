package virtual

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
)

// ResolvedFromHeader names the member that served an artifact (R-REPO-019). It is a member the
// caller can read, so it discloses nothing new; it is never sent on an endpoint with exposure
// rules, because members are not exposed there (R-EP-018).
const ResolvedFromHeader = "Trove-Resolved-From"

// Artifact walks the three tiers of R-REPO-019 step 6 and serves the first hit: the local members
// in order, the caches of the remote members without contacting an upstream, then the upstreams of
// the remote members on which the caller passes the cache-fill check. When nothing was found it
// answers 404 if every consulted member said "not found" and otherwise with the failure of the first
// member that failed transiently, because "not found" would be a false statement about an item that
// may exist (step 7).
func (r *Resolver) Artifact(ctx context.Context, c Caller, p *Plan, t Tiers, w http.ResponseWriter) error {
	repo, d := p.Virtual.Key, p.Descriptor
	if !p.Admitted {
		r.m.resolved(repo, sourceNone)
		return notFound()
	}
	serve := func(m Member, hit Hit, source string) error {
		if !c.Rules {
			w.Header().Set(ResolvedFromHeader, m.Repo.Key)
		}
		r.m.resolved(repo, source)
		return hit(w)
	}
	for _, m := range p.Local() {
		hit, ok, err := t.Local(ctx, m, d)
		if err != nil {
			return err
		}
		if ok {
			return serve(m, hit, sourceLocal)
		}
	}
	remotes := p.Remote()
	for _, m := range remotes {
		hit, ok, err := t.RemoteCached(ctx, m, d)
		if err != nil {
			return err
		}
		if ok {
			return serve(m, hit, sourceCache)
		}
	}
	var failure error
	for _, m := range remotes {
		if !m.MayFill {
			// A member the caller may not fill counts as a miss (R-AUTHZ-009 rule 3).
			continue
		}
		tw := &trackWriter{ResponseWriter: w}
		if !c.Rules {
			tw.Header().Set(ResolvedFromHeader, m.Repo.Key)
		}
		found, err := t.RemoteFetch(ctx, m, d, tw)
		if found && err == nil {
			r.m.resolved(repo, sourceUpstream)
			return nil
		}
		if !tw.wrote {
			tw.Header().Del(ResolvedFromHeader)
		}
		switch {
		case err == nil, isMiss(err):
			// This member does not have it.
		case tw.wrote || !isTransient(err) || ctx.Err() != nil:
			// A failure after the first byte, or one that is not about reaching the member,
			// ends the request: the next member cannot continue it.
			r.m.resolved(repo, sourceNone)
			return err
		case failure == nil:
			failure = err
		}
	}
	r.m.resolved(repo, sourceNone)
	if failure != nil {
		return failure
	}
	return notFound()
}

// trackWriter notes whether the response has been started, so that a fetch which failed after the
// first byte is not followed by another member's.
type trackWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (t *trackWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// isMiss reports whether an error from a remote member says "the member does not have it": a
// 404, or the refusal to populate, which a virtual repository counts as a miss (R-AUTHZ-008).
func isMiss(err error) bool {
	e, ok := errs.As(err)
	if !ok {
		return false
	}
	return e.Kind == errs.NotFound || (e.Kind == errs.Forbidden && e.Code == errs.CodePopulateNotPermitted)
}

// isTransient reports whether an error says the member could not be asked or did not answer.
func isTransient(err error) bool {
	e, ok := errs.As(err)
	if !ok {
		return false
	}
	switch e.Kind {
	case errs.UpstreamFailed, errs.UpstreamTimeout, errs.Unavailable:
		return true
	}
	return false
}
