package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The remote class (R-OCI-021 to R-OCI-025). The remote engine owns the request path, the freshness, the collapsing of concurrent
// fetches, the negative cache and the circuit (R-REPO-025 to R-REPO-031); this file is what the format adds to it: where the
// registry's resources are (the namespace prefix of the URL), which request fetches a document or an artifact, what a fetched
// document must be before it is stored, and what is recorded of it in the transaction that stores it. The token handshake is
// remote_auth.go, the revalidation by HEAD remote_revalidate.go, the answers a client sees remote_handlers.go.

// DockerHubHost is the upstream host whose one-component names are the names below library/ (R-OCI-025).
const DockerHubHost = "registry-1.docker.io"

// taglistLimit is the n the format asks for when it fetches an upstream's tags list document (R-OCI-024).
const taglistLimit = 10000

// MaxTaglistEntries bounds the tags of a tags list document of an upstream (R-OCI-020).
const MaxTaglistEntries = 100_000

// truncatedMember is the member the format adds to a stored tags list document whose upstream answer was cut (it had a Link): the
// stored document is the upstream's plus this fact, which a client's window beyond the stored tags needs (R-OCI-024). It cannot be
// confused with an upstream member: the grammar of a tags list names only name and tags.
const truncatedMember = "x-trove-truncated"

// The manifest media types a fetch asks for (R-OCI-022): all four, so the upstream returns its index and not a platform manifest it
// picked.
var acceptManifests = strings.Join([]string{
	manifest.MediaTypeImageIndex, manifest.MediaTypeDockerList, manifest.MediaTypeImageManifest, manifest.MediaTypeDockerManifest,
}, ", ")

// Remote returns the format's remote adapter. It is a stable value, because Register records the instance it returns.
func (f *Format) Remote() formats.RemoteAdapter { return remoteAdapter{f: f} }

// remoteAdapter embeds the defaults of the contract: no artifact gate beyond the digest the path names, and the pass-through
// queries are the tags list window and the referrers (UpstreamQuery).
type remoteAdapter struct {
	formats.RemoteBase
	f *Format
}

var (
	_ formats.RemoteAdapter        = remoteAdapter{}
	_ formats.FillInspector        = remoteAdapter{}
	_ formats.Authenticator        = remoteAdapter{}
	_ formats.Revalidator          = remoteAdapter{}
	_ formats.StoodChallengeReader = remoteAdapter{}
)

// TracksVersionRemoval is false: a registry has no removal marks, so the generic gone-package rule applies to a name the upstream
// no longer has (R-REPO-029).
func (remoteAdapter) TracksVersionRemoval() bool { return false }

// target is a canonical path of the layout taken apart: the image name, the part of the layout and what follows it.
type target struct {
	name, kind, tail string
}

// parseTarget resolves a canonical path of a remote repository: a tag, a manifest, a blob or the tags list of an image name. A path
// outside the layout is the 404 of a missing resource.
func parseTarget(p formats.Path) (target, error) {
	missing := errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	name, kind, tail, ok := splitLayout(p.Path)
	if !ok || !ValidName(name) {
		return target{}, missing
	}
	switch kind {
	case dirTags:
		if !ValidTag(tail) {
			return target{}, missing
		}
	case dirManifests, dirBlobs:
		if !ValidDigest(tail) {
			return target{}, missing
		}
	case fileTaglist:
	default:
		return target{}, missing
	}
	return target{name: name, kind: kind, tail: tail}, nil
}

func (t target) isManifest() bool { return t.kind == dirTags || t.kind == dirManifests }

// namePrefix is the namespace prefix of the upstream URL (R-OCI-021): its path without the slashes at both ends.
func namePrefix(up *url.URL) string { return strings.Trim(up.Path, "/") }

// upstreamName is the image name as the upstream knows it: below the namespace prefix.
func upstreamName(up *url.URL, name string) string {
	if p := namePrefix(up); p != "" {
		return p + "/" + name
	}
	return name
}

// registryURL is the address of a resource of the distribution API of the upstream: its origin, "/v2/", the prefixed image name and
// the part after it. The name and the tag or digest are checked by the grammars of R-OCI-002, so nothing here needs escaping.
func registryURL(up *url.URL, name, rest string) *url.URL {
	u := *up
	u.User, u.Fragment, u.RawFragment, u.RawQuery = nil, "", "", ""
	u.Path = "/v2/" + upstreamName(up, name) + rest
	u.RawPath = ""
	return &u
}

// UpstreamRequest maps a canonical path to the upstream request (R-OCI-021, R-OCI-022): the address is derived from the repository's
// URL, the image name and the tag or digest the path names, never from a host a client named.
func (remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	var u *url.URL
	switch t.kind {
	case dirTags, dirManifests:
		u = registryURL(rc.Upstream, t.name, "/manifests/"+t.tail)
	case dirBlobs:
		u = registryURL(rc.Upstream, t.name, "/blobs/"+t.tail)
	default:
		u = registryURL(rc.Upstream, t.name, "/tags/list")
		u.RawQuery = "n=" + strconv.Itoa(taglistLimit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	switch {
	case t.isManifest():
		req.Header.Set("Accept", acceptManifests)
	case t.kind == fileTaglist:
		req.Header.Set("Accept", "application/json")
	}
	return req, nil
}

// isQueryPath reports whether a path is the path of a pass-through query: "<name>/referrers/<digest>" or "<name>/tags/list".
func isQueryPath(p string) bool {
	segs := strings.Split(p, "/")
	n := len(segs)
	if n < 3 || !ValidName(strings.Join(segs[:n-2], "/")) {
		return false
	}
	return (segs[n-2] == wordReferrers && ValidDigest(segs[n-1])) || (segs[n-2] == wordTags && segs[n-1] == wordList)
}

// UpstreamQuery maps a pass-through query to the upstream request (R-OCI-024): the referrers of a digest, and a window of the tags
// list beyond the stored document. The query's path is "<name>/referrers/<digest>" or "<name>/tags/list".
func (remoteAdapter) UpstreamQuery(ctx context.Context, rc formats.RemoteContext, q formats.Query) (*http.Request, error) {
	bad := errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	segs := strings.Split(q.Path.Path, "/")
	n := len(segs)
	if n < 3 {
		return nil, bad
	}
	var (
		name = strings.Join(segs[:n-2], "/")
		u    *url.URL
		acc  string
	)
	if !ValidName(name) {
		return nil, bad
	}
	switch {
	case segs[n-2] == wordReferrers && ValidDigest(segs[n-1]):
		u = registryURL(rc.Upstream, name, "/referrers/"+segs[n-1])
		if at := q.Params.Get("artifactType"); at != "" {
			u.RawQuery = url.Values{"artifactType": {at}}.Encode()
		}
		acc = manifest.MediaTypeImageIndex
	case segs[n-2] == wordTags && segs[n-1] == wordList:
		u = registryURL(rc.Upstream, name, "/tags/list")
		u.RawQuery = q.Params.Encode()
		acc = "application/json"
	default:
		return nil, bad
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	req.Header.Set("Accept", acc)
	return req, nil
}

// schema1Types are the media types of Docker's schema 1, which the registry does not serve (R-OCI-022).
func schema1Type(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/vnd.docker.distribution.manifest.v1+json" || mt == "application/vnd.docker.distribution.manifest.v1+prettyjws"
}

// AcceptResponse checks what a 2xx answer says before a byte is stored (R-OCI-020, R-OCI-022): a manifest of Docker's schema 1 is
// refused by its type, a tag fetched with a Docker-Content-Digest is read through a reader that refuses the body whose SHA-256 is
// not that digest (the digest of a manifest fetched by digest is the engine's check, BeforeArtifact), and a tags list the upstream
// cut is marked as cut.
func (remoteAdapter) AcceptResponse(p formats.Path, resp *http.Response) error {
	t, err := parseTarget(p)
	if err != nil {
		if isQueryPath(p.Path) {
			return nil // a pass-through answer is read and filtered by its handler
		}
		return err
	}
	switch {
	case t.isManifest():
		if schema1Type(resp.Header.Get("Content-Type")) {
			return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
				"The upstream served a manifest of Docker schema 1, which the registry does not serve (MANIFEST_INVALID).")
		}
		// A manifest is read within 4 MiB before it is spooled, not after (R-OCI-020).
		if resp.ContentLength > MaxDocumentBytes {
			return errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream manifest is larger than the limit of documents held in memory.")
		}
		resp.Body = &boundedBody{rc: resp.Body, left: MaxDocumentBytes}
		if t.kind == dirTags {
			if want, ok := announcedDigest(resp.Header); ok {
				resp.Body = &digestBody{rc: resp.Body, h: sha256.New(), want: want}
			}
		}
	case t.kind == fileTaglist:
		if hasNextLink(resp.Header) {
			// The wrapper adds a member, so the body is longer than the upstream's Content-Length: the length is no longer known
			// (Q-OCI-126). A body that keeps the announced length would fail the spool's check with a 400.
			resp.Body = &truncatedBody{rc: resp.Body}
			resp.ContentLength = -1
			resp.Header.Del("Content-Length")
		}
	}
	return nil
}

// announcedDigest is the SHA-256 the upstream announced for a manifest in Docker-Content-Digest; ok is false for a header that is
// absent or names another algorithm.
func announcedDigest(h http.Header) (want [32]byte, ok bool) {
	v := strings.TrimSpace(h.Get("Docker-Content-Digest"))
	if !ValidDigest(v) {
		return want, false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(v, "sha256:"))
	if err != nil || len(b) != len(want) {
		return want, false
	}
	copy(want[:], b)
	return want, true
}

// boundedBody passes a body through up to a limit and fails with an upstream error beyond it, so a manifest of an upstream that sends no
// length is refused as a bad upstream document (502) and not read into the spool to the artifact limit.
type boundedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream manifest is larger than the limit of documents held in memory.")
	}
	return n, err
}

func (b *boundedBody) Close() error { return b.rc.Close() }

// digestBody passes a manifest through and fails at its end when the SHA-256 of what passed is not the digest the upstream announced.
type digestBody struct {
	rc   io.ReadCloser
	h    hash.Hash
	want [32]byte
	done bool
}

// Read hashes what passes. The check is made when the body ends, so it relies on the consumer reading to the end: the spool reads a
// body of a known length through a reader that stops one byte past it and so sees the end (blob/spool.go), and a body of an unknown
// length until it ends. A consumer that stops before the end never gets the verdict; digestBody must not be used for one
// (TestR_OCI_020_TheDigestOfATagIsCheckedWhicheverWayTheBodyIsRead).
func (d *digestBody) Read(p []byte) (int, error) {
	n, err := d.rc.Read(p)
	d.h.Write(p[:n])
	if err == io.EOF && !d.done {
		d.done = true
		if !bytes.Equal(d.h.Sum(nil), d.want[:]) {
			return n, errs.E(errs.UpstreamFailed, errs.CodeUpstreamIntegrityMismatch,
				"The manifest the upstream served does not match the Docker-Content-Digest it announced; it was not stored.")
		}
	}
	return n, err
}

func (d *digestBody) Close() error { return d.rc.Close() }

// hasNextLink reports whether a Link header announces a further page.
func hasNextLink(h http.Header) bool {
	for _, v := range h.Values("Link") {
		if strings.Contains(strings.ToLower(v), `rel="next"`) || strings.Contains(strings.ToLower(v), "rel=next") {
			return true
		}
	}
	return false
}

// truncatedBody reads a tags list document and adds the member that says the upstream cut it. A document it cannot extend (larger
// than the bound, or no JSON object) passes unchanged: PrepareFill refuses it.
type truncatedBody struct {
	rc io.ReadCloser
	r  io.Reader
}

func (t *truncatedBody) Read(p []byte) (int, error) {
	if t.r == nil {
		buf, err := io.ReadAll(io.LimitReader(t.rc, MaxDocumentBytes+1))
		if err != nil {
			return 0, err
		}
		t.r = bytes.NewReader(markTruncated(buf))
		if len(buf) > MaxDocumentBytes {
			t.r = io.MultiReader(bytes.NewReader(buf), t.rc)
		}
	}
	return t.r.Read(p)
}

func (t *truncatedBody) Close() error { return t.rc.Close() }

// markTruncated adds `"x-trove-truncated":true` as the last member of a JSON object.
func markTruncated(doc []byte) []byte {
	end := bytes.LastIndexByte(doc, '}')
	if end < 0 || len(bytes.TrimSpace(doc[end+1:])) != 0 {
		return doc
	}
	head := bytes.TrimRight(doc[:end], " \t\r\n")
	sep := ","
	if len(head) > 0 && head[len(head)-1] == '{' {
		sep = ""
	}
	out := make([]byte, 0, len(doc)+32)
	out = append(out, head...)
	out = append(out, sep+`"`+truncatedMember+`":true}`...)
	return out
}

// StoodMissing reads a 401 that stood after the handshake (Q-OCI-127): a Bearer challenge with error="insufficient_scope" is the answer
// of a registry (Docker Hub among them) to a valid token for a repository that does not exist or is private. It is the upstream's "no
// such image" and not a refusal of the repository's credential.
func (remoteAdapter) StoodMissing(_ formats.Path, header http.Header) bool {
	for _, v := range header.Values("Www-Authenticate") {
		for _, c := range parseChallenges(v) {
			if c.scheme == "bearer" && c.params["error"] == "insufficient_scope" {
				return true
			}
		}
	}
	return false
}

// ExpectsType declares the media types of the registry's resources, so that the engine does not block them even when the block list
// grows: a manifest is one of the four manifest types, a blob an octet stream (a CDN's answer) or a layer's own type, a tags list
// JSON.
func (remoteAdapter) ExpectsType(p formats.Path, mediaType string) bool {
	t, err := parseTarget(p)
	if err != nil {
		return false
	}
	mt := strings.ToLower(mediaType)
	switch {
	case t.isManifest():
		switch mt {
		case manifest.MediaTypeImageManifest, manifest.MediaTypeImageIndex, manifest.MediaTypeDockerManifest, manifest.MediaTypeDockerList:
			return true
		}
		return false
	case t.kind == dirBlobs:
		return !strings.HasPrefix(mt, "text/") && mt != "application/xhtml+xml"
	}
	return mt == "application/json"
}

// BeforeArtifact allows every artifact and announces the digest the path names: a manifest by digest and a blob are addressed by
// their SHA-256, which the engine verifies a download against (R-OCI-020, R-OCI-023).
func (remoteAdapter) BeforeArtifact(_ context.Context, _ formats.Host, _ *formats.Request, p formats.Path) (formats.Digests, error) {
	t, err := parseTarget(p)
	if err != nil {
		return formats.Digests{}, err
	}
	if t.kind != dirManifests && t.kind != dirBlobs {
		return formats.Digests{}, nil
	}
	sum, err := hex.DecodeString(strings.TrimPrefix(t.tail, "sha256:"))
	if err != nil {
		return formats.Digests{}, errs.E(errs.Internal, errs.CodeInternal, "A digest that passed its grammar does not decode.")
	}
	return formats.Digests{SHA256: sum}, nil
}

// fillManifest is the parsed manifest a fill carries to the rows of R-OCI-007: from PrepareFill of a tag to OnFill, from InspectFill
// of a manifest fetched by digest to OnCommit.
type fillManifest struct {
	name string
	m    *manifest.Manifest
}

// fetchedMediaType is the media type a fetched manifest is read as. Neither PrepareFill nor InspectFill is told the Content-Type of
// the answer, so the document's own mediaType member decides, and a document without one is an index when it has manifests and an
// image manifest otherwise (the OCI image specification makes the member optional).
func fetchedMediaType(data []byte) string {
	var doc struct {
		MediaType string          `json:"mediaType"`
		Manifests json.RawMessage `json:"manifests"`
	}
	if untrusted.UnmarshalJSON(data, untrusted.Limits{}, &doc) != nil {
		return ""
	}
	switch {
	case doc.MediaType != "":
		return doc.MediaType
	case len(doc.Manifests) > 0:
		return manifest.MediaTypeImageIndex
	}
	return manifest.MediaTypeImageManifest
}

// schemaOne reports whether a document declares schemaVersion 1.
func schemaOne(data []byte) bool {
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	return untrusted.UnmarshalJSON(data, untrusted.Limits{}, &doc) == nil && doc.SchemaVersion == 1
}

// readFetched reads a spooled or fetched manifest within the limits of R-OCI-020. Anything the parser refuses is a bad upstream
// document: 502 with MANIFEST_INVALID in the detail (R-OCI-022), and nothing is stored.
func (f *Format) readFetched(name string, data []byte) (*fillManifest, error) {
	if schemaOne(data) {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed,
			"The upstream served a manifest of Docker schema 1, which the registry does not serve (MANIFEST_INVALID).")
	}
	m, err := manifest.Parse(data, fetchedMediaType(data), f.parseLimits())
	if err != nil {
		detail := "The upstream served a manifest the registry does not accept (MANIFEST_INVALID)"
		if e, ok := errs.As(err); ok && e.Detail != "" {
			detail += ": " + e.Detail
		}
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, detail+".", errs.WithCause(err))
	}
	return &fillManifest{name: name, m: m}, nil
}

func readAll(body io.ReaderAt, size int64) ([]byte, error) {
	if size > MaxDocumentBytes {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream document is larger than the limit of documents held in memory.")
	}
	doc, err := io.ReadAll(io.NewSectionReader(body, 0, size))
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The fetched document cannot be read.", errs.WithCause(err))
	}
	return doc, nil
}

// PrepareFill checks a fetched metadata document before anything is stored, outside every transaction (R-ARCH-019): a tag's manifest
// is parsed within the limits of R-OCI-020 and entered under its digest as an alias (R-OCI-022), a tags list is read within its
// bounds (R-OCI-020). A document that is not what its path promises is a 502 and the previous entry stays.
func (a remoteAdapter) PrepareFill(_ context.Context, _ formats.RemoteContext, p formats.Path, _ string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	t, err := parseTarget(p)
	if err != nil {
		return formats.Prepared{}, err
	}
	doc, err := readAll(body, size)
	if err != nil {
		return formats.Prepared{}, err
	}
	switch t.kind {
	case dirTags:
		fm, err := a.f.readFetched(t.name, doc)
		if err != nil {
			return formats.Prepared{}, err
		}
		return formats.Prepared{Aliases: []formats.Path{{Path: ManifestPath(t.name, fm.m.Digest)}}, Data: fm}, nil
	case fileTaglist:
		if _, err := parseTaglist(doc); err != nil {
			return formats.Prepared{}, err
		}
		return formats.Prepared{}, nil
	}
	return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The OCI remote fills tag documents and tags lists as metadata.")
}

// OnFill runs in the transaction that upserts the document's cache entry. A tag's manifest is recorded as the rows of R-OCI-007, the
// way a push records it, unless the stored document was fetched later (the entry then stays and the rows follow it, R-REPO-026); the
// alias item the engine enters afterwards ties the rows to the cache (Q-OCI-34). The tags list owns no row.
func (remoteAdapter) OnFill(ctx context.Context, tx formats.Tx, ev formats.FillEvent) (formats.FillDecision, error) {
	fm, ok := ev.Prepared.Data.(*fillManifest)
	if !ok {
		return formats.FillByFetchTime, nil
	}
	if !ev.StoredAt.IsZero() && ev.StoredAt.After(ev.FetchedAt) {
		return formats.FillByFetchTime, nil
	}
	return formats.FillByFetchTime, recordFilled(ctx, tx, ev.Packages, ev.Repo, ev.Data, fm)
}

// recordFilled writes the manifest row and the references of a manifest a remote fetched, and raises the revision of the image
// name's package: a remote fill is a rev_seq trigger (R-ARCH-012).
func recordFilled(ctx context.Context, tx formats.Tx, pw formats.PackageWriter, repo formats.RepoRef, data any, fm *fillManifest) error {
	st, err := portOf(data)
	if err != nil {
		return err
	}
	pkg, _, err := pw.EnsureLocked(ctx, repo, fm.name, fm.name)
	if err != nil {
		return err
	}
	if err := st.PutManifest(ctx, tx, manifestInput(repo.ID, fm.name, fm.m)); err != nil {
		return err
	}
	_, _, err = pw.BumpRevision(ctx, pkg)
	return err
}

// Rewrite renders a stored upstream document for one response: a manifest is served as it is stored (the digest the client verifies
// is that of the stored bytes, Q-OCI-8); a tags list is rendered with the name as the client of this host spells it, never the name
// the upstream gave (its namespace prefix, its own host), and without the members of the format's own.
func (remoteAdapter) Rewrite(_ context.Context, _ formats.RemoteContext, p formats.Path, doc []byte, links formats.LinkBuilder) ([]byte, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	if t.kind != fileTaglist {
		return doc, nil
	}
	stored, err := parseTaglist(doc)
	if err != nil {
		return nil, err
	}
	if stored.Tags == nil {
		stored.Tags = []string{}
	}
	out, err := json.Marshal(tagsDoc{Name: links.ClientName(t.name), Tags: stored.Tags})
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The tags list cannot be encoded.", errs.WithCause(err))
	}
	return out, nil
}

// InspectsFill is true for a manifest fetched by digest: it is read, and its rows recorded, before it is stored (R-OCI-020). A blob
// is not read (layers are never unpacked).
func (remoteAdapter) InspectsFill(p formats.Path) bool {
	t, err := parseTarget(p)
	return err == nil && t.kind == dirManifests
}

// InspectFill reads a manifest fetched by digest. Its result reaches OnCommit, which records the rows (remoteCommit).
func (a remoteAdapter) InspectFill(_ context.Context, _ formats.RemoteContext, p formats.Path, sp formats.Spooled) (*formats.Inspection, error) {
	t, err := parseTarget(p)
	if err != nil {
		return nil, err
	}
	r, err := sp.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := untrusted.ReadAll(r, MaxDocumentBytes+1)
	if err != nil {
		if untrusted.IsTooLarge(err) {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream manifest is larger than the limit of documents held in memory.")
		}
		return nil, err
	}
	fm, err := a.f.readFetched(t.name, data)
	if err != nil {
		return nil, err
	}
	return &formats.Inspection{Package: t.name, Payload: fm}, nil
}

// remoteCommit records the rows of a manifest a remote fetched by digest, in the transaction that wrote its item. The alias item of
// a tag fill carries no inspection: its rows are OnFill's. A blob writes none.
func (f *Format) remoteCommit(ctx context.Context, tx formats.Tx, c formats.CommitEvent) error {
	if c.Inspection == nil {
		return nil
	}
	fm, ok := c.Inspection.Payload.(*fillManifest)
	if !ok {
		return nil
	}
	return recordFilled(ctx, tx, c.Packages, c.Repo, c.Data, fm)
}
