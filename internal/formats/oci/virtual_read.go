package oci

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/manifest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// What a client sees of a virtual repository when it reads (R-OCI-027). A request addressed to a virtual repository is decided on the
// virtual repository first (the grants, the endpoint's exposure, the repository's patterns), then planned by the virtual engine for the
// image name as the client typed it: the plan holds the members that the repository's patterns, the pin rules and claims of R-REPO-023
// and the caller's right to read admit, in member order, each with the right to fill its cache. The handlers below walk that plan and call
// the handlers of a direct request on a request derived for the member (formats.Request.Member), so that a member answers exactly as it
// does when addressed, and every decision about it still names the virtual repository as the addressed one (Q-EP-11, Q-AUTHZ-163).

// resolvedFromHeader names the member that answered (R-REPO-019); it is never sent on an endpoint with exposure rules, where members are
// not exposed (R-EP-018).
const resolvedFromHeader = "Trove-Resolved-From"

// maxLinkDepth bounds the nesting of image indexes the link step of a push follows (an index, its manifests, their blobs).
const maxLinkDepth = 4

// vmember is a member of a plan with the request derived for it.
type vmember struct {
	pm formats.PlannedMember
	rq *formats.Request
}

func (m vmember) key() string { return m.pm.Member.Name.Key }

// members plans the request for the path, as the client typed the name, and returns the members that take part: local members first, in
// member order, then remote members in member order (R-OCI-027). A path the repository's patterns or the claims keep from the repository
// has no member.
func (f *Format) members(ctx context.Context, rq *formats.Request, path string) (locals, remotes []vmember, err error) {
	p := formats.Path{Path: path}
	plan, err := rq.Host.Resolver().Plan(ctx, rq, p, f.Classify(p))
	if err != nil {
		return nil, nil, err
	}
	if !plan.Admitted {
		return nil, nil, nil
	}
	all := rq.Repo.Members()
	for _, pm := range plan.Members {
		for i := range all {
			if all[i].Name != pm.Member.Name {
				continue
			}
			mrq, err := rq.Member(&all[i])
			if err != nil {
				return nil, nil, err
			}
			if pm.Tier == formats.TierLocal {
				locals = append(locals, vmember{pm, mrq})
			} else {
				remotes = append(remotes, vmember{pm, mrq})
			}
			break
		}
	}
	return locals, remotes, nil
}

// resolvedWriter puts Trove-Resolved-From on the answer of the member that serves it, when the status line goes out, so that a member
// that answers with an error leaves no header behind. wrote says the answer has started: a failure after that ends the request.
type resolvedWriter struct {
	http.ResponseWriter
	from  string
	wrote bool
}

func (w *resolvedWriter) start() {
	if w.wrote {
		return
	}
	w.wrote = true
	if w.from != "" {
		w.Header().Set(resolvedFromHeader, w.from)
	}
}

func (w *resolvedWriter) WriteHeader(code int) {
	w.start()
	w.ResponseWriter.WriteHeader(code)
}

func (w *resolvedWriter) Write(b []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(b)
}

func (w *resolvedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// isMemberMiss reports whether an error of a member says "this member does not have it" for the caller: the item is not there, the caller
// may not read it there, or may not bring it into the cache of a remote member (R-AUTHZ-008, which a virtual repository counts as a miss).
func isMemberMiss(err error) bool {
	e, ok := errs.As(err)
	return ok && (e.Kind == errs.NotFound || e.Kind == errs.Forbidden)
}

// isMemberFailure reports whether an error says the member could not be asked or did not answer, or that its stored object is lost: the
// walk goes on, and the failure is the answer when no member serves the item, because "not found" would be a false statement about an
// item that may exist (R-REPO-019 step 7).
func isMemberFailure(err error) bool {
	if errors.Is(err, formats.ErrOffline) {
		return true
	}
	e, ok := errs.As(err)
	if !ok {
		return false
	}
	switch e.Kind {
	case errs.UpstreamFailed, errs.UpstreamTimeout, errs.Unavailable:
		return true
	}
	return e.Code == errs.CodeContentUnavailable
}

// step is one attempt of a walk: serve the answer from a member.
type step struct {
	m   vmember
	run func(w http.ResponseWriter) error
}

// walk tries the steps in order and serves the first that answers. A miss goes on to the next step; a failure of a member goes on too and
// is the answer when nothing serves; any other error, or one after the answer has started, ends the request. unknown is the answer of a
// walk in which every member said "not found".
func walk(ctx context.Context, rq *formats.Request, w http.ResponseWriter, steps []step, unknown func() error) error {
	var failure error
	for _, s := range steps {
		rw := &resolvedWriter{ResponseWriter: w}
		if !rq.Endpoint.Rules {
			rw.from = s.m.key()
		}
		err := s.run(rw)
		switch {
		case err == nil:
			return nil
		case rw.wrote || ctx.Err() != nil:
			return err
		case isMemberMiss(err):
		case isMemberFailure(err):
			failure = cmp.Or(failure, err)
		default:
			return err
		}
	}
	if failure != nil {
		return asAddressed(rq, failure)
	}
	return unknown()
}

// asAddressed words a member's failure as the addressed repository's on an endpoint with exposure rules (R-EP-018): no answer there names a
// member. The kind, the code and Retry-After stay, so the client acts on it as before; the member's error stays the cause.
func asAddressed(rq *formats.Request, err error) error {
	if !rq.Endpoint.Rules {
		return err
	}
	e, ok := errs.As(err)
	if !ok {
		return err
	}
	return errs.E(e.Kind, e.Code, rq.Repo.Name.String()+" could not answer the request.", errs.WithCause(err), errs.WithRetryAfter(e.RetryAfter))
}

// virtualManifestGet is GET and HEAD of a manifest through a virtual repository.
func (f *Format) virtualManifestGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, ref := rq.Params.Get("name"), rq.Params.Get("reference")
	if strings.Contains(ref, ":") {
		if !ValidDigest(ref) {
			return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
		}
		return f.virtualByDigest(ctx, rq, w, name, ref, f.manifestLookup())
	}
	if !ValidTag(ref) {
		return errManifestUnknown()
	}
	path := TagPath(name, ref)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Version: ref, Path: path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	locals, remotes, err := f.members(ctx, rq, path)
	if err != nil {
		return err
	}
	// Local members first, then every remote member through the remote request path in member order: a remote member's stored tag is not
	// preferred over an earlier member's upstream (R-OCI-027).
	var steps []step
	for _, m := range append(locals, remotes...) {
		steps = append(steps, step{m, func(w http.ResponseWriter) error { return f.handleManifestGet(ctx, m.rq, w) }})
	}
	return walk(ctx, rq, w, steps, errManifestUnknown)
}

// virtualBlobGet is GET and HEAD of a blob through a virtual repository.
func (f *Format) virtualBlobGet(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name, digest := rq.Params.Get("name"), rq.Params.Get("digest")
	if !ValidDigest(digest) {
		return errDigestInvalid("A digest is sha256: followed by 64 lower-case hexadecimal digits.")
	}
	return f.virtualByDigest(ctx, rq, w, name, digest, f.blobLookup())
}

// lookup is what differs between a manifest and a blob when they are taken by digest.
type lookup struct {
	pathOf      func(name, digest string) string
	get         func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error
	contentType string
	unknown     func() error
}

func (f *Format) manifestLookup() lookup {
	return lookup{ManifestPath, f.handleManifestGet, "", errManifestUnknown}
}

func (f *Format) blobLookup() lookup {
	return lookup{BlobPath, f.handleBlobGet, blobContentType, errBlobUnknown}
}

// virtualByDigest serves a manifest or a blob by digest from the three tiers of R-REPO-019 step 6: the local members, in order; the caches
// of the remote members, without asking an upstream; then the upstreams of the remote members the caller may fill. The bytes are the
// same wherever they are found, so the first tier that has them serves them.
func (f *Format) virtualByDigest(ctx context.Context, rq *formats.Request, w http.ResponseWriter, name, digest string, l lookup) error {
	steps, err := f.digestSteps(ctx, rq, name, digest, l, true)
	if err != nil {
		return err
	}
	return walk(ctx, rq, w, steps, l.unknown)
}

// digestSteps are the steps of the three tiers; withFetch false leaves the third out (the link step reads, it never fetches).
func (f *Format) digestSteps(ctx context.Context, rq *formats.Request, name, digest string, l lookup, withFetch bool) ([]step, error) {
	path := l.pathOf(name, digest)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return nil, err
	}
	locals, remotes, err := f.members(ctx, rq, path)
	if err != nil {
		return nil, err
	}
	get := func(m vmember) func(http.ResponseWriter) error {
		return func(w http.ResponseWriter) error { return l.get(ctx, m.rq, w) }
	}
	var steps []step
	for _, m := range locals {
		steps = append(steps, step{m, get(m)})
	}
	for _, m := range remotes {
		steps = append(steps, step{m, func(w http.ResponseWriter) error { return f.serveCached(ctx, m.rq, w, name, digest, l) }})
	}
	if withFetch {
		for _, m := range remotes {
			if m.pm.MayFill {
				steps = append(steps, step{m, get(m)})
			}
		}
	}
	return steps, nil
}

// serveCached serves what the cache of a remote member holds at the path of the digest, without a request to the upstream. The caller's
// read of the item is decided on both spellings of the name (Q-OCI-129). An item that is not cached is a miss.
func (f *Format) serveCached(ctx context.Context, mrq *formats.Request, w http.ResponseWriter, client, digest string, l lookup) error {
	allowed, err := bothAllow(ctx, mrq, client, []string{digest}, l.pathOf)
	if err != nil {
		return err
	}
	if !allowed[0] {
		return l.unknown()
	}
	it, ok, err := mrq.Host.Remote().Cached(ctx, mrq, formats.Path{Path: l.pathOf(remoteName(mrq, client), digest)})
	if err != nil {
		return err
	}
	if !ok {
		return l.unknown()
	}
	if sum, ok := manifest.ParseDigest(digest); ok && l.contentType == "" {
		// A manifest about to be served from a member's cache is gated like one served from the member itself (R-VULN-015).
		if err := refuseBlockedImage(ctx, mrq, remoteName(mrq, client), sum); err != nil {
			return err
		}
	}
	vw := validatorWriter{ResponseWriter: w, digest: digest, contentType: l.contentType}
	return mrq.Host.Items().Serve(ctx, storedValidators(mrq), vw, it, formats.ServeStored)
}

// memberTags are the tags of an image name one member holds and the caller may read there; held says that the member holds anything of
// the name (a tags list, a manifest or a blob).
type memberTags struct {
	tags []string
	held bool
}

// localMemberTags reads every tag of the name a local member holds and the caller may read in it.
func (f *Format) localMemberTags(ctx context.Context, st Store, mrq *formats.Request, name string) (memberTags, error) {
	out := memberTags{}
	repo := mrq.RepoRef()
	cursor := ""
	for len(out.tags) < MaxTaglistEntries {
		page, err := st.Tags(ctx, repo.ID, name, cursor, MaxPage)
		if err != nil {
			return out, err
		}
		paths := make([]string, len(page.Tags))
		for i, t := range page.Tags {
			paths[i] = TagPath(name, t)
		}
		allowed, err := mrq.Host.PathsAllowed(ctx, mrq, formats.ActionRead, paths)
		if err != nil {
			return out, err
		}
		for i, t := range page.Tags {
			if allowed[i] {
				out.tags = append(out.tags, t)
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	out.held = len(out.tags) > 0
	if !out.held {
		held, err := st.Holds(ctx, repo.ID, name)
		if err != nil {
			return out, err
		}
		out.held = held
	}
	return out, nil
}

// remoteMemberTags reads the stored tags list of the name from a remote member (what its cache holds or, for a caller who may fill it,
// what its upstream answers for the metadata period), without the upstream's further pages: a list the upstream cut is continued only
// when a client addresses the remote itself (H-701). A member that cannot be asked or has no such image holds nothing.
func (f *Format) remoteMemberTags(ctx context.Context, mrq *formats.Request, client string) (memberTags, error) {
	gate, err := bothAllow(ctx, mrq, client, []string{""}, func(n, _ string) string { return TaglistPath(n) })
	if err != nil {
		return memberTags{}, err
	}
	if !gate[0] {
		return memberTags{}, nil
	}
	d, err := mrq.Host.Remote().Metadata(ctx, mrq, formats.Path{Path: TaglistPath(remoteName(mrq, client))}, "")
	if err != nil {
		if unreachable(err) || isMemberMiss(err) {
			return memberTags{}, nil
		}
		return memberTags{}, err
	}
	if d.Status.Negative {
		return memberTags{}, nil
	}
	raw, err := d.Bytes(ctx)
	if err != nil {
		return memberTags{}, err
	}
	stored, err := parseTaglist(raw)
	if err != nil {
		return memberTags{}, err
	}
	allowed, err := readableMask(ctx, mrq, client, stored.Tags)
	if err != nil {
		return memberTags{}, err
	}
	out := memberTags{held: true}
	for i, t := range stored.Tags {
		if allowed[i] {
			out.tags = append(out.tags, t)
		}
	}
	return out, nil
}

// virtualTagsList is the tags list of an image name through a virtual repository (R-OCI-027): the union of the tags of the members the
// plan admits, each decided for the caller in its member and again on the virtual repository, sorted, without repeats, and paged as a
// direct list is (R-OCI-015).
func (f *Format) virtualTagsList(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := rq.Params.Get("name")
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: TaglistPath(name), Actions: []formats.Action{formats.ActionRead}}); err != nil {
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
	locals, remotes, err := f.members(ctx, rq, TaglistPath(name))
	if err != nil {
		return err
	}
	st, err := storeOf(rq.Host)
	if err != nil {
		return err
	}
	var docs []formats.MemberDocument
	held := false
	var failure error
	for _, m := range append(locals, remotes...) {
		var mt memberTags
		var err error
		if m.pm.Tier == formats.TierLocal {
			mt, err = f.localMemberTags(ctx, st, m.rq, name)
		} else {
			mt, err = f.remoteMemberTags(ctx, m.rq, name)
		}
		if err != nil {
			if !isMemberFailure(err) {
				return err
			}
			failure = cmp.Or(failure, err)
			continue
		}
		held = held || mt.held
		doc, err := json.Marshal(tagsDoc{Name: name, Tags: mt.tags})
		if err != nil {
			return errs.E(errs.Internal, errs.CodeInternal, "The registry could not encode its answer.", errs.WithCause(err))
		}
		docs = append(docs, formats.MemberDocument{Member: m.pm.Member, Tier: m.pm.Tier, Position: m.pm.Position, Doc: doc})
	}
	if !held && failure != nil {
		return asAddressed(rq, failure)
	}
	if !held {
		return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "No member of the repository holds anything of the image name.")
	}
	merged, err := virtualAdapter{}.Merge(ctx, rq, formats.Path{Path: TaglistPath(name)}, docs)
	if err != nil {
		return err
	}
	var union mergedTaglist
	if err := json.Unmarshal(merged, &union); err != nil {
		return errs.E(errs.Internal, errs.CodeInternal, "The merged tags list is not a document the registry reads.", errs.WithCause(err))
	}
	tags := union.Tags[sort.SearchStrings(union.Tags, last+"\x00"):]
	doc := tagsDoc{Name: rq.Host.Links(rq).ClientName(name), Tags: []string{}}
	next := ""
	if n > 0 {
		doc.Tags, next, err = f.pageVisible(ctx, rq, tags, name, n)
		if err != nil {
			return err
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	if next != "" {
		w.Header().Set("Link", tagsListLink(rq, n, next))
	}
	return jsonReply(w, http.StatusOK, "application/json", doc)
}

// pageVisible takes the first n tags of the sorted union that the caller may read on the virtual repository itself (its exposure and the
// grants on the address the client typed), in chunks, and returns the tag a following page starts after: the last tag listed.
func (f *Format) pageVisible(ctx context.Context, rq *formats.Request, union []string, name string, n int) (tags []string, next string, err error) {
	tags = []string{}
	for start := 0; start < len(union); start += MaxPage {
		chunk := union[start:min(start+MaxPage, len(union))]
		paths := make([]string, len(chunk))
		for i, t := range chunk {
			paths[i] = TagPath(name, t)
		}
		allowed, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths)
		if err != nil {
			return nil, "", err
		}
		for i, t := range chunk {
			if !allowed[i] {
				continue
			}
			if len(tags) == n {
				return tags, tags[len(tags)-1], nil
			}
			tags = append(tags, t)
		}
	}
	return tags, "", nil
}

// unionDesc is a descriptor of a referrers answer with the digest it is deduplicated and ordered by.
type unionDesc struct {
	digest string
	raw    json.RawMessage
}

// virtualReferrers is the referrers API through a virtual repository (R-OCI-027): the union of the members' descriptors for the digest,
// deduplicated by digest, in digest order, filtered by artifactType and paged by R-OCI-016. A virtual repository without a local member
// whose remote members all have no referrers API answers 404, as the remote does, so that the client falls back to the tag schema.
func (f *Format) virtualReferrers(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	name := rq.Params.Get("name")
	subject, ok := digestFromText(rq.Params.Get("digest"))
	if !ok {
		return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest is not a sha256 digest.")
	}
	dgst := digestText(subject)
	path := ManifestPath(name, dgst)
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: path, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	query := rq.URL.Query()
	artifactType := query.Get("artifactType")
	if len(artifactType) > maxArtifactTypeBytes {
		return errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter artifactType is longer than 255 bytes.")
	}
	var after *[32]byte
	last := query.Get("last")
	if last != "" {
		d, ok := digestFromText(last)
		if !ok {
			return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The parameter last is not a sha256 digest.")
		}
		after = &d
	}
	locals, remotes, err := f.members(ctx, rq, path)
	if err != nil {
		return err
	}
	st, err := storeOf(rq.Host)
	if err != nil {
		return err
	}
	var union []unionDesc
	more, cut, answered := false, false, false
	for _, m := range locals {
		pg, err := readReferrers(ctx, st, m.rq.RepoRef().ID, name, subject, artifactType, after, func(ms []rows.Manifest) ([]bool, error) {
			paths := make([]string, len(ms))
			for i, x := range ms {
				paths[i] = ManifestPath(name, digestText(x.Digest))
			}
			return m.rq.Host.PathsAllowed(ctx, m.rq, formats.ActionRead, paths)
		})
		if err != nil {
			return err
		}
		answered, more, cut = true, more || pg.more, cut || pg.cut
		for _, d := range pg.descs {
			raw, err := json.Marshal(d)
			if err != nil {
				return errs.E(errs.Internal, errs.CodeInternal, "The registry could not encode its answer.", errs.WithCause(err))
			}
			union = append(union, unionDesc{d.Digest, raw})
		}
	}
	var failure error
	for _, m := range remotes {
		list, err := f.remoteReferrerList(ctx, m.rq, name, dgst, artifactType)
		switch {
		case err == nil:
		case isMemberMiss(err) || unreachable(err):
			continue
		case isMemberFailure(err):
			failure = cmp.Or(failure, err)
			continue
		default:
			return err
		}
		answered = true
		for _, raw := range list {
			var d struct {
				Digest string `json:"digest"`
			}
			if json.Unmarshal(raw, &d) == nil && (last == "" || d.Digest > last) {
				union = append(union, unionDesc{d.Digest, raw})
			}
		}
	}
	if !answered {
		if failure != nil {
			return asAddressed(rq, failure)
		}
		return errs.E(errs.NotFound, errs.CodeOCINameUnknown, "No member of the repository serves referrers for this digest.")
	}
	slices.SortStableFunc(union, func(a, b unionDesc) int { return cmp.Compare(a.digest, b.digest) })
	union = slices.CompactFunc(union, func(a, b unionDesc) bool { return a.digest == b.digest })
	page, cursor, cutOff, err := f.pageReferrers(ctx, rq, name, union)
	if err != nil {
		return err
	}
	h := w.Header()
	if artifactType != "" {
		h.Set("OCI-Filters-Applied", "artifactType")
	}
	if cut {
		h.Set("Trove-Annotations-Cut", "true")
	}
	if more || cutOff {
		h.Set("Link", referrersLink(rq, artifactType, cursor))
	}
	h.Set("Cache-Control", "no-cache")
	return jsonReply(w, http.StatusOK, mediaTypeImageIndex, struct {
		SchemaVersion int               `json:"schemaVersion"`
		MediaType     string            `json:"mediaType"`
		Manifests     []json.RawMessage `json:"manifests"`
	}{2, mediaTypeImageIndex, page})
}

// pageReferrers keeps the descriptors of the union the caller may read on the virtual repository itself, up to a page of MaxPage
// descriptors and MaxReferrersBytes. cursor is the digest of the last descriptor listed; cutOff says that descriptors follow it.
func (f *Format) pageReferrers(ctx context.Context, rq *formats.Request, name string, union []unionDesc) (page []json.RawMessage, cursor string, cutOff bool, err error) {
	page = []json.RawMessage{}
	paths := make([]string, len(union))
	for i, d := range union {
		paths[i] = ManifestPath(name, d.digest)
	}
	allowed := make([]bool, 0, len(union))
	for start := 0; start < len(paths); start += MaxPage {
		part, err := rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths[start:min(start+MaxPage, len(paths))])
		if err != nil {
			return nil, "", false, err
		}
		allowed = append(allowed, part...)
	}
	total := 0
	for i, d := range union {
		if !allowed[i] {
			continue
		}
		if len(page) >= MaxPage || (len(page) > 0 && total+len(d.raw)+1 > MaxReferrersBytes) {
			return page, cursor, true, nil
		}
		total += len(d.raw) + 1
		page = append(page, d.raw)
		cursor = d.digest
	}
	return page, cursor, false, nil
}
