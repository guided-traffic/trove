package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/manifest"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/publish"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
	"github.com/guided-traffic/trove/internal/formats/npm/tarball"
)

// maxSigstoreBytes bounds a provenance bundle: a Sigstore bundle is a few
// kilobytes, and one is held in the spool next to the tarball.
const maxSigstoreBytes = 256 << 10

// base64Len is the length of the base64 form of n bytes.
func base64Len(n int64) int64 { return (n + 2) / 3 * 4 }

// bodySlack is what R-NPM-007 step 2 adds to the base64 form of the tarball for the rest of
// the request: the metadata budget of the scanner (the manifest, the descriptors and the
// dist-tags) and the base64 form of the largest provenance bundle, whose data is outside that
// budget. Each part is within its own limit, so the sum of the limits is the cap.
const bodySlack = publish.DefaultMaxMetadataBytes + (maxSigstoreBytes+2)/3*4

// bodyLimit is the cap of a publish request: the base64 form of the tarball limit plus
// the slack (R-NPM-007 step 2).
func bodyLimit(maxTarball int64) int64 { return base64Len(maxTarball) + bodySlack }

// handlePublish is PUT {name} (R-NPM-007, flow F1). The body is read once, as a
// stream: the tarball goes decoded into the spool while its digests are computed,
// everything else is held within the limits of the scanner; nothing is held in a
// transaction while bytes arrive (R-NPM-046). Validation runs before the commit
// transaction, and the rule that makes the commit safe (the package row lock, the
// ownership, the immutability, the name fold) runs inside it.
func (f *Format) handlePublish(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	rep := f.replicaOf(rq.Host)
	cfg := rep.cfg.Publish
	n, err := name.Parse(rq.Params.Get("name"))
	if err != nil {
		return err
	}
	// The name decides before a byte of the body is read; the version, which the body
	// names, decides again once it is known.
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}
	// The ownership rule decides before the body is read too (S-4, R-AUTHZ-018): a caller who
	// could not commit costs no spool and no validation.
	if err := f.precheckOwnership(ctx, rq, n.String()); err != nil {
		return err
	}
	limit := bodyLimit(int64(cfg.MaxTarballSize))
	if rq.ContentLength > limit {
		return manifest.TooLarge(fmt.Sprintf("the publish request is larger than %d bytes", limit))
	}

	var tarSpool, sigSpool formats.Spooled
	spoolRq := withoutLength(rq)
	sink := func(file string, data io.Reader) (any, error) {
		slot, max := &tarSpool, int64(cfg.MaxTarballSize)
		switch {
		case strings.HasSuffix(file, tarballSuffix):
		case strings.HasSuffix(file, sigstoreSuffix):
			slot, max = &sigSpool, maxSigstoreBytes
		default:
			return nil, manifest.BadPublish("the publish request carries an attachment that is neither a .tgz nor a .sigstore file")
		}
		if *slot != nil {
			return nil, manifest.BadPublish("the publish request carries more than one " + file[strings.LastIndexByte(file, '.'):] + " attachment")
		}
		sp, err := rq.Host.Ingest().Spool(ctx, spoolRq, data, formats.IngestOptions{MediaType: "application/octet-stream", MaxSize: max})
		if err != nil {
			return nil, spoolError(err)
		}
		*slot = sp
		return sp, nil
	}
	env, err := publish.Scan(ctx, http.MaxBytesReader(w, rq.Body, limit), publish.Limits{MaxDecoded: int64(cfg.MaxTarballSize)}, sink)
	if err != nil {
		return ioError(err)
	}
	if len(env.Attachments) == 0 {
		return f.metadataUpdate(ctx, rq, w, n, env)
	}
	req, err := env.Request(n.String(), manifest.DefaultLimits())
	if err != nil {
		return err
	}
	version := req.Version.Canonical()
	sum := tarSpool.Digests()
	if err := req.CheckDigests(sum.SHA512, sum.SHA1); err != nil {
		return err
	}
	res, err := validateTarball(ctx, tarSpool, tarball.Limits{
		MaxCompressedBytes: int64(cfg.MaxTarballSize), MaxUnpackedBytes: int64(cfg.MaxUnpackedSize), MaxEntries: cfg.MaxTarEntries,
	})
	if err != nil {
		return err
	}
	if err := manifest.CheckIdentity(res.Manifest, n.String(), version); err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	audit := auditOf(rq)
	if diff := manifest.Mismatches(req.Manifest, res.Manifest, res.Facts); len(diff) > 0 {
		if err := st.RecordMismatch(ctx, rows.MismatchInput{Name: n.String(), Version: version, Fields: diff, Mode: cfg.ManifestConsistency, Audit: audit}); err != nil {
			return err
		}
		if cfg.ManifestConsistency == "reject" {
			return manifest.CheckConsistency(req.Manifest, res.Manifest, res.Facts)
		}
	}
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: n.String(), Version: version, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
		return err
	}

	// Step 4, outside any transaction: the version exists, was unpublished, or is new.
	integrity := res.Integrity()
	repo := rq.RepoRef()
	facts, err := st.VersionFacts(ctx, repo.ID, n.String(), version)
	if err != nil {
		return err
	}
	if done, err := f.versionTaken(ctx, rq, w, rep, st, req, facts, integrity, sigSpool); done || err != nil {
		return err
	}
	shadow, shadowSkipped, err := f.shadowCheck(ctx, rq, n.String())
	if err != nil {
		return err
	}

	insp := &formats.Inspection{
		Package: n.String(), Version: version,
		Properties: map[string]string{"npm.name": n.String(), "npm.version": version},
	}
	pc := &publishCommit{
		name: n, version: version, tag: req.Tag, res: res, principal: rq.Subject.ID, audit: audit,
		installScript: manifest.HasInstallScript(res.Manifest, res.Facts), shadowing: shadow, shadowSkipped: shadowSkipped,
		latestGuard: settingsOf(rq.Repo.Config).LatestGuard,
	}
	pc.stored = manifest.Stored(req.Manifest, manifest.ServerFields{
		Name: n.String(), Version: version, Principal: principalName(rq), Integrity: integrity, Shasum: res.SHA1Hex(),
		FileCount: res.FileCount, UnpackedSize: res.UnpackedSize, HasInstallScript: pc.installScript, HasShrinkwrap: res.HasShrinkwrap,
	})
	commit := formats.Commit{
		Path: formats.Path{Path: TarballPath(n, version)}, MediaType: "application/octet-stream",
		Inspection: insp, Payload: pc,
	}
	// The provenance bundle commits with the tarball, in the one transaction of the
	// publish (R-ARCH-009): a version is never visible without the bundle the client sent.
	sc := &sigstoreCommit{name: n, version: version}
	if sigSpool != nil {
		commit.Companions = []formats.Companion{{
			Spooled: sigSpool, Path: formats.Path{Path: SigstorePath(n, version)}, MediaType: req.Sigstore.ContentType, Payload: sc,
		}}
	}
	committed, err := rq.Host.Ingest().Commit(ctx, rq, tarSpool, commit)
	if err != nil {
		return f.commitError(ctx, rq, w, rep, st, req, err, integrity, sigSpool)
	}
	if committed.Identical {
		// The item was there already with these bytes: a publish that committed on
		// another replica meanwhile. The hook did not run here.
		facts, err := st.VersionFacts(ctx, repo.ID, n.String(), version)
		if err != nil {
			return err
		}
		_, err = f.versionTakenAfterCommit(ctx, rq, w, rep, st, req, facts, integrity, sigSpool)
		return err
	}
	rev := pc.rev
	if sigSpool != nil {
		rev = sc.rev
	}
	if pc.kept {
		w.Header().Set("npm-notice", "latest stays at "+pc.latest+"; move it with npm dist-tag add")
	}
	return writePublished(w, n.String(), Rev(repo.ID, pc.pkg, rev))
}

// withoutLength is the request as the spool sees it: the body it reads is the
// decoded attachment, whose length the request's Content-Length does not state.
func withoutLength(rq *formats.Request) *formats.Request {
	r := *rq.Request
	r.ContentLength = -1
	c := *rq
	c.Request = &r
	return &c
}

// spoolError reports a spool that grew past its limit as the 413 of npm.
func spoolError(err error) error {
	if e, ok := errs.As(err); ok && e.Kind == errs.TooLarge {
		return manifest.TooLarge(e.Detail)
	}
	return ioError(err)
}

// ioError classifies what a read or a pass over the body returned that is not an
// error of the publish: a cancelled or expired request is a failure to retry.
func ioError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the publish was complete.", errs.WithCause(err))
	}
	return err
}

// validateTarball runs the streaming pass of R-NPM-007 step 3 over the spool.
func validateTarball(ctx context.Context, sp formats.Spooled, lim tarball.Limits) (*tarball.Result, error) {
	rc, err := sp.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	res, err := tarball.Validate(ctx, rc, lim)
	if err != nil {
		if _, ok := errs.As(err); ok {
			return nil, err
		}
		return nil, ioError(err)
	}
	return res, nil
}

// Inspect reads the identity of a tarball (FMT-008) under the limits of the
// inspection (R-ARCH-028): its entries, the size, depth and compression ratio of
// the package.json, and the deadline. A publish does not come through here: it
// validates with the limits of npm.publish.*, which are the format's own and above
// these (R-NPM-007), and answers 400 where Inspect answers 422. What the archive
// rules refuse as ambiguous or forbidden is content trove does not read, 422
// content_refused; a bound that is crossed is 413.
func (f *Format) Inspect(ctx context.Context, in formats.InspectInput) (*formats.Inspection, error) {
	lim := in.Limits
	if lim.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, lim.Deadline)
		defer cancel()
	}
	res, err := validateTarball(ctx, in.Body, tarball.Limits{
		MaxEntries: lim.Entries, MaxManifestBytes: lim.ManifestEntryBytes, MaxManifestDepth: lim.JSONDepth, MaxRatio: lim.Ratio,
	})
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.Invalid {
			return nil, errs.E(errs.Unprocessable, errs.CodeContentRefused, e.Detail, errs.WithCause(err))
		}
		return nil, err
	}
	pkg, _ := res.Manifest["name"].(string)
	version, _ := res.Manifest["version"].(string)
	props := map[string]string{"npm.name": pkg, "npm.version": version}
	if n, err := name.Parse(pkg); err == nil && n.IsScoped() {
		props["npm.scope"] = "@" + n.Scope()
	}
	return &formats.Inspection{Package: pkg, Version: version, Properties: props, Payload: res}, nil
}

// versionTaken applies the immutability rule and the acknowledged retry of
// R-NPM-007 step 4 and R-NPM-008 to the facts read before the commit. It returns
// done when it answered the request.
func (f *Format) versionTaken(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store,
	req *publish.Request, facts rows.VersionFacts, integrity string, sig formats.Spooled,
) (bool, error) {
	switch {
	case facts.Tombstoned:
		return false, errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished,
			req.Name.String()+"@"+req.Version.Canonical()+" was unpublished and is never accepted again; publish a new version.")
	case !facts.Live:
		return false, nil
	}
	return f.versionTakenAfterCommit(ctx, rq, w, rep, st, req, facts, integrity, sig)
}

// versionTakenAfterCommit decides a request whose version is live: it is the retry
// of a publish whose answer was lost, and is acknowledged, or it is a second
// publish and answers 409 npm_version_exists.
func (f *Format) versionTakenAfterCommit(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store,
	req *publish.Request, facts rows.VersionFacts, integrity string, sig formats.Spooled,
) (bool, error) {
	n, version := req.Name, req.Version.Canonical()
	switch {
	case facts.Tombstoned:
		return false, errs.E(errs.Conflict, errs.CodeNPMVersionUnpublished,
			n.String()+"@"+version+" was unpublished and is never accepted again; publish a new version.")
	case !facts.Live:
		return false, errs.E(errs.Conflict, errs.CodeNPMVersionExists, n.String()+"@"+version+" could not be published; read it again and retry.")
	}
	ok, err := f.isRetry(ctx, rq, rep, req, facts, integrity, sig)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, errs.E(errs.Conflict, errs.CodeNPMVersionExists,
			n.String()+"@"+version+" is published already; a version is never accepted again with other bytes.")
	}
	audit := auditOf(rq)
	err = rq.Host.Write(ctx, rq, func(tx formats.Tx, _ formats.PackageWriter) error {
		return st.RecordRetry(ctx, tx, rows.RetryInput{Name: n.String(), Version: version, Integrity: integrity, Tag: req.Tag, Audit: audit})
	})
	if err != nil {
		return false, err
	}
	rep.met.retryAcknowledged(rq.Repo.Key)
	row, found, err := rq.Host.Packages().Package(ctx, rq.RepoRef(), n.String())
	if err != nil {
		return false, err
	}
	if !found {
		return false, errs.E(errs.Internal, errs.CodeInternal, "The package row of a published version is missing.")
	}
	w.Header().Set("npm-notice", fmt.Sprintf("%s@%s was already published by you at %s; nothing changed",
		n, version, facts.PublishedAt.UTC().Format(time.RFC3339)))
	return true, writePublished(w, n.String(), Rev(rq.RepoRef().ID, row.Ref.ID, row.Ref.RevSeq))
}

// isRetry reports whether the request is the retry of the publish that created the
// live version (Q-NPM-11): the same principal, within npm.publish.retry_window on
// PostgreSQL's clock, the same SHA-512, the same dist-tag, and a provenance bundle
// exactly when the version has one, with the same SHA-256.
func (f *Format) isRetry(ctx context.Context, rq *formats.Request, rep *replica, req *publish.Request, facts rows.VersionFacts,
	integrity string, sig formats.Spooled,
) (bool, error) {
	window := rep.cfg.Publish.RetryWindow
	sub := rq.Subject
	switch {
	case window <= 0, sub.Anonymous, sub.ID == "", facts.PublishedBy != sub.ID:
		return false, nil
	case facts.Age >= window, facts.Integrity != integrity, facts.PublishTag != req.Tag:
		return false, nil
	}
	stored, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: SigstorePath(req.Name, req.Version.Canonical())})
	if err != nil {
		return false, err
	}
	has := found && !stored.Folder
	if has != (sig != nil) {
		return false, nil
	}
	return !has || bytes.Equal(stored.Digests.SHA256, sig.Digests().SHA256), nil
}

// commitError decides what a failed commit means. A path that took other content
// meanwhile answers 403 from the item store, which for a version number is the
// immutability rule's 409.
func (f *Format) commitError(ctx context.Context, rq *formats.Request, w http.ResponseWriter, rep *replica, st Store,
	req *publish.Request, err error, integrity string, sig formats.Spooled,
) error {
	if e, ok := errs.As(err); ok && e.Kind == errs.Forbidden && e.Code == errs.CodeForbidden {
		facts, ferr := st.VersionFacts(ctx, rq.RepoRef().ID, req.Name.String(), req.Version.Canonical())
		if ferr == nil && (facts.Live || facts.Tombstoned) {
			_, verr := f.versionTakenAfterCommit(ctx, rq, w, rep, st, req, facts, integrity, sig)
			return verr
		}
	}
	return err
}

// shadowCheck is step 6 of the flow: the first publish of a name that a remote
// member of a containing virtual repository serves must not shadow it (R-REPO-048).
// It runs with no transaction open. A refusal is the 403 or 503 the check built, which
// names the package and the virtual repositories the caller can read and no remote
// member; a write that proceeds hands the commit what the check found, for the audit
// events of the transaction. skipped says that the repository held a live version, so
// that nothing was asked: the commit must not find the publish to be the first one then.
func (f *Format) shadowCheck(ctx context.Context, rq *formats.Request, pkg string) (found rows.Shadowing, skipped bool, err error) {
	live, _, err := rq.Host.Packages().Versions(ctx, rq.RepoRef(), pkg)
	if err != nil {
		return rows.Shadowing{}, false, err
	}
	if len(live) > 0 {
		return rows.Shadowing{}, true, nil
	}
	out, err := rq.Host.Resolver().ShadowCheck(ctx, rq, pkg)
	if err != nil {
		return rows.Shadowing{}, false, err
	}
	if !out.Proceed {
		if out.Refusal != nil {
			return rows.Shadowing{}, false, out.Refusal
		}
		return rows.Shadowing{}, false, errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether the name is served by a remote repository could not be determined; try again.", errs.WithRetryAfter(out.RetryAfter))
	}
	return shadowing(out), false, nil
}

// precheckOwnership asks the ownership rule of R-AUTHZ-018 before the body is read: a caller who
// may not write the package is refused before a byte is spooled or validated. It reads the rows
// and writes nothing; the commit decides again under the claim row lock and claims. The write is
// asked as a change of the package, not as a claim, so that the early answer never refuses what
// the commit would allow (a claim needs a designated owner, which only the commit can know).
func (f *Format) precheckOwnership(ctx context.Context, rq *formats.Request, pkg string) error {
	live, _, err := rq.Host.Packages().Versions(ctx, rq.RepoRef(), pkg)
	if err != nil {
		return err
	}
	return rq.Host.CheckOwnership(ctx, rq, formats.Target{Package: pkg, Actions: []formats.Action{formats.ActionDeploy}},
		formats.OwnershipWrite{FirstVersion: len(live) == 0})
}

// writePublished answers a publish: 201 and the body npm ignores.
func writePublished(w http.ResponseWriter, pkg, rev string) error {
	return writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": pkg, "rev": rev})
}

// writeJSON writes a JSON body with its status.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(b)
	return err
}

// auditOf is the audit context of a write: the repository, the endpoint and the
// actor, from the request's subject.
func auditOf(rq *formats.Request) rows.Audit {
	s := rq.Subject
	kind := string(s.Kind)
	if s.Anonymous {
		kind = "anonymous"
	}
	return rows.Audit{
		RepoKey: rq.Repo.Key, Endpoint: s.Endpoint,
		Actor: rows.Actor{Kind: kind, ID: s.ID, Name: s.Name, TokenID: s.TokenID, AuthMethod: s.AuthMethod},
	}
}

// principalName is the name of the principal as R-AUTH-033 renders it for a client:
// a user's name, "sa:<name>" for a service account, "ci:<name>" for a CI identity.
func principalName(rq *formats.Request) string {
	s := rq.Subject
	switch string(s.Kind) {
	case "service_account":
		return "sa:" + s.Name
	case "ci":
		return "ci:" + s.Name
	}
	return s.Name
}
