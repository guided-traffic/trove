package ansible

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/qname"
)

// The remote class's registration and its handlers (R-ANS-016 to R-ANS-018, R-ANS-020). The documents are rendered from the upstream
// documents the engine keeps (fresh, refreshed or stale); the stored bytes are never altered (R-ANS-017). What a response says
// about the archives and the roles is trove's own address, so nothing of the upstream's addresses reaches a client.
func init() {
	registerClass(formats.Remote, func(f *Format) { f.remote = remoteAdapter{f: f} })
	for route, h := range map[string]func(*Format) formats.HandlerFunc{
		routeRoot:         func(f *Format) formats.HandlerFunc { return f.remoteRoot },
		routeCollection:   func(f *Format) formats.HandlerFunc { return f.remoteCollection },
		routeVersions:     func(f *Format) formats.HandlerFunc { return f.remoteVersions },
		routeVersion:      func(f *Format) formats.HandlerFunc { return f.remoteVersion },
		routeArtifact:     func(f *Format) formats.HandlerFunc { return f.remoteArtifact },
		routeRolesFind:    func(f *Format) formats.HandlerFunc { return f.remoteRoleFind },
		routeRoleVersions: func(f *Format) formats.HandlerFunc { return f.remoteRoleVersions },
		routeRoleArchive:  func(f *Format) formats.HandlerFunc { return f.remoteRoleArchive },
	} {
		registerHandler(formats.Remote, route, h)
	}
}

// remoteRenderVersion is part of every validator of a rendered document: a release that changes the rendering raises it.
const remoteRenderVersion = 1

// cacheSuffix is the suffix of the key of the cache view of a remote repository (R-REPO-032).
const cacheSuffix = "~cache"

// servingName is the name a document is served under, in the form of an address: the addressed repository, its key followed by the
// cache-view suffix for the cache view.
func servingName(rq *formats.Request) qname.Repo {
	r := rq.Addressed()
	if rq.CacheView {
		r.Key += cacheSuffix
	}
	return r
}

// addrOf is the address documents are rendered for.
func remoteAddr(rq *formats.Request) galaxydoc.Addr {
	return galaxydoc.Addr{Repo: rq.Host.Links(rq).Registry(servingName(rq))}
}

// collectionOf reads and normalizes the namespace and name of a matched route; a name no collection can have is a missing resource.
func remoteCollectionOf(rq *formats.Request) (ns, name string, err error) {
	ns, name = strings.ToLower(rq.Params.Get(paramNamespace)), strings.ToLower(rq.Params.Get(paramName))
	if !collection.ValidPart(ns) || !collection.ValidPart(name) {
		return "", "", missing()
	}
	return ns, name, nil
}

func readTarget(pkg, ver, path string) formats.Target {
	return formats.Target{Package: pkg, Version: ver, Path: path, Actions: []formats.Action{formats.ActionRead}}
}

// storedDocument reads a document of the remote through the engine: fresh, refreshed or stale. The negative entry of a path the
// upstream does not have is the 404 of a missing resource.
func storedDocument(ctx context.Context, rq *formats.Request, path, variant string) (*formats.RemoteDocument, error) {
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: path}, variant)
	if err != nil {
		return nil, err
	}
	if doc.Status.Negative {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.", errs.WithHeader("Cache-Status", doc.Status.Header))
	}
	return doc, nil
}

// etag is the weak validator of a rendering: a hash of the inputs of the rendering, computed without rendering. The inputs are the
// render version, the repository (its id, the key it is served under and its configuration version), the base URL, the endpoint with
// its exposure fingerprint, and the parts: what the document is a function of (the digests of the stored documents, the paging).
// Every replica sends the same value for the same inputs.
func etag(rq *formats.Request, kind string, parts ...string) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field("ansible-remote")
	field(strconv.Itoa(remoteRenderVersion))
	field(rq.Repo.ID)
	field(servingName(rq).String())
	field(strconv.FormatInt(rq.Repo.Version, 10))
	field(rq.Host.Links(rq).Base())
	field(rq.Endpoint.Name)
	field(rq.Endpoint.ExposureFingerprint)
	field(kind)
	for _, p := range parts {
		field(p)
	}
	return `W/"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// matches reports whether an If-None-Match header holds the validator, by the weak comparison of RFC 9110 section 8.8.3.2. The
// wildcard never matches: a 200 costs bytes and never correctness.
func remoteMatches(header, tag string) bool {
	for _, c := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(c), "W/") == strings.TrimPrefix(tag, "W/") {
			return true
		}
	}
	return false
}

// reply writes a rendered document with its validator, or the 304 of a validator that matches. status is the Cache-Status of the
// engine, "" for none.
func remoteReply(rq *formats.Request, w http.ResponseWriter, tag, status string, render func() ([]byte, error)) error {
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "private, no-cache")
	if status != "" {
		h.Set("Cache-Status", status)
	}
	if remoteMatches(rq.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	body, err := render()
	if err != nil {
		return err
	}
	if int64(len(body)) > MaxDocumentBytes {
		return errs.E(errs.Internal, errs.CodeInternal, "The document is larger than the limit of documents held in memory.")
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
}

func hexSum(d [32]byte) string { return hex.EncodeToString(d[:]) }

// upstreamHasV1 reads the stored API root and reports whether the upstream serves the role API (R-ANS-003, R-ANS-016).
func upstreamHasV1(ctx context.Context, rq *formats.Request) (has bool, doc *formats.RemoteDocument, err error) {
	doc, err = storedDocument(ctx, rq, RootDocument, documentVariant)
	if err != nil {
		return false, nil, err
	}
	raw, err := doc.Bytes(ctx)
	if err != nil {
		return false, nil, err
	}
	d, err := parseDiscovery(raw)
	if err != nil {
		return false, nil, err
	}
	return d.v1 != "", doc, nil
}

// remoteRoot answers the discovery document of the repository URL (R-ANS-003): v1 is listed when the upstream lists it.
func (f *Format) remoteRoot(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if err := rq.Host.Authorize(ctx, rq, readTarget("", "", RootDocument)); err != nil {
		return err
	}
	v1, doc, err := upstreamHasV1(ctx, rq)
	if err != nil {
		return err
	}
	return remoteReply(rq, w, etag(rq, "root", strconv.FormatBool(v1)), doc.Status.Header, func() ([]byte, error) { return galaxydoc.RenderRoot(v1), nil })
}

// remoteCollection answers the collection document (R-ANS-004).
func (f *Format) remoteCollection(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := remoteCollectionOf(rq)
	if err != nil {
		return err
	}
	path := CollectionDocumentPath(ns, name)
	if err := rq.Host.Authorize(ctx, rq, readTarget(ns+"."+name, "", path)); err != nil {
		return err
	}
	doc, err := storedDocument(ctx, rq, path, documentVariant)
	if err != nil {
		return err
	}
	return remoteReply(rq, w, etag(rq, "collection", hexSum(doc.Digest)), doc.Status.Header, func() ([]byte, error) {
		raw, err := doc.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		s, err := parseUpstreamCollection(raw)
		if err != nil {
			return nil, err
		}
		s.Namespace, s.Name = ns, name
		return galaxydoc.RenderCollection(s, remoteAddr(rq)), nil
	})
}

// remoteVersions answers the version list (R-ANS-004, R-ANS-017, Q-ANS-12). The upstream's list is stored in pages of the size the
// upstream answers (100 at most), one document each; the page of the request is cut from the stored pages that cover its range, and
// the count and the links are trove's.
func (f *Format) remoteVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := remoteCollectionOf(rq)
	if err != nil {
		return err
	}
	path := VersionsDocumentPath(ns, name)
	if err := rq.Host.Authorize(ctx, rq, readTarget(ns+"."+name, "", path)); err != nil {
		return err
	}
	q := rq.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, offset = galaxydoc.PageLimit(limit), max(offset, 0)

	first, err := storedDocument(ctx, rq, path, offsetVariant(0))
	if err != nil {
		return err
	}
	firstRaw, err := first.Bytes(ctx)
	if err != nil {
		return err
	}
	head, err := parsePage(firstRaw)
	if err != nil {
		return err
	}
	// The size of a stored page is the number of entries the upstream answered with when it has more: an upstream that caps its
	// list below 100 is read at its own page size.
	size := upstreamListLimit
	if head.count > len(head.entries) && len(head.entries) > 0 {
		size = len(head.entries)
	}
	docs := []*formats.RemoteDocument{first}
	var starts []int
	if offset < head.count {
		last := min(offset+limit, head.count) - 1
		for start := offset / size * size; start <= last/size*size; start += size {
			starts = append(starts, start)
		}
	}
	pages := map[int]upstreamPage{0: head}
	for _, start := range starts {
		if start == 0 {
			continue
		}
		d, err := storedDocument(ctx, rq, path, offsetVariant(start))
		if err != nil {
			return err
		}
		raw, err := d.Bytes(ctx)
		if err != nil {
			return err
		}
		p, err := parsePage(raw)
		if err != nil {
			return err
		}
		docs = append(docs, d)
		pages[start] = p
	}
	digests := make([]string, 0, len(docs))
	for _, d := range docs {
		digests = append(digests, hexSum(d.Digest))
	}
	tag := etag(rq, "versions", strings.Join(digests, ","), strconv.Itoa(limit), strconv.Itoa(offset), strconv.Itoa(size))
	return remoteReply(rq, w, tag, first.Status.Header, func() ([]byte, error) {
		var window []galaxydoc.VersionEntry
		if len(starts) > 0 {
			var joined []galaxydoc.VersionEntry
			for _, start := range starts {
				joined = append(joined, pages[start].entries...)
			}
			from := offset - starts[0]
			if from < len(joined) {
				window = joined[from:min(from+limit, len(joined))]
			}
		}
		a := remoteAddr(rq)
		a.Namespace, a.Name = ns, name
		return galaxydoc.RenderVersionList(window, head.count, limit, offset, a), nil
	})
}

// remoteVersion answers the version document (R-ANS-004, R-ANS-017): the upstream's document with the repository's addresses and
// without the plugin listing. The digest and the size are the ones recorded first for the version, so that the document a client
// checks the archive against is the one its cached bytes were verified against (R-ANS-018).
func (f *Format) remoteVersion(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, err := remoteCollectionOf(rq)
	if err != nil {
		return err
	}
	ver := rq.Params.Get(paramVersion)
	if !version.Valid(ver) {
		return missing()
	}
	path := VersionDocumentPath(ns, name, ver)
	if err := rq.Host.Authorize(ctx, rq, readTarget(ns+"."+name, ver, path)); err != nil {
		return err
	}
	doc, err := storedDocument(ctx, rq, path, documentVariant)
	if err != nil {
		return err
	}
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	rec, found, err := st.Announced(ctx, rq.RepoRef().ID, ns+"."+name, ver)
	if err != nil {
		return err
	}
	recorded := ""
	if found {
		recorded = hexSum(rec.SHA256)
	}
	return remoteReply(rq, w, etag(rq, "version", hexSum(doc.Digest), recorded), doc.Status.Header, func() ([]byte, error) {
		raw, err := doc.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		v, _, err := galaxydoc.ParseUpstreamVersion(raw)
		if err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream sent a version document that trove cannot use: "+err.Error()+".", errs.WithCause(err))
		}
		v.Namespace, v.Name, v.Filename = ns, name, collection.FileName(ns, name, ver)
		if found {
			v.SHA256, v.Size = rec.SHA256, rec.Size
		}
		return galaxydoc.RenderVersion(v, remoteAddr(rq)), nil
	})
}

// remoteArtifact serves the archive of a version: the cached item, or one fetch under the collapsing protocol, verified against the
// digest the version document announced (R-ANS-018).
func (f *Format) remoteArtifact(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	ns, name, ver, ok := collection.ParseFileName(rq.Params.Get(paramFile))
	if !ok || !collection.ValidPart(strings.ToLower(ns)) || !collection.ValidPart(strings.ToLower(name)) || !version.Valid(ver) {
		return missing()
	}
	ns, name = strings.ToLower(ns), strings.ToLower(name)
	path := ArtifactPath(ns, name, ver)
	if err := rq.Host.Authorize(ctx, rq, readTarget(ns+"."+name, ver, path)); err != nil {
		return err
	}
	return rq.Host.Remote().Artifact(ctx, rq, formats.Path{Path: path}, formats.Digests{}, w)
}

// remoteRoleFind answers the lookup of a role by owner and name (R-ANS-020): only these two parameters pass to the upstream.
func (f *Format) remoteRoleFind(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	q := rq.URL.Query()
	owner, name := q.Get("owner__username"), q.Get("name")
	if !roleWordOK(owner) || !roleWordOK(name) {
		return errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, "The lookup needs owner__username and name, each 1 to 64 characters of letters, digits, '.', '_' and '-'.")
	}
	if err := rq.Host.Authorize(ctx, rq, readTarget("", "", RoleLookupDocument)); err != nil {
		return err
	}
	if err := requireV1(ctx, rq); err != nil {
		return err
	}
	doc, err := storedDocument(ctx, rq, RoleLookupDocument, lookupVariant(owner, name))
	if err != nil {
		return err
	}
	return remoteReply(rq, w, etag(rq, "role-lookup", hexSum(doc.Digest), owner, name), doc.Status.Header, func() ([]byte, error) {
		raw, err := doc.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		return renderRoleLookup(raw, rq.Host.Links(rq).Registry(servingName(rq)), owner, name)
	})
}

// requireV1 answers 404 in the v1 shape for an upstream that serves collections only (R-ANS-020).
func requireV1(ctx context.Context, rq *formats.Request) error {
	has, _, err := upstreamHasV1(ctx, rq)
	if err != nil {
		return err
	}
	if !has {
		return missing()
	}
	return nil
}

// roleID reads the numeric id of a matched role route.
func roleID(rq *formats.Request) (string, error) {
	id := rq.Params.Get(paramID)
	if !digits(id, maxRoleIDDigits) {
		return "", errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, "A role id is 1 to 12 digits.")
	}
	return id, nil
}

// remoteRoleVersions answers a page of the versions of a role (R-ANS-020): a version whose download_url is not an https URL on an
// extra host is left out, and every other one points to the role archive route of the repository.
func (f *Format) remoteRoleVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	id, err := roleID(rq)
	if err != nil {
		return err
	}
	page := 1
	if s := rq.URL.Query().Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxRolePage {
			return errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, "page is a number from 1 to "+strconv.Itoa(maxRolePage)+".")
		}
		page = n
	}
	path := RoleVersionsDocumentPath(id)
	if err := rq.Host.Authorize(ctx, rq, readTarget(RolePackage(id), "", path)); err != nil {
		return err
	}
	if err := requireV1(ctx, rq); err != nil {
		return err
	}
	doc, err := storedDocument(ctx, rq, path, pageVariant(page))
	if err != nil {
		return err
	}
	settings, _ := rq.Repo.Remote()
	hosts := make([]string, len(settings.ExtraHosts))
	for i, h := range settings.ExtraHosts {
		hosts[i] = h.Pattern
	}
	return remoteReply(rq, w, etag(rq, "role-versions", hexSum(doc.Digest), id, strconv.Itoa(page)), doc.Status.Header, func() ([]byte, error) {
		raw, err := doc.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		return renderRoleVersions(raw, id, rq.Host.Links(rq).Registry(servingName(rq)), func(u string) bool { return roleHostAllowed(hosts, u) }, "")
	})
}

// remoteRoleArchive serves the archive of a role version: the cached item, or one fetch from the address the version list recorded for
// exactly this path, under the collapsing protocol (R-ANS-020). The upstream announces no digest for it.
func (f *Format) remoteRoleArchive(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	id, err := roleID(rq)
	if err != nil {
		return err
	}
	ref, ok := strings.CutSuffix(rq.Params.Get(paramFile), collection.ArchiveSuffix)
	if !ok || !roleRefOK(ref) {
		return errs.E(errs.Invalid, errs.CodeAnsibleInvalidRequest, "A role archive is named <version>.tar.gz, the version of 1 to 128 characters of letters, digits, '.', '_', '+' and '-'.")
	}
	path := RoleArchivePath(id, ref)
	if err := rq.Host.Authorize(ctx, rq, readTarget(RolePackage(id), "", path)); err != nil {
		return err
	}
	return rq.Host.Remote().Artifact(ctx, rq, formats.Path{Path: path}, formats.Digests{}, w)
}

// pageOffsetOf is the offset of a stored version-list page, read from its links: a page without a previous link starts at 0, the last
// page ends at the count, and a page in between ends where the next link starts.
func pageOffsetOf(doc []byte, entries, count int) int {
	var l struct {
		Links struct {
			Previous *string `json:"previous"`
			Next     *string `json:"next"`
		} `json:"links"`
	}
	if json.Unmarshal(doc, &l) != nil || l.Links.Previous == nil {
		return 0
	}
	if l.Links.Next == nil {
		return max(count-entries, 0)
	}
	if u, err := url.Parse(*l.Links.Next); err == nil {
		if n, err := strconv.Atoi(u.Query().Get("offset")); err == nil {
			return max(n-entries, 0)
		}
	}
	return 0
}
