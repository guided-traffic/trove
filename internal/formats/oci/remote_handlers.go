package oci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// What a client sees of a remote repository (R-OCI-021 to R-OCI-025). The routes are the ones of the local class; the handlers of
// handlers_manifest.go, handlers_blob.go, handlers_tags.go and handlers_referrers.go hand a request addressed to a remote repository
// to these. A manifest by tag and the tags list are metadata documents of the engine, a manifest by digest and a blob are artifacts
// it fills once, a window beyond the stored tags list and the referrers are passed through.

// libraryName is the image name a request is made for: the name the client sent, and below library/ a one-component name of a repository
// whose upstream is Docker Hub itself, so that nginx and library/nginx are one cached copy (R-OCI-025). No other upstream gets the
// mapping, and neither does a Docker Hub URL with a namespace prefix: below https://registry-1.docker.io/bitnami a name is bitnami/<name>,
// whose upstream spelling has no library/ (Q-OCI-129).
func libraryName(upstreamURL, name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	if u, err := url.Parse(upstreamURL); err == nil && strings.EqualFold(u.Hostname(), DockerHubHost) && namePrefix(u) == "" {
		return "library/" + name
	}
	return name
}

func remoteName(rq *formats.Request, name string) string {
	if st, ok := rq.Repo.Remote(); ok {
		return libraryName(st.URL, name)
	}
	return name
}

// otherSpelling is the second spelling of a name on a Docker Hub remote without a namespace prefix: `library/nginx` for `nginx` and `nginx`
// for `library/nginx`. "" for every other name and upstream.
func otherSpelling(rq *formats.Request, client string) string {
	if st, ok := rq.Repo.Remote(); ok {
		return otherSpellingOf(st.URL, client)
	}
	return ""
}

func otherSpellingOf(upstreamURL, client string) string {
	if upstreamURL == "" {
		return ""
	}
	if mapped := libraryName(upstreamURL, client); mapped != client {
		return mapped
	}
	if rest, ok := strings.CutPrefix(client, "library/"); ok && !strings.Contains(rest, "/") && libraryName(upstreamURL, rest) == client {
		return rest
	}
	return ""
}

// SourceSpellings is the other spelling of the item a cross-repository mount takes its blob from (Q-OCI-129): on a Docker Hub remote without
// a namespace prefix `library/nginx/_blobs/<d>` is also `nginx/_blobs/<d>` and the reverse, and the source is decided on both.
func (f *Format) SourceSpellings(upstreamURL string, p formats.Path) []formats.Path {
	name, kind, tail, ok := splitLayout(p.Path)
	if !ok {
		return nil
	}
	other := otherSpellingOf(upstreamURL, name)
	if other == "" {
		return nil
	}
	if kind == fileTaglist {
		return []formats.Path{{Path: TaglistPath(other)}}
	}
	return []formats.Path{{Path: other + "/" + kind + "/" + tail}}
}

// gateRemote decides a read of a remote repository. The decision point is asked on the name the client typed (Authorize, which audits) and
// on the other spelling (PathsAllowed, which does not): the read must be allowed on both (Q-OCI-129). The cached copy, the package rows, the
// screens and the populate check of a fill use `library/<n>`, so an exclusion written for either spelling, in a policy, a token scope, an
// exposure rule, a pattern or a claim, holds whichever the client types; a grant on a Docker Hub remote has to name both spellings (an
// include-all grant does). target names the client's spelling; pathOf places a name in the layout; deny is the refusal for a name the rules hide.
func gateRemote(ctx context.Context, rq *formats.Request, client string, target formats.Target, pathOf func(name string) string, deny func() error) error {
	if err := rq.Host.Authorize(ctx, rq, target); err != nil {
		return err
	}
	other := otherSpelling(rq, client)
	if other == "" {
		return nil
	}
	allowed, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, []string{pathOf(other)})
	if err != nil {
		return err
	}
	if !allowed[0] {
		return deny()
	}
	return nil
}

// metadataTTL is the period a pass-through answer is kept: the repository's metadata period (R-OCI-024).
func metadataTTL(rq *formats.Request) time.Duration {
	if st, ok := rq.Repo.Remote(); ok && st.Cache.MetadataTTLSeconds > 0 {
		return time.Duration(st.Cache.MetadataTTLSeconds) * time.Second
	}
	return 5 * time.Minute
}

// missing turns what the engine answers for an upstream that does not have the resource into the registry's code for it, and keeps
// the headers the engine put on the refusal (Cache-Status).
func missing(err error, mk func() error) error {
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.NotFound {
		return err
	}
	out := mk()
	if oe, ok := errs.As(out); ok && len(e.Headers) > 0 {
		if oe.Headers == nil {
			oe.Headers = map[string]string{}
		}
		for k, v := range e.Headers {
			oe.Headers[k] = v
		}
	}
	return out
}

// remoteManifestGet is GET and HEAD of a manifest of a remote repository (R-OCI-022): a tag is the stored metadata document, fresh
// for the metadata period and revalidated by a HEAD; a digest is content, filled once and verified against the digest.
func (f *Format) remoteManifestGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	client, ref := rq.Params.Get("name"), rq.Params.Get("reference")
	name := remoteName(rq, client)
	read := []formats.Action{formats.ActionRead}
	if strings.Contains(ref, ":") {
		if !ValidDigest(ref) {
			return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
		}
		path := ManifestPath(name, ref)
		pathOf := func(n string) string { return ManifestPath(n, ref) }
		if err := gateRemote(ctx, rq, client, formats.Target{Package: client, Path: pathOf(client), Actions: read}, pathOf, errManifestUnknown); err != nil {
			return err
		}
		vw := validatorWriter{ResponseWriter: w, digest: ref}
		return missing(rq.Host.Remote().Artifact(ctx, storedValidators(rq), formats.Path{Path: path}, formats.Digests{}, vw), errManifestUnknown)
	}
	if !ValidTag(ref) {
		return errManifestUnknown()
	}
	path := TagPath(name, ref)
	pathOf := func(n string) string { return TagPath(n, ref) }
	if err := gateRemote(ctx, rq, client, formats.Target{Package: client, Version: ref, Path: pathOf(client), Actions: read}, pathOf, errManifestUnknown); err != nil {
		return err
	}
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: path}, "")
	if err != nil {
		// The tag a client falls back to when the referrers API is not there (`sha256-<hex of the subject>`, OCI distribution specification,
		// referrers tag schema) is asked for after every pull of Docker with the containerd image store. When the upstream cannot be
		// asked or failed, it is a tag nobody has found out about: 404, as the referrers are (Q-OCI-204).
		if referrersTag(ref) && (unreachable(err) || upstreamFailed(err)) {
			return errManifestUnknown()
		}
		return missing(err, errManifestUnknown)
	}
	if doc.Status.Negative {
		return missing(errs.E(errs.NotFound, errs.CodeNotFound, "", errs.WithHeader("Cache-Status", doc.Status.Header)), errManifestUnknown)
	}
	body, err := doc.Bytes(ctx)
	if err != nil {
		return err
	}
	return writeStoredDocument(rq, w, doc, body)
}

// writeStoredDocument answers with a stored manifest as it is stored: its bytes and the media type the upstream gave (Q-OCI-8), the
// digest the client verifies, the entity tag the registry hands out and the Cache-Status of the engine. A conditional request that
// matches is answered 304.
func writeStoredDocument(rq *formats.Request, w http.ResponseWriter, doc *formats.RemoteDocument, body []byte) error {
	dgst := "sha256:" + hex.EncodeToString(doc.Digest[:])
	mt := doc.MediaType
	if mt == "" {
		mt = fetchedMediaType(body)
	}
	h := w.Header()
	h.Set("Content-Type", mt)
	h.Set("Docker-Content-Digest", dgst)
	h.Set("ETag", `"`+dgst+`"`)
	h.Set("Cache-Control", "no-cache")
	if doc.Status.Header != "" {
		h.Set("Cache-Status", doc.Status.Header)
	}
	if matchesETag(rq.Header.Get("If-None-Match"), dgst) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(body)
	return err
}

// matchesETag reports whether an If-None-Match value names the digest as the registry hands it out, or is the wildcard.
func matchesETag(header, dgst string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || strings.Trim(part, `"`) == dgst {
			return true
		}
	}
	return false
}

// remoteBlobGet is GET and HEAD of a blob of a remote repository (R-OCI-023): filled once under the engine's flight, verified
// against its digest, served with ranges and conditional requests from the stored item. A HEAD of a blob that is not cached is
// answered from the upstream's HEAD without a fill.
func (f *Format) remoteBlobGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	client, digest := rq.Params.Get("name"), rq.Params.Get("digest")
	name := remoteName(rq, client)
	if !ValidDigest(digest) {
		return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	}
	path := BlobPath(name, digest)
	pathOf := func(n string) string { return BlobPath(n, digest) }
	target := formats.Target{Package: client, Path: pathOf(client), Actions: []formats.Action{formats.ActionRead}}
	if err := gateRemote(ctx, rq, client, target, pathOf, errBlobUnknown); err != nil {
		return err
	}
	vw := validatorWriter{ResponseWriter: w, digest: digest, contentType: blobContentType}
	return missing(rq.Host.Remote().Artifact(ctx, storedValidators(rq), formats.Path{Path: path}, formats.Digests{}, vw), errBlobUnknown)
}

// storedTaglist is the tags list document the engine stores: the upstream's answer and, when the upstream cut it, the member the
// format added (truncatedMember).
type storedTaglist struct {
	Name      string   `json:"name"`
	Tags      []string `json:"tags"`
	Truncated bool     `json:"x-trove-truncated"`
}

// parseTaglist reads an upstream's tags list within the bounds of R-OCI-020: 4 MiB and 100,000 entries. A tag that is none is
// dropped, the rest is in byte order and without repeats.
func parseTaglist(doc []byte) (storedTaglist, error) {
	var out storedTaglist
	lim := untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: MaxDocumentBytes})
	if err := untrusted.UnmarshalJSON(doc, lim, &out); err != nil {
		return out, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's tags list is not a document the registry reads.", errs.WithCause(err))
	}
	if len(out.Tags) > MaxTaglistEntries {
		return out, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's tags list has more than 100,000 entries.")
	}
	out.Tags = slices.DeleteFunc(out.Tags, func(t string) bool { return !ValidTag(t) })
	sort.Strings(out.Tags)
	out.Tags = slices.Compact(out.Tags)
	return out, nil
}

// remoteTagsList is the tags list of a name of a remote repository (R-OCI-024): the page the client asks for, cut from the stored
// document of the upstream's answer, and, when the upstream cut its answer and the window reaches beyond what is stored, continued
// with the upstream's own pages, passed through. A tag the caller may not read is not listed and its name is in no Link: the page is
// filled from the readable tags, as the local class does (Q-OCI-76). Offline, with the circuit open, or for a caller who may not fill
// the repository, the stored window answers.
func (f *Format) remoteTagsList(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	clientName := rq.Params.Get("name")
	name := remoteName(rq, clientName)
	// The gate is the tags list document of the name the client typed, the path the layout places in the image (Q-OCI-140); each tag is
	// decided by its own path below.
	deny := func() error {
		return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The upstream has no image of this name.")
	}
	target := formats.Target{Package: clientName, Path: TaglistPath(clientName), Actions: []formats.Action{formats.ActionRead}}
	if err := gateRemote(ctx, rq, clientName, target, TaglistPath, deny); err != nil {
		return err
	}
	query := rq.URL.Query()
	n, err := pageSize(query)
	if err != nil {
		return err
	}
	last := query.Get("last")
	if last != "" && !ValidTag(last) {
		return errs.E(errs.Invalid, errs.CodeOCITagInvalid, "The parameter last is not a tag.")
	}
	doc := tagsDoc{Name: rq.Host.Links(rq).ClientName(clientName), Tags: []string{}}
	if n == 0 {
		return jsonReply(w, http.StatusOK, "application/json", doc)
	}
	d, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: TaglistPath(name)}, "")
	if err != nil {
		return err
	}
	if d.Status.Negative {
		return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The upstream has no image of this name.", errs.WithHeader("Cache-Status", d.Status.Header))
	}
	raw, err := d.Bytes(ctx)
	if err != nil {
		return err
	}
	stored, err := parseTaglist(raw)
	if err != nil {
		return err
	}
	src := &tagSource{rq: rq, name: name, client: clientName, chunk: stored.Tags[sort.SearchStrings(stored.Tags, last+"\x00"):], cut: stored.Truncated, cursor: last}
	tags, next, err := src.page(ctx, n)
	if err != nil {
		return err
	}
	doc.Tags = tags
	w.Header().Set("Cache-Control", "no-cache")
	if d.Status.Header != "" {
		w.Header().Set("Cache-Status", d.Status.Header)
	}
	if next != "" {
		w.Header().Set("Link", tagsListLink(rq, n, next))
	}
	return jsonReply(w, http.StatusOK, "application/json", doc)
}

// tagSource is the tags a page is filled from: the stored document's window after `last`, and, when the upstream cut its answer, the
// upstream's pages after the last tag taken (R-OCI-024).
type tagSource struct {
	rq *formats.Request
	// name is the name the upstream and the cache know, client the one the caller typed, on which each tag is decided.
	name, client string
	// chunk are the tags not yet decided, in byte order.
	chunk []string
	// cut says that tags may follow chunk at the upstream: the stored document was cut, or chunk is a page the upstream gave.
	cut bool
	// cursor is the last tag taken from the source, the `last` of the next upstream page.
	cursor string
	// passes is the number of upstream pages this request has asked for (maxPassPages).
	passes int
}

// page fills a page of at most n readable tags, in chunks, with one listing-form check per chunk (no audit event per entry,
// R-AUTHZ-016), and returns it with the tag a following page starts after: the last tag listed, never a tag the caller may not read.
// next is empty when nothing follows. It stops at the scan budget of the local class, so a long run of hidden tags is not walked.
func (s *tagSource) page(ctx context.Context, n int) (tags []string, next string, err error) {
	tags = []string{}
	scanned := 0
	for {
		if len(s.chunk) == 0 {
			if !s.cut {
				return tags, "", nil
			}
			if s.passes >= maxPassPages {
				// The page ends early with a Link: the client asks again, and each of its requests costs a bounded number of
				// upstream pages (Q-OCI-128).
				if len(tags) == 0 {
					return nil, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported,
						"More than the allowed number of consecutive tags of the image name are not readable for the caller; the list cannot go on without naming one of them.")
				}
				return tags, tags[len(tags)-1], nil
			}
			s.passes++
			more, ok, err := s.pass(ctx)
			if err != nil {
				return nil, "", err
			}
			if !ok || len(more) == 0 {
				return tags, "", nil // the upstream cannot be asked, or has no more
			}
			s.chunk = more
		}
		take := s.chunk[:min(len(s.chunk), max(n-len(tags), 64), MaxPage)]
		allowed, err := readableMask(ctx, s.rq, s.client, take)
		if err != nil {
			return nil, "", err
		}
		for i, t := range take {
			scanned++
			s.cursor = t
			if !allowed[i] {
				continue
			}
			tags = append(tags, t)
			if len(tags) == n {
				s.chunk = s.chunk[i+1:]
				if len(s.chunk) > 0 || s.cut {
					next = t
				}
				return tags, next, nil
			}
		}
		s.chunk = s.chunk[len(take):]
		if scanned >= tagsScanBudget {
			if len(tags) == 0 {
				return nil, "", errs.E(errs.Invalid, errs.CodeOCIUnsupported,
					"More than 10,000 consecutive tags of the image name are not readable for the caller; the list cannot go on without naming one of them.")
			}
			return tags, tags[len(tags)-1], nil
		}
	}
}

// maxPassPages bounds the pages of the upstream one request of a client may ask for. A page of tags the client asked for is filled
// from the stored document and, beyond it, from upstream pages; the hidden tags among them are walked past, and the registry's own page size
// (Docker Hub and GHCR cut a list at some hundred tags) decides how many requests that costs. Eight pages end a client's page early,
// with a Link, so one request costs eight upstream requests at most however many tags are hidden (Q-OCI-128).
const maxPassPages = 8

// pass asks the upstream for the page after the cursor (R-ARCH-013, kept for the metadata period). ok is false when it cannot be
// asked: offline, the circuit open, or a caller who may not fill the repository; the stored window then answers (R-OCI-024). An
// upstream that gives tags at or before the cursor is not followed back.
func (s *tagSource) pass(ctx context.Context) (page []string, ok bool, err error) {
	params := url.Values{"n": {strconv.Itoa(MaxPage)}}
	if s.cursor != "" {
		params.Set("last", s.cursor)
	}
	res, err := s.rq.Host.Remote().Query(ctx, s.rq, formats.Query{
		Path: formats.Path{Path: s.name + "/" + wordTags + "/" + wordList}, Params: params, TTL: metadataTTL(s.rq),
	})
	switch {
	case err == nil:
	case unreachable(err):
		return nil, false, nil
	case errs.KindOf(err) == errs.NotFound:
		return nil, true, nil
	default:
		return nil, false, err
	}
	parsed, err := parseTaglist(res.Body)
	if err != nil {
		return nil, false, err
	}
	page = parsed.Tags[sort.SearchStrings(parsed.Tags, s.cursor+"\x00"):]
	return page[:min(len(page), MaxPage)], true, nil
}

// referrersTag reports whether a tag has the form of the referrers tag schema: `sha256-` and the 64 hexadecimal digits of a digest.
func referrersTag(tag string) bool {
	hexPart, ok := strings.CutPrefix(tag, "sha256-")
	return ok && ValidDigest("sha256:"+hexPart)
}

// upstreamFailed reports whether the upstream was asked and did not answer usably: it could not be reached, cut the connection, timed out or
// answered 5xx. A referrers request that meets it is answered as one that cannot be had (Q-OCI-204): Docker with the containerd image store
// asks for the referrers of every image it pulls, and an error there would fail a pull of an image the repository holds completely.
func upstreamFailed(err error) bool {
	switch errs.KindOf(err) {
	case errs.UpstreamFailed, errs.UpstreamTimeout, errs.Unavailable:
		return true
	}
	return false
}

// unreachable reports whether an answer of the engine says the upstream cannot be asked now and the stored copy is to answer:
// the repository is offline (or the installation is), the caller may not fill it, or the circuit is open.
func unreachable(err error) bool {
	if errors.Is(err, formats.ErrOffline) {
		return true
	}
	e, ok := errs.As(err)
	return ok && e.Code == errs.CodeUpstreamAssumedOffline
}

// readableMask decides one listing-form check for the tags of a name.
func readableMask(ctx context.Context, rq *formats.Request, name string, tags []string) ([]bool, error) {
	return bothAllow(ctx, rq, name, tags, TagPath)
}

// bothAllow decides the read of one entry per element on the spelling the client typed and on the other one; an entry is held when both hold.
func bothAllow(ctx context.Context, rq *formats.Request, name string, elems []string, pathOf func(name, elem string) string) ([]bool, error) {
	paths := make([]string, len(elems))
	for i, e := range elems {
		paths[i] = pathOf(name, e)
	}
	out, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths)
	if err != nil {
		return nil, err
	}
	other := otherSpelling(rq, name)
	if other == "" {
		return out, nil
	}
	for i, e := range elems {
		paths[i] = pathOf(other, e)
	}
	second, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i] = out[i] && second[i]
	}
	return out, nil
}

// remoteReferrers is the referrers API of a remote repository (R-OCI-024): the upstream's own answer, passed through and kept for the
// metadata period, with the descriptors the caller may not read left out. An upstream that has no referrers API answers 404, and so
// does the registry, so that the client falls back to the tag schema through the same remote (Q-OCI-15).
func (f *Format) remoteReferrers(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	client := rq.Params.Get("name")
	dgst := rq.Params.Get("digest")
	if !ValidDigest(dgst) {
		return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest is not a sha256 digest.")
	}
	// The gate is the subject's manifest path, the item the request asks about (Q-OCI-140); each referrer is decided by its own path.
	deny := func() error {
		return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The upstream has no image of this name.")
	}
	pathOf := func(n string) string { return ManifestPath(n, dgst) }
	target := formats.Target{Package: client, Path: pathOf(client), Actions: []formats.Action{formats.ActionRead}}
	if err := gateRemote(ctx, rq, client, target, pathOf, deny); err != nil {
		return err
	}
	artifactType := rq.URL.Query().Get("artifactType")
	if len(artifactType) > maxArtifactTypeBytes {
		return errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter artifactType is longer than 255 bytes.")
	}
	kept, err := f.remoteReferrerList(ctx, rq, client, dgst, artifactType)
	if err != nil {
		return err
	}
	if artifactType != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	return jsonReply(w, http.StatusOK, mediaTypeImageIndex, struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType"`
		Manifests     []json.RawMessage `json:"manifests"`
	}{2, mediaTypeImageIndex, kept})
}

// remoteReferrerList is the descriptors of the upstream's referrers answer for the digest that the caller may read, filtered by artifactType, at
// most a page of the local class. The gate of the request is the caller's (the remote's own, or that of the virtual repository this is a member
// of); each descriptor is decided here, on its own path and on both spellings of the name.
func (f *Format) remoteReferrerList(ctx context.Context, rq *formats.Request, client, dgst, artifactType string) ([]json.RawMessage, error) {
	name := remoteName(rq, client)
	params := url.Values{}
	if artifactType != "" {
		params.Set("artifactType", artifactType)
	}
	res, err := rq.Host.Remote().Query(ctx, rq, formats.Query{
		Path: formats.Path{Path: name + "/" + wordReferrers + "/" + dgst}, Params: params, TTL: metadataTTL(rq),
	})
	if err != nil {
		// Not asked (offline, the circuit open, a caller who may not fill) is answered like an upstream without the API: 404, so that
		// the client falls back to the tag schema through the same remote, whose manifests are cached like every other (Q-OCI-128). An
		// empty index would say that nothing refers to the subject, which nobody has found out.
		if unreachable(err) || upstreamFailed(err) {
			return nil, errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The referrers of this digest are not stored and the upstream cannot be asked.")
		}
		return nil, missing(err, func() error {
			return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "The upstream serves no referrers for this digest.")
		})
	}
	var index struct {
		Manifests []json.RawMessage `json:"manifests"`
	}
	lim := untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: MaxReferrersBytes})
	if err := untrusted.UnmarshalJSON(res.Body, lim, &index); err != nil {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream's referrers answer is not an image index the registry reads.", errs.WithCause(err))
	}
	// At most a page of the local class (R-OCI-016): the descriptors beyond it are not decided or listed.
	index.Manifests = index.Manifests[:min(len(index.Manifests), MaxPage)]
	return f.readableReferrers(ctx, rq, client, artifactType, index.Manifests)
}

// readableReferrers keeps the descriptors of the upstream's answer that name a manifest the caller may read, and that match the
// artifactType filter: an upstream that ignores the filter is filtered here, so the answer is what OCI-Filters-Applied says.
func (f *Format) readableReferrers(ctx context.Context, rq *formats.Request, name, artifactType string, in []json.RawMessage) ([]json.RawMessage, error) {
	type desc struct {
		Digest       string `json:"digest"`
		ArtifactType string `json:"artifactType"`
	}
	keep := make([]json.RawMessage, 0, len(in))
	digests := make([]string, 0, len(in))
	for _, raw := range in {
		var d desc
		if err := json.Unmarshal(raw, &d); err != nil || !ValidDigest(d.Digest) {
			continue
		}
		if artifactType != "" && d.ArtifactType != artifactType {
			continue
		}
		keep = append(keep, raw)
		digests = append(digests, d.Digest)
	}
	out := []json.RawMessage{}
	if len(digests) == 0 {
		return out, nil
	}
	allowed, err := bothAllow(ctx, rq, name, digests, ManifestPath)
	if err != nil {
		return nil, err
	}
	for i := range keep {
		if allowed[i] {
			out = append(out, keep[i])
		}
	}
	return out, nil
}
