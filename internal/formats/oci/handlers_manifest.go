package oci

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// Pulling and pushing manifests (R-OCI-009, R-OCI-013, R-OCI-014). Both handlers serve the local class; the remote and the virtual
// class join with their adapters, which answer through the same routes.

func errDigestInvalid(detail string) error {
	return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, detail)
}

func errTagInvalid() error {
	return errs.E(errs.Invalid, errs.CodeOCITagInvalid, "A tag matches [a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}.")
}

func errUnsupported(detail string) error {
	return errs.E(errs.Invalid, errs.CodeOCIUnsupported, detail)
}

// storeOf is the data port of the format as the host binds it.
func storeOf(h formats.Host) (Store, error) { return portOf(h.FormatData(ID)) }

// parseLimits are the limits a manifest is read under (R-OCI-020): the defaults of untrusted content, tightened by the descriptor of
// the format (nesting, values) and bounded by the document size.
func (f *Format) parseLimits() untrusted.Limits {
	return untrusted.DefaultLimits().Tighten(f.Descriptor().Limits).Tighten(untrusted.Limits{DocumentBytes: MaxDocumentBytes})
}

// digestHex is "sha256:<hex>" of a stored item's digest.
func digestOf(sha256 []byte) string { return "sha256:" + hex.EncodeToString(sha256) }

// validatorWriter presents the validators of a stored item the way the registry API spells them (R-OCI-008): the digest in
// Docker-Content-Digest and the ETag as "sha256:<hex>". Headers of a response that carries the item are set when its status line
// goes out, so an error answer carries neither. contentType, when set, replaces the stored media type (a blob is octet-stream).
type validatorWriter struct {
	http.ResponseWriter
	digest      string
	contentType string
}

func (w validatorWriter) WriteHeader(code int) {
	switch code {
	case http.StatusOK, http.StatusPartialContent, http.StatusNotModified:
		h := w.Header()
		h.Set("Docker-Content-Digest", w.digest)
		h.Set("ETag", `"`+w.digest+`"`)
		if w.contentType != "" && code != http.StatusNotModified {
			h.Set("Content-Type", w.contentType)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w validatorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// validatorHeaders are the request headers that carry entity tags.
func validatorHeaders() [3]string { return [3]string{"If-None-Match", "If-Match", "If-Range"} }

// storedValidators turns the entity tags of the request from the form the registry hands out ("sha256:<hex>") into the form the item
// store compares ("<hex>"), so If-None-Match of a client that sends back what it was given answers 304.
func storedValidators(rq *formats.Request) *formats.Request {
	hit := false
	for _, k := range validatorHeaders() {
		if strings.Contains(rq.Header.Get(k), `"sha256:`) {
			hit = true
		}
	}
	if !hit {
		return rq
	}
	r2 := rq.Clone(rq.Context())
	for _, k := range validatorHeaders() {
		vs := r2.Header.Values(k)
		for i, v := range vs {
			vs[i] = strings.ReplaceAll(v, `"sha256:`, `"`)
		}
	}
	c := *rq
	c.Request = r2
	return &c
}

// serveStored serves the item at a canonical path of the addressed repository, or answers missing() when it holds none.
func serveStored(ctx context.Context, rq *formats.Request, w http.ResponseWriter, path, contentType string, missing func() error) error {
	it, found, err := rq.Host.Items().Stat(ctx, rq.RepoRef(), formats.Path{Path: path})
	if err != nil {
		return err
	}
	if !found || it.Folder || len(it.Digests.SHA256) != 32 {
		return missing()
	}
	vw := validatorWriter{ResponseWriter: w, digest: digestOf(it.Digests.SHA256), contentType: contentType}
	return rq.Host.Items().Serve(ctx, storedValidators(rq), vw, it, formats.ServeStored)
}

// handleManifestGet is GET and HEAD of a manifest by tag or by digest (R-OCI-009): the stored bytes with their media type, whatever
// Accept says, so the digest stays the one the client verifies (Q-OCI-8).
func (f *Format) handleManifestGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	switch rq.Repo.Class {
	case formats.Remote:
		return f.remoteManifestGet(ctx, rq, w)
	case formats.Local:
	default:
		return f.virtualManifestGet(ctx, rq, w)
	}
	name, ref := rq.Params.Get("name"), rq.Params.Get("reference")
	var path, version string
	if strings.Contains(ref, ":") {
		if !ValidDigest(ref) {
			return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
		}
		path = ManifestPath(name, ref)
	} else {
		if !ValidTag(ref) {
			return errManifestUnknown()
		}
		version = ref
		path = TagPath(name, ref)
	}
	// The coordinate is the item the request reads: the path decides the grants and the repository's patterns that are scoped by path
	// (R-AUTHZ-005), the name the grants scoped by package. A pull by digest names no version: the exposure rules that narrow by
	// tag do not apply to it, and the format has no exposure adapter yet (Deferred).
	target := formats.Target{Package: name, Version: version, Path: path, Actions: []formats.Action{formats.ActionRead}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	return serveStored(ctx, rq, w, path, "", errManifestUnknown)
}

// pushed is what a push of a manifest hands the storage hooks (R-OCI-013 step 8).
type pushed struct {
	name string
	// tag is the tag of a push by tag, "" for a push by digest.
	tag string
	m   *manifest.Manifest
	// linked says the tag item is committed as a link to the stored manifest, which the hook of the tag then checks again in the
	// transaction.
	linked bool
	// shadowSkipped says that the shadowing check asked nothing because the name held a live version when it ran; a commit that
	// then finds the push to be the name's first version asks the client to retry, so that the check runs (R-REPO-048).
	shadowSkipped bool
}

// tagCommit is the payload of the tag item, the lead of a push by tag; manifestCommit the payload of the manifest item, the companion
// of a push by tag or the lead of a push by digest.
type (
	tagCommit      struct{ *pushed }
	manifestCommit struct{ *pushed }
)

// unsupportedQuery refuses the parameters of the distribution specification that this registry does not offer (R-OCI-002,
// R-OCI-013): extra tags in the query, and a digest algorithm other than sha256.
func unsupportedQuery(rq *formats.Request) error {
	q := rq.URL.Query()
	if _, ok := q["tag"]; ok {
		return errUnsupported("Extra tags in the query (?tag=) are not supported; push the manifest once per tag.")
	}
	for _, a := range q["digest-algorithm"] {
		if a != "sha256" {
			return errUnsupported("Only the digest algorithm sha256 is supported.")
		}
	}
	return nil
}

// handleManifestPut is PUT of a manifest by tag or by digest (R-OCI-013).
//
// The body is read once into the spool, parsed and checked against the repository outside any transaction; what makes the commit safe
// (the package row lock, the reference check on locked rows, the ownership claim) runs again in the commit transaction. A push by tag
// commits the tag item as the lead and the manifest item as its companion, or links the tag to the stored manifest; a push by digest
// has the manifest as the lead.
//
// Through a virtual repository the push is decided and committed on its deployment target (rq below is the request made for the target,
// addr the one the client addressed, which the Location is built for), and the blobs and manifests it names that the target lacks are
// linked in from the members first (R-OCI-028).
func (f *Format) handleManifestPut(ctx context.Context, addr *formats.Request, w http.ResponseWriter) error {
	rq, err := writeRequest(ctx, addr)
	if err != nil {
		return err
	}
	name, ref := rq.Params.Get("name"), rq.Params.Get("reference")
	byDigest := strings.Contains(ref, ":")
	switch {
	case byDigest && !ValidDigest(ref):
		return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	case !byDigest && !ValidTag(ref):
		return errTagInvalid()
	}
	if err := unsupportedQuery(rq); err != nil {
		return err
	}
	mt, err := manifest.MediaTypeOf(rq.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	tag, path := "", ManifestPath(name, ref)
	if !byDigest {
		tag, path = ref, TagPath(name, ref)
	}
	st, err := storeOf(rq.Host)
	if err != nil {
		return err
	}
	// The caller holds deploy on the repository (the coarse check ran before the body is read); here it is decided for the name and
	// the tag, with the patterns of the repository and the ownership of an image name that has an owner, before the body is read.
	target := formats.Target{Package: name, Version: tag, Path: path, Actions: []formats.Action{formats.ActionDeploy}}
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	repo := rq.RepoRef()
	live, _, err := rq.Host.Packages().Versions(ctx, repo, name)
	if err != nil {
		return err
	}
	// The pre-check says what the commit will: a tag that exists is moved or pushed again, which is a change by an owner or a writer
	// and never a publish; a new tag and a manifest by digest add content.
	publish := true
	if !byDigest {
		if _, found, err := rq.Host.Items().Stat(ctx, repo, formats.Path{Path: path}); err != nil {
			return err
		} else if found {
			publish = false
		}
	}
	if err := rq.Host.CheckOwnership(ctx, rq, target, formats.OwnershipWrite{Publish: publish, FirstVersion: len(live) == 0}); err != nil {
		return err
	}
	sp, err := rq.Host.Ingest().Spool(ctx, rq, rq.Body, formats.IngestOptions{MediaType: mt, MaxSize: MaxManifestBytes, Length: rq.ContentLength})
	if err != nil {
		return ioError(err)
	}
	data, err := readSpool(sp)
	if err != nil {
		return err
	}
	m, err := manifest.Parse(data, mt, f.parseLimits())
	if err != nil {
		return err
	}
	if byDigest && ref != m.Digest {
		return errDigestInvalid("The digest of the manifest is " + m.Digest + ", not the one in the request.")
	}
	if _, withheld, err := st.Tombstone(ctx, repo.ID, name, m.Sum); err != nil {
		return err
	} else if withheld {
		return errs.E(errs.Conflict, errs.CodeConflict,
			"The manifest "+m.Digest+" was deleted and is withheld after a restore; an administrator reinstates it before it is pushed again.")
	}
	if addr.Repo.Class == formats.Virtual {
		if err := f.linkFromMembers(ctx, addr, rq, st, name, len(live), m, 0); err != nil {
			return err
		}
	}
	if err := checkReferences(ctx, st, nil, repo.ID, name, m); err != nil {
		return err
	}
	skipped, err := f.shadowCheck(ctx, rq, name, len(live))
	if err != nil {
		return err
	}
	p := &pushed{name: name, tag: tag, m: m, shadowSkipped: skipped}
	if byDigest {
		_, err = rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{
			Path: formats.Path{Path: path}, MediaType: m.MediaType, Payload: &manifestCommit{p},
		})
	} else {
		err = f.commitTag(ctx, rq, st, sp, p)
	}
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Docker-Content-Digest", m.Digest)
	h.Set("Location", addr.Host.Links(addr).Path(name+"/manifests/"+m.Digest))
	if m.Subject != nil {
		h.Set("OCI-Subject", m.Subject.Digest)
	}
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
	return nil
}

// maxTagAttempts bounds the rounds of a push by tag that a concurrent push of the same tag disturbs.
const maxTagAttempts = 3

// commitTag commits the tag item of a push by tag. The tag is decided from what it points at when the commit begins: a new tag, or
// one that points at the same digest, needs deploy; one that points at another digest also needs overwrite (R-OCI-006). A concurrent
// push of the same tag can make the decision stale between the read and the commit; the item store then refuses the overwrite the
// decision did not cover, and the tag is read and decided again.
func (f *Format) commitTag(ctx context.Context, rq *formats.Request, st Store, sp formats.Spooled, p *pushed) error {
	repo, m := rq.RepoRef(), p.m
	tagPath, mPath := TagPath(p.name, p.tag), ManifestPath(p.name, m.Digest)
	// The manifest is stored when its item is live and its row is recorded: the tag is then linked to it, without a second copy.
	linkable := false
	if it, found, err := rq.Host.Items().Stat(ctx, repo, formats.Path{Path: mPath}); err != nil {
		return err
	} else if found && !it.Folder {
		_, row, err := st.Manifest(ctx, repo.ID, p.name, m.Sum)
		if err != nil {
			return err
		}
		linkable = row
	}
	var last error
	for range maxTagAttempts {
		cur, found, err := rq.Host.Items().Stat(ctx, repo, formats.Path{Path: tagPath})
		if err != nil {
			return err
		}
		moving := found && !bytes.Equal(cur.Digests.SHA256, m.Sum[:])
		actions := []formats.Action{formats.ActionDeploy}
		if moving {
			actions = append(actions, formats.ActionOverwrite)
		}
		if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: p.name, Version: p.tag, Path: tagPath, Actions: actions}); err != nil {
			return err
		}
		if found && !moving && !linkable {
			// The tag is pushed again and holds this digest, but the manifest item is gone (a raw delete): the identical tag commits
			// nothing, so the manifest is committed on its own, as a push by digest would, to make the tag pullable again.
			if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: p.name, Path: mPath, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
				return err
			}
			_, err := rq.Host.Ingest().Commit(ctx, rq, sp, formats.Commit{Path: formats.Path{Path: mPath}, MediaType: m.MediaType, Payload: &manifestCommit{p}})
			return err
		}
		c := formats.Commit{Path: formats.Path{Path: tagPath}, MediaType: m.MediaType, MayOverwrite: moving, Payload: &tagCommit{p}}
		if linkable {
			p.linked = true
			_, err = rq.Host.Ingest().Link(ctx, rq, formats.SourceRef{Repo: rq.Repo.Name, Path: mPath}, c)
			if errs.KindOf(err) == errs.NotFound {
				// The manifest went away meanwhile, or the caller may not read it back: commit it with the tag.
				linkable, p.linked = false, false
				last = err
				continue
			}
		} else {
			// The manifest item is a companion of the tag item, in the same transaction; it is the same bytes, so the spool is shared.
			if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: p.name, Path: mPath, Actions: []formats.Action{formats.ActionDeploy}}); err != nil {
				return err
			}
			c.Companions = []formats.Companion{{
				Spooled: sp, Path: formats.Path{Path: mPath}, MediaType: m.MediaType, Payload: &manifestCommit{p},
			}}
			_, err = rq.Host.Ingest().Commit(ctx, rq, sp, c)
		}
		if err == nil {
			return nil
		}
		if e, ok := errs.As(err); ok && e.Kind == errs.Forbidden && e.Code == errs.CodeForbidden && !moving {
			last = err
			continue
		}
		return err
	}
	return last
}

// checkReferences is R-OCI-013 step 4: everything the manifest names must be stored under the name, a layer that is foreign and the
// subject excepted, and a child of an index must not be a manifest the journal replay withheld. With a transaction the rows are held
// against a removal until it ends.
func checkReferences(ctx context.Context, st Store, tx formats.Tx, repoID int64, name string, m *manifest.Manifest) error {
	var paths []string
	var children [][32]byte
	for _, r := range m.References() {
		if !r.Required {
			continue
		}
		if r.Kind == manifest.KindManifest {
			sum, _ := manifest.ParseDigest(r.Digest)
			children = append(children, sum)
			paths = append(paths, ManifestPath(name, r.Digest))
		} else {
			paths = append(paths, BlobPath(name, r.Digest))
		}
	}
	have, err := st.ExistingItems(ctx, tx, repoID, paths)
	if err != nil {
		return err
	}
	stored := make(map[string]struct{}, len(have))
	for _, p := range have {
		stored[p] = struct{}{}
	}
	missing := 0
	first := ""
	for _, p := range paths {
		if _, ok := stored[p]; !ok {
			if missing == 0 {
				first = p[strings.LastIndexByte(p, '/')+1:]
			}
			missing++
		}
	}
	if missing > 0 {
		return errs.E(errs.Invalid, errs.CodeOCIManifestBlobUnknown,
			"The manifest names "+strconv.Itoa(missing)+" blob or manifest that the repository does not hold under this image name; the first is "+first+".")
	}
	withheld, err := st.WithheldAmong(ctx, tx, repoID, name, children)
	if err != nil {
		return err
	}
	if len(withheld) > 0 {
		return errs.E(errs.Invalid, errs.CodeOCIManifestBlobUnknown,
			"The manifest names a manifest that was deleted and is withheld after a restore: sha256:"+hex.EncodeToString(withheld[0][:])+".")
	}
	return nil
}

// shadowCheck is the shadowing check of the first version of a name (R-OCI-006, R-REPO-048). It runs with no transaction open.
// skipped says that the repository held a live version, so nothing was asked: the commit must not find the push to be the first one
// then.
func (f *Format) shadowCheck(ctx context.Context, rq *formats.Request, name string, live int) (skipped bool, err error) {
	if live > 0 {
		return true, nil
	}
	out, err := rq.Host.Resolver().ShadowCheck(ctx, rq, name)
	if err != nil {
		return false, err
	}
	if !out.Proceed {
		if out.Refusal != nil {
			return false, out.Refusal
		}
		return false, errs.E(errs.Unavailable, errs.CodeShadowCheckUnavailable,
			"Whether the image name is served by a remote repository could not be determined; try again.", errs.WithRetryAfter(out.RetryAfter))
	}
	return false, nil
}

// readSpool reads the spooled body, which the spool bounded at the manifest limit.
func readSpool(sp formats.Spooled) ([]byte, error) {
	r, err := sp.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := untrusted.ReadAll(r, MaxManifestBytes+1)
	if err != nil {
		if untrusted.IsTooLarge(err) {
			return nil, errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, "The manifest is larger than 4 MiB.")
		}
		return nil, err
	}
	return data, nil
}

// ioError classifies what a read of the body returned that is no refusal of the manifest: a cancelled or expired request is a failure
// to retry.
func ioError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errs.E(errs.Unavailable, errs.CodeUnavailable, "The request ended before the push was complete.", errs.WithCause(err))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errs.E(errs.Invalid, errs.CodeOCIManifestInvalid, "The body ended before its declared length.", errs.WithCause(err))
	}
	return err
}
