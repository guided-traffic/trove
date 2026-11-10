package ansible

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// maxPreamble bounds what is read of a multipart body before its first part starts: the parser would otherwise read an unbounded run of
// bytes looking for the boundary.
const maxPreamble = 64 << 10

// maxDigestPart bounds the part sha256 of a publish.
const maxDigestPart = 128

const archiveMediaType = "application/gzip"

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func init() {
	registerHandler(formats.Local, routePublish, func(f *Format) formats.HandlerFunc { return f.handlePublish })
}

// handlePublish is POST v3/artifacts/collections/ (R-ANS-008, flow F1). The body is read once, as a stream into the spool; nothing is
// held in a transaction while bytes arrive (R-ARCH-019). What the archive declares decides next, outside any transaction: the digest
// the client sent, the identity, the permission on the name, whether the number is stored, tombstoned or free, and the shadowing check.
// The rule that makes the commit safe (the package row lock, the number and tombstone checks after it, the ownership) runs again inside
// the commit transaction. An archive that cannot be a collection archive is recorded as a failed import task and answered 202, so the
// client prints Galaxy's own failure text (R-ANS-009); every refusal before the inspection is an HTTP error and has no task.
//
// A virtual repository's publish goes to its deployment target and a remote repository answers 405, both before this is reached
// (Route.Classes, the dispatcher).
func (f *Format) handlePublish(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	rep := f.replicaOf(rq.Host)
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	sp, declared, err := f.readPublish(ctx, rq, rep)
	if err != nil {
		return err
	}
	if declared != "" && declared != hexOf(sp.Digests().SHA256) {
		rep.published(outcomeFailed)
		return errs.E(errs.Invalid, errs.CodeAnsibleSHA256Mismatch,
			"The sha256 sent with the archive is "+declared+", the archive's is "+hexOf(sp.Digests().SHA256)+". Nothing was stored.")
	}
	au := auditOf(rq)
	insp, err := f.Inspect(ctx, formats.InspectInput{
		MediaType: archiveMediaType, Body: sp, Limits: untrusted.DefaultLimits().Tighten(f.Descriptor().Limits),
	})
	if err != nil {
		return f.failedTask(ctx, rq, w, rep, st, au, err)
	}
	a, _ := insp.Payload.(*collection.Archive)
	name := insp.Package
	path := ArtifactPath(a.Namespace, a.Name, a.Version)
	// The caller holds deploy on the repository (the coarse check ran before the body was read); here the name the archive declares is
	// authorized (R-AUTHZ-005), including the ownership of a name that has an owner, before the number is looked at.
	target := formats.Target{Package: name, Version: a.Version, Path: path, Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	repo := rq.RepoRef()
	live, _, err := rq.Host.Packages().Versions(ctx, repo, name)
	if err != nil {
		return err
	}
	if err := rq.Host.CheckOwnership(ctx, rq, target, formats.OwnershipWrite{Publish: true, FirstVersion: len(live) == 0}); err != nil {
		return err
	}
	pc := &publishCommit{archive: a, audit: au, task: rows.Task{ID: uuid.NewString()}}
	if done, err := f.numberTaken(ctx, rq, w, rep, st, pc, live, sp); done || err != nil {
		return err
	}
	shadow, skipped, err := f.shadowCheck(ctx, rq, name, len(live))
	if err != nil {
		return err
	}
	pc.info, pc.shadowSkipped = shadow, skipped
	_, err = rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
		Path: formats.Path{Path: path}, MediaType: archiveMediaType, Inspection: insp, Payload: pc,
	})
	if err != nil {
		return f.commitError(ctx, rq, w, rep, st, pc, sp, err)
	}
	if !pc.taskRecorded {
		// The commit found identical content at the path and ran no hook: the publish repeats one that had committed.
		return f.repeat(ctx, rq, w, rep, st, pc)
	}
	rep.published(outcomeCompleted)
	return f.answerTask(rq, w, pc.task.ID)
}

// readPublish reads the body of a publish into the spool (R-ANS-008 step 1). The body is a multipart/form-data of the parts file (the
// archive) and sha256 (its hex digest), in either order: the client sends sha256 first, the documentation shows file first. Any other
// body is the archive itself, for a scripted publish with curl, with the digest optionally in the query. declared is the digest the
// client sent, lower case, or "".
func (f *Format) readPublish(ctx context.Context, rq *formats.Request, rep *replica) (sp formats.Spooled, declared string, err error) {
	opts := formats.IngestOptions{MediaType: archiveMediaType, MaxSize: int64(rep.cfg.MaxArchiveBytes)}
	mt, params, perr := mime.ParseMediaType(rq.Header.Get("Content-Type"))
	if perr != nil || mt != "multipart/form-data" {
		opts.Length = rq.ContentLength
		sp, err = rq.Host.Ingest().Spool(ctx, rq, rq.Body, opts)
		if err != nil {
			return nil, "", ioError(err)
		}
		return sp, strings.ToLower(rq.URL.Query().Get("sha256")), nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", invalidRequest("The multipart body of a publish has no boundary.")
	}
	opts.Length = -1
	pre := &limited{r: rq.Body, left: maxPreamble}
	mr := multipart.NewReader(pre, boundary)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", invalidRequest("The multipart body of a publish is malformed.", errs.WithCause(err))
		}
		// The part's own reads go through the parser, whose buffer has read ahead of the limit already; the limit applies to the
		// preamble and the part headers only.
		pre.left = -1
		switch part.FormName() {
		case "file":
			if sp != nil {
				return nil, "", invalidRequest("The publish has the part file twice.")
			}
			if sp, err = rq.Host.Ingest().Spool(ctx, rq, filePartBody(part), opts); err != nil {
				return nil, "", ioError(err)
			}
		case "sha256":
			b, err := io.ReadAll(io.LimitReader(part, maxDigestPart+1))
			if err != nil || len(b) > maxDigestPart {
				return nil, "", invalidRequest("The part sha256 of the publish is not a hexadecimal digest.")
			}
			declared = strings.ToLower(strings.TrimSpace(string(b)))
		default:
			if _, err := io.Copy(io.Discard, io.LimitReader(part, 1<<16)); err != nil {
				return nil, "", invalidRequest("The multipart body of a publish is malformed.", errs.WithCause(err))
			}
		}
	}
	if sp == nil {
		return nil, "", invalidRequest("The publish has no part named file; the archive is sent as multipart/form-data with the parts file and sha256.")
	}
	if declared != "" && !isHexDigest(declared) {
		return nil, "", invalidRequest("The part sha256 of the publish is not 64 hexadecimal digits.")
	}
	return sp, declared, nil
}

// filePartBody is the bytes of the file part. ansible-core sends the archive base64-encoded with Content-Transfer-Encoding: base64
// (prepare_multipart of ansible.module_utils.urls, verified with the clients of the e2e set), lines of 76 characters; the multipart
// reader decodes quoted-printable by itself and leaves base64 alone, so the digest and the archive would be those of the encoded text.
// A part without that header is the raw archive, which curl and the documentation send.
func filePartBody(part *multipart.Part) io.Reader {
	if strings.EqualFold(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")), "base64") {
		return base64.NewDecoder(base64.StdEncoding, part)
	}
	return part
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func invalidRequest(detail string, opts ...errs.Option) error {
	return errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, detail, opts...)
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

// ioError classifies what a read or a pass over the body returned that is not a refusal of the archive: a cancelled or expired request
// is a failure to retry.
func ioError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the publish was complete.", errs.WithCause(err))
	}
	return err
}

// auditOf is the audit context of a write: the repository, the endpoint and the actor, from the request's subject.
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

// failedTask records the refusal of an archive as a failed import task and answers 202 with its address (R-ANS-009). An error that is
// no refusal of the archive (a failed read, an ended context) is returned as it is: it is a failure of the request, not of the archive.
func (f *Format) failedTask(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, au rows.Audit, err error) error {
	e, ok := errs.As(err)
	if !ok || (e.Kind != errs.Unprocessable && e.Kind != errs.TooLarge) {
		return err
	}
	code := strings.TrimPrefix(e.Code, "ansible_")
	if e.Kind == errs.TooLarge {
		code = "invalid_archive"
	}
	now := time.Now().UTC()
	t := rows.Task{
		ID: uuid.NewString(), State: rows.TaskFailed, RepoID: rq.RepoRef().ID, CreatedBy: au.Actor.ID,
		CreatedAt: now, StartedAt: now, UpdatedAt: now, FinishedAt: now, ErrorCode: code, ErrorDescription: e.Detail,
		Messages: []rows.TaskMessage{{Level: "ERROR", Message: e.Detail, Time: now}},
	}
	if err := st.CreateTask(ctx, nil, t, au); err != nil {
		return err
	}
	rep.published(outcomeFailed)
	return f.answerTask(rq, w, t.ID)
}

// answerTask answers 202 with the address of the task, which the client polls (R-ANS-008 step 5).
func (f *Format) answerTask(rq *formats.Request, w http.ResponseWriter, id string) error {
	addr := galaxydoc.Addr{Repo: rq.Host.Links(rq).Registry(rq.Addressed())}
	body := []byte(`{"task":` + string(jsonString(addr.Task(id))) + `}`)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", itoa(len(body)))
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(body)
	return nil
}

// numberTaken applies the immutability rule, the acknowledged retry and the tombstone to a publish before the commit (R-ANS-008 step 4).
// It returns done when it answered the request: the publish of the identical bytes of a stored version answers 202 with a task of its
// own and changes nothing else. Other bytes of a stored number, and a tombstoned number, are refused with 409.
func (f *Format) numberTaken(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, pc *publishCommit, live []formats.VersionRow, sp formats.Spooled) (bool, error) {
	a := pc.archive
	name := a.Namespace + "." + a.Name
	repoID := rq.RepoRef().ID
	// A withheld number is refused first: a restore's replay leaves the generic version row of a withheld version in place (R-ANS-029).
	if _, tombstoned, err := st.Tombstoned(ctx, repoID, name, a.Version); err != nil {
		return false, err
	} else if tombstoned {
		rep.published(outcomeTombstoned)
		return false, versionTombstoned(name, a.Version)
	}
	for _, v := range live {
		if !version.Same(v.Version, a.Version) {
			continue
		}
		row, found, err := st.Version(ctx, repoID, name, v.Version)
		if err != nil {
			return false, err
		}
		if !found || row.SHA256 != sumOf(sp.Digests().SHA256) || row.Version != a.Version {
			rep.published(outcomeExists)
			return false, versionExists(name, a.Version)
		}
		return true, f.repeat(ctx, rq, w, rep, st, pc)
	}
	return false, nil
}

// repeat answers the publish of the bytes of a stored version: a task of its own that points to the same version (R-ANS-009), no row, no
// revision and no package event.
func (f *Format) repeat(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, pc *publishCommit) error {
	a := pc.archive
	name := a.Namespace + "." + a.Name
	now := time.Now().UTC()
	t := pc.task
	t.Namespace, t.Name, t.Version = a.Namespace, a.Name, a.Version
	t.State, t.RepoID, t.CreatedBy = rows.TaskCompleted, rq.RepoRef().ID, pc.audit.Actor.ID
	t.CreatedAt, t.StartedAt, t.UpdatedAt, t.FinishedAt = now, now, now, now
	t.Messages = []rows.TaskMessage{{Level: "INFO", Message: "Collection " + name + " " + a.Version + " is stored already with these bytes; nothing was changed", Time: now}}
	if err := st.CreateTask(ctx, nil, t, pc.audit); err != nil {
		return err
	}
	rep.published(outcomeCompleted)
	return f.answerTask(rq, w, t.ID)
}

// commitError decides what a failed commit means. A number that another publish took meanwhile is the immutability rule's answer, and not
// the 403 the item store gives a path that took other content: the version is read again and decides.
func (f *Format) commitError(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store, pc *publishCommit, sp formats.Spooled, err error) error {
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Forbidden || e.Code != errs.CodeForbidden {
		if ok && e.Code == errs.CodeAnsibleVersionExists {
			rep.published(outcomeExists)
		}
		return err
	}
	cur, _, lerr := rq.Host.Packages().Versions(ctx, rq.RepoRef(), pc.archive.Namespace+"."+pc.archive.Name)
	if lerr != nil {
		return err
	}
	if done, verr := f.numberTaken(ctx, rq, w, rep, st, pc, cur, sp); done || verr != nil {
		return verr
	}
	return err
}

// shadowCheck is the shadowing check of a first publish of a name (R-ANS-008 step 3, R-REPO-048). It runs with no transaction open. A
// refusal is the 403 or 503 the check built; a publish that proceeds hands the commit what the check found, for the audit events of the
// transaction. skipped says that the repository held a live version, so nothing was asked: the commit must not find the publish to be
// the first one then.
func (f *Format) shadowCheck(ctx context.Context, rq *formats.Request, name string, live int) (found rows.PublishInfo, skipped bool, err error) {
	if live > 0 {
		return rows.PublishInfo{}, true, nil
	}
	out, err := rq.Host.Resolver().ShadowCheck(ctx, rq, name)
	if err != nil {
		return rows.PublishInfo{}, false, err
	}
	if !out.Proceed {
		if out.Refusal != nil {
			return rows.PublishInfo{}, false, out.Refusal
		}
		return rows.PublishInfo{}, false, errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether the name is served by a remote repository could not be determined; try again.", errs.WithRetryAfter(out.RetryAfter))
	}
	s := rows.PublishInfo{ShadowUndetermined: out.Undetermined}
	for _, x := range out.Shadowed {
		s.Shadowed = append(s.Shadowed, rows.ShadowPair{Virtual: x.Virtual, Remote: x.Remote})
	}
	return s, false, nil
}
