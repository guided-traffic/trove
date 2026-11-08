package nuget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// maxPreamble bounds what is read of a multipart body before its first part starts: the
// parser would otherwise read an unbounded run of bytes looking for the boundary.
const maxPreamble = 64 << 10

// handlePush is PUT v3/package (R-NUGET-006, flow F1). The body is read once, as a stream
// into the spool; nothing is held in a transaction while bytes arrive (R-ARCH-019). What
// the package declares decides next, outside any transaction: the identity, the
// permission on the ID, whether the number is stored, tombstoned or free, and the
// shadowing check. The rule that makes the commit safe (the package row lock, the version
// and tombstone checks after it, the ownership) runs again inside the commit transaction.
//
// The route is an upload to a local repository; a remote repository answers 405 before
// this is reached, and a virtual one is routed to its deployment target (R-NUGET-019).
func (f *Format) handlePush(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	rep := f.replicaOf(rq.Host)
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	body, length, err := packagePart(rq)
	if err != nil {
		return err
	}
	sp, err := rq.Host.Ingest().Spool(ctx, rq, body, formats.IngestOptions{MediaType: "application/octet-stream", Length: length})
	if err != nil {
		return ioError(err)
	}
	in, err := readIncoming(ctx, sp, rep.archiveLimits())
	if err != nil {
		return ioError(err)
	}
	// The caller holds deploy on the repository (the coarse check ran before the body was
	// read); here the ID the package declares is authorized (R-AUTHZ-005), including the
	// ownership of an ID that has an owner, before the number is looked at.
	// The path is the package's lead item, which the push creates (R-NUGET-005): a grant's path patterns decide it as
	// /files/ decides the same file (R-AUTHZ-005, Q-AUTHZ-250).
	target := formats.Target{Package: in.lowerID, Version: in.lowerVersion, Path: NupkgPath(in.lowerID, in.lowerVersion), Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	repo := rq.RepoRef()
	live, _, err := rq.Host.Packages().Versions(ctx, repo, in.lowerID)
	if err != nil {
		return err
	}
	if err := rq.Host.CheckOwnership(ctx, rq, target, formats.OwnershipWrite{Publish: true, FirstVersion: len(live) == 0}); err != nil {
		return err
	}
	sum := sp.Digests().SHA512
	if done, err := f.numberTaken(ctx, rq, w, rep, st, in, sum); done || err != nil {
		return err
	}
	shadow, skipped, err := f.shadowCheck(ctx, rq, in.lowerID, len(live))
	if err != nil {
		return err
	}
	pc := &pushCommit{in: in, origin: rows.OriginPush, audit: auditOf(rq), shadowing: shadow, shadowSkipped: skipped}
	insp := &formats.Inspection{
		Package: in.lowerID, Version: in.lowerVersion,
		Properties: map[string]string{"nuget.id": in.lowerID, "nuget.version": in.lowerVersion},
	}
	spec, err := rq.Host.Ingest().Spool(ctx, rq, bytes.NewReader(in.result.Nuspec), formats.IngestOptions{
		MediaType: "application/xml", Length: int64(len(in.result.Nuspec)),
	})
	if err != nil {
		return ioError(err)
	}
	_, err = rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
		Path: formats.Path{Path: in.nupkgPath()}, MediaType: "application/octet-stream", Inspection: insp, Payload: pc,
		Companions: []formats.Companion{{
			Spooled: spec, Path: formats.Path{Path: in.nuspecPath()}, MediaType: "application/xml", Generated: true, Payload: &nuspecCommit{in: in},
		}},
	})
	if err != nil {
		return f.commitError(ctx, rq, w, rep, st, in, sum, err)
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

// packagePart opens the package of a push: the first part of a multipart/form-data body,
// whose further parts, part names and part headers are ignored (R-NUGET-006), or - for a
// scripted upload with curl - the whole body of any other content type. length is the
// declared length of what it returns: none for a part, the request's for a raw body.
func packagePart(rq *formats.Request) (body io.Reader, length int64, err error) {
	mt, params, perr := mime.ParseMediaType(rq.Header.Get("Content-Type"))
	if perr != nil || mt != "multipart/form-data" {
		return rq.Body, rq.ContentLength, nil //nolint:nilerr // a content type that is no form is the raw body, not an error
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, 0, errs.E(errs.Invalid, errs.CodeInvalid, "The multipart body of a push has no boundary.")
	}
	pre := &limited{r: rq.Body, left: maxPreamble}
	part, err := multipart.NewReader(pre, boundary).NextPart()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, 0, errs.E(errs.Invalid, errs.CodeInvalid, "The multipart body of a push holds no part; the first part is the package.")
		}
		return nil, 0, errs.E(errs.Invalid, errs.CodeInvalid, "The multipart body of a push is malformed.", errs.WithCause(err))
	}
	// The part's own reads go through the parser, whose buffer has read ahead of the limit
	// already; the limit applies to the preamble and the part headers only.
	pre.left = -1
	return part, -1, nil
}

// limited fails a read once left bytes were consumed; a negative left lifts the limit.
type limited struct {
	r    io.Reader
	left int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left >= 0 {
		if l.left == 0 {
			return 0, errors.New("the multipart preamble is too long")
		}
		if int64(len(p)) > l.left {
			p = p[:l.left]
		}
	}
	n, err := l.r.Read(p)
	if l.left >= 0 {
		l.left -= int64(n)
	}
	return n, err
}

// numberTaken applies the immutability rule, the acknowledged retry and the tombstone to a
// push before the commit (R-NUGET-006). It returns done when it answered the request: the
// push of the identical bytes of a stored version answers 201 and changes nothing - no
// row, no revision, no event. Other bytes of a stored number, and a tombstoned number, are
// refused with 409.
func (f *Format) numberTaken(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, in *incoming, sha512 []byte) (bool, error) {
	repo := rq.RepoRef()
	facts, found, err := st.VersionFacts(ctx, repo.ID, in.lowerID, in.lowerVersion)
	if err != nil {
		return false, err
	}
	if found {
		if !bytes.Equal(facts.SHA512, sha512) {
			return false, versionExists(in)
		}
		rep.writes.replayed(rq.Repo.Name.String())
		w.WriteHeader(http.StatusCreated)
		return true, nil
	}
	_, err = f.tombstoneVerdict(ctx, nil, st, repo.ID, in, sha512)
	return false, err
}

// commitError decides what a failed commit means. A number that another push took
// meanwhile is the immutability rule's answer, and not the 403 the item store gives a path
// that took other content: the version is read again and decides.
func (f *Format) commitError(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, in *incoming, sha512 []byte, err error) error {
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Forbidden || e.Code != errs.CodeForbidden {
		return err
	}
	if done, verr := f.numberTaken(ctx, rq, w, rep, st, in, sha512); done || verr != nil {
		return verr
	}
	return err
}

// shadowCheck is the shadowing check of a first push of an ID (R-NUGET-006, R-REPO-048).
// It runs with no transaction open. A refusal is the 403 or 503 the check built; a push
// that proceeds hands the commit what the check found, for the audit events of the
// transaction. skipped says that the repository held a live version, so nothing was asked:
// the commit must not find the push to be the first one then.
func (f *Format) shadowCheck(ctx context.Context, rq *formats.Request, lowerID string, live int) (found rows.Shadowing, skipped bool, err error) {
	if live > 0 {
		return rows.Shadowing{}, true, nil
	}
	out, err := rq.Host.Resolver().ShadowCheck(ctx, rq, lowerID)
	if err != nil {
		return rows.Shadowing{}, false, err
	}
	if !out.Proceed {
		if out.Refusal != nil {
			return rows.Shadowing{}, false, out.Refusal
		}
		return rows.Shadowing{}, false, errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether the ID is served by a remote repository could not be determined; try again.", errs.WithRetryAfter(out.RetryAfter))
	}
	s := rows.Shadowing{Undetermined: out.Undetermined}
	for _, x := range out.Shadowed {
		s.Shadowed = append(s.Shadowed, rows.ShadowPair{Virtual: x.Virtual, Remote: x.Remote})
	}
	return s, false, nil
}

// ioError classifies what a read or a pass over the body returned that is not a refusal
// of the package: a cancelled or expired request is a failure to retry.
func ioError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the push was complete.", errs.WithCause(err))
	}
	return err
}

// auditOf is the audit context of a write: the repository, the endpoint and the actor,
// from the request's subject.
func auditOf(rq *formats.Request) rows.Audit {
	s := rq.Subject
	kind := string(s.Kind)
	if s.Anonymous {
		kind = "anonymous"
	}
	return rows.Audit{
		RepoKey: rq.Repo.Name.String(), Endpoint: s.Endpoint,
		Actor: rows.Actor{Kind: kind, ID: s.ID, Name: s.Name, TokenID: s.TokenID, AuthMethod: s.AuthMethod},
	}
}
