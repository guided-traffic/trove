package formathost

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/uploads"
)

// SessionService is the part of the upload session service (internal/uploads) the host uses; *uploads.Service implements it. The
// service decides nothing about who may do what: the host does, on every call, before it asks the service.
type SessionService interface {
	Start(ctx context.Context, target int64, sc uploads.Scope) (uploads.Session, error)
	Lookup(ctx context.Context, id uuid.UUID, sc uploads.Scope) (uploads.Session, error)
	Get(ctx context.Context, id uuid.UUID, sc uploads.Scope) (uploads.Session, error)
	Append(ctx context.Context, id uuid.UUID, sc uploads.Scope, offset int64, body io.Reader, limit int64) (int64, error)
	Finalize(ctx context.Context, id uuid.UUID, sc uploads.Scope, offset int64, sum [32]byte, last io.Reader, limit int64, commit uploads.CommitFunc) (uploads.Result, error)
	Abort(ctx context.Context, id uuid.UUID, sc uploads.Scope) error
}

var _ SessionService = (*uploads.Service)(nil)

// unsupportedUploads is the upload-session port of a host without a session service (a replica that has none wired, the host of
// a mirror pass): every call answers not_implemented, never as if the session did not exist.
type unsupportedUploads struct{}

var _ formats.UploadSessions = unsupportedUploads{}

func uploadsNotBuilt() error {
	return errs.E(errs.NotImplemented, errs.CodeNotImplemented, "Upload sessions are not available on this host.")
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

func (unsupportedUploads) Finalize(context.Context, *formats.Request, formats.SessionRef, int64, [32]byte, io.Reader, formats.Commit) (formats.Committed, error) {
	return formats.Committed{}, uploadsNotBuilt()
}

func (unsupportedUploads) Abort(context.Context, *formats.Request, formats.SessionRef) error {
	return uploadsNotBuilt()
}

// sessions is the host's upload-session port over the session service. On every call it decides what the service does not: that
// the principal is the session's, that the session was started for the binding of the call on the repository the client
// addressed, and that `deploy` on the session's target is still allowed (a permission revoked during an upload ends it). Whatever
// is wrong answers errs.NotFound, the answer for a session that does not exist, so that a session id tells a caller nothing
// (R-STOR-011 step 1, Q-OCI-12).
type sessions struct {
	h   *Host
	svc SessionService
}

var _ formats.UploadSessions = sessions{}

// uploadGone is the answer for a session the request may not continue.
func uploadGone() error {
	return errs.E(errs.NotFound, errs.CodeOCIBlobUploadUnknown, "The upload does not exist.")
}

// uploadTarget is the repository a session of the addressed repository writes to: the repository itself for a local one, its
// deployment target for a virtual one (R-OCI-028). A call acts on the addressed repository; a request derived for a member is
// refused.
func uploadTarget(rq *formats.Request) (repository.Repository, error) {
	if rq.Addressed() != rq.Repo.Name {
		return repository.Repository{}, errs.E(errs.Internal, errs.CodeInternal, "An upload call acts on the addressed repository.")
	}
	switch rq.Repo.Class {
	case repository.Local:
		return *rq.Repo, nil
	case repository.Virtual:
		if t, ok := rq.Repo.DeployTarget(); ok && t.Class == repository.Local {
			return t, nil
		}
		return repository.Repository{}, errs.E(errs.Internal, errs.CodeInternal, "The virtual repository has no deployment target for an upload.")
	}
	return repository.Repository{}, errs.E(errs.Internal, errs.CodeInternal, "An upload is written to a local repository.")
}

// scope is what the session must match for this request.
func scopeOf(rq *formats.Request, binding string) uploads.Scope {
	principal := rq.Subject.ID
	if rq.Subject.CI != nil {
		// A CI identity mapping is one principal for every job it matches; each exchange issues a token of its own, so the session and the cap
		// are keyed on the token (Q-STOR-176): two jobs under one mapping neither share a cap nor see each other's sessions.
		if _, err := uuid.Parse(rq.Subject.TokenID); err == nil {
			principal = rq.Subject.TokenID
		}
	}
	return uploads.Scope{Addressed: rq.RepoRef().ID, Binding: binding, Principal: principal}
}

// decide is the decision of `deploy` on the target for the binding as the package, on the request's index. The coordinate is the
// target's own, as for a write through a virtual repository (R-NPM-029): a request derived for the target carries no addressed
// repository. The allowed decision is remembered, so that the commit of a finalize records it.
func (u sessions) decide(ctx context.Context, rq *formats.Request, target repository.Repository, binding string) (*formats.Request, error) {
	trq := rq
	if target.Name != rq.Repo.Name {
		trq = rq.WriteTarget(&target)
		idx, err := u.h.o.Decider.Know(ctx, rq.Index, rq.Subject, target.Name)
		if err != nil {
			return nil, err
		}
		trq.Index = idx
	}
	// The coordinate names a path of the package, as every decision does (R-AUTHZ-005): a session has no blob yet, so it is the package's
	// document path when the format has one ("<name>/_taglist" for an image), the binding itself otherwise. A grant or a pattern scoped by
	// path then decides on a path the format classifies in the package.
	path := binding
	if f, ok := u.h.o.Formats(target.Format); ok {
		if doc, ok := f.PackageDocumentPath(binding); ok && doc.Path != "" {
			path = doc.Path
		}
	}
	t := formats.Target{Package: binding, Path: path, Actions: []formats.Action{formats.ActionDeploy}}
	if err := u.h.Authorize(ctx, trq, t); err != nil {
		return nil, err
	}
	return trq, nil
}

// session finds the session of the call and decides on its target. A refusal of the decision is the session that does not exist.
func (u sessions) session(ctx context.Context, rq *formats.Request, ref formats.SessionRef, read func(uploads.Scope) (uploads.Session, error)) (opened, error) {
	target, err := uploadTarget(rq)
	if err != nil {
		return opened{}, err
	}
	sc := scopeOf(rq, ref.Binding)
	sess, err := read(sc)
	if err != nil {
		return opened{}, err
	}
	// The addressed repository still sends the session where it was started (a virtual repository whose deployment target changed
	// ends the uploads to the old one).
	if repoRef(target).ID != sess.Target {
		return opened{}, uploadGone()
	}
	trq, err := u.decide(ctx, rq, target, ref.Binding)
	if err != nil {
		if k := errs.KindOf(err); k == errs.Forbidden || k == errs.NotFound || k == errs.Unauthenticated {
			return opened{}, uploadGone()
		}
		return opened{}, err
	}
	return opened{sess: sess, sc: sc, trq: trq, target: target}, nil
}

// opened is a session of a call whose scope matched and whose target the caller may still deploy to.
type opened struct {
	sess uploads.Session
	sc   uploads.Scope
	// trq is the request for the target, the one the commit of a finalize is made on.
	trq    *formats.Request
	target repository.Repository
}

func sessionOf(target repository.Repository, s uploads.Session) formats.UploadSession {
	return formats.UploadSession{ID: s.ID, Target: repoRef(target), Binding: s.Binding, Received: s.Received, PartSize: s.PartSize}
}

// Start implements [formats.UploadSessions].
func (u sessions) Start(ctx context.Context, rq *formats.Request, target formats.RepoRef, binding string) (formats.UploadSession, error) {
	want, err := uploadTarget(rq)
	if err != nil {
		return formats.UploadSession{}, err
	}
	if repoRef(want).ID != target.ID || want.Name != target.Name {
		return formats.UploadSession{}, errs.E(errs.Internal, errs.CodeInternal, "An upload is started for the repository of the request or its deployment target.")
	}
	if _, err := u.decide(ctx, rq, want, binding); err != nil {
		return formats.UploadSession{}, err
	}
	sess, err := u.svc.Start(ctx, target.ID, scopeOf(rq, binding))
	if err != nil {
		return formats.UploadSession{}, err
	}
	return sessionOf(want, sess), nil
}

// Get implements [formats.UploadSessions].
func (u sessions) Get(ctx context.Context, rq *formats.Request, ref formats.SessionRef) (formats.UploadSession, error) {
	o, err := u.session(ctx, rq, ref, func(sc uploads.Scope) (uploads.Session, error) { return u.svc.Get(ctx, ref.ID, sc) })
	if err != nil {
		return formats.UploadSession{}, err
	}
	return sessionOf(o.target, o.sess), nil
}

// lookup reads the session in any state: the calls that act on it check the state themselves.
func (u sessions) lookup(ctx context.Context, rq *formats.Request, ref formats.SessionRef) (opened, error) {
	return u.session(ctx, rq, ref, func(sc uploads.Scope) (uploads.Session, error) { return u.svc.Lookup(ctx, ref.ID, sc) })
}

// Append implements [formats.UploadSessions].
func (u sessions) Append(ctx context.Context, rq *formats.Request, ref formats.SessionRef, offset int64, body io.Reader) (int64, error) {
	o, err := u.lookup(ctx, rq, ref)
	if err != nil {
		return 0, err
	}
	limit, err := files.MaxUpload(o.target)
	if err != nil {
		return 0, err
	}
	return u.svc.Append(ctx, ref.ID, o.sc, offset, body, limit)
}

// Finalize implements [formats.UploadSessions]. The blob is committed as the item c names, with the target as the repository, in
// the transaction that also ends the session.
func (u sessions) Finalize(ctx context.Context, rq *formats.Request, ref formats.SessionRef, offset int64, sum [32]byte, last io.Reader, c formats.Commit) (formats.Committed, error) {
	o, err := u.lookup(ctx, rq, ref)
	if err != nil {
		return formats.Committed{}, err
	}
	limit, err := files.MaxUpload(o.target)
	if err != nil {
		return formats.Committed{}, err
	}
	// The session was admitted for its binding; the commit writes c.Path, and `deploy` is decided on that path as well, so that what is
	// written is what was decided (the patterns of the repository and the grants by path see it).
	if p := c.Path.Path; p != "" {
		// The package of the path comes from the format, as the item commit sees it (patterns, ownership and grants match the package name);
		// a path of another package than the one the session was started for is a defect of the format's handler, refused before it is written.
		pkg := ref.Binding
		if f, ok := u.h.o.Formats(o.target.Format); ok {
			pkg = f.Classify(formats.Path{Path: p}).Package
		}
		if pkg != ref.Binding {
			return formats.Committed{}, errs.E(errs.Internal, errs.CodeInternal, "The path of a finalize belongs to another package than the session.")
		}
		t := formats.Target{Package: pkg, Path: p, Actions: []formats.Action{formats.ActionDeploy}}
		if err := u.h.Authorize(ctx, o.trq, t); err != nil {
			return formats.Committed{}, err
		}
	}
	var out formats.Committed
	commit := func(ctx context.Context, staged *blob.Spooled, markDone func(context.Context, meta.Tx, int64) error) error {
		sp := &spooled{s: staged, expect: items.Expected{SHA256: sum[:]}}
		var cerr error
		out, cerr = ingestor{h: u.h}.commit(ctx, o.trq, sp, c, func(ctx context.Context, tx meta.Tx, it items.Ref) error { return markDone(ctx, tx, it.ID) })
		return cerr
	}
	res, err := u.svc.Finalize(ctx, ref.ID, o.sc, offset, sum, last, limit, commit)
	if err != nil {
		return formats.Committed{Received: res.Received}, err
	}
	if res.Replayed {
		// the session is done: the item is the one its first commit made, and nothing is committed again
		return formats.Committed{Item: formats.ItemRef{ID: res.ItemID, Repo: repoRef(o.target), Path: c.Path.Path}, Identical: true}, nil
	}
	return out, nil
}

// Abort implements [formats.UploadSessions].
func (u sessions) Abort(ctx context.Context, rq *formats.Request, ref formats.SessionRef) error {
	o, err := u.lookup(ctx, rq, ref)
	if err != nil {
		return err
	}
	return u.svc.Abort(ctx, ref.ID, o.sc)
}
