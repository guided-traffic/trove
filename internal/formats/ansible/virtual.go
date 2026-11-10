package ansible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
	"github.com/guided-traffic/trove/internal/formats/ansible/merge"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// variantFirstPage is the variant of the version list a merge asks for: the first stored page of a remote member, whose further pages
// the adapter reads itself (virtual_remote.go).
const variantFirstPage = "offset=0"

// pageSize is the stored unit of an upstream's version list (Q-ANS-12): the variants of its pages are offset=0, offset=100, ….
const pageSize = 100

type virtualAdapter struct{ f *Format }

var _ formats.VirtualAdapter = virtualAdapter{}

// vdocKind tells the documents of a collection apart.
type vdocKind uint8

const (
	vdocVersions vdocKind = iota + 1
	vdocCollection
	vdocVersion
)

// documentOf classifies a canonical path as a document of a collection and returns its parts; ok is false for any other path.
func documentOf(p formats.Path) (kind vdocKind, ns, name, ver string, ok bool) {
	segs := strings.Split(p.Path, "/")
	if len(segs) != 4 || segs[0] != dirCollections || !collection.ValidPart(segs[1]) || !collection.ValidPart(segs[2]) {
		return 0, "", "", "", false
	}
	ns, name = segs[1], segs[2]
	switch file := segs[3]; file {
	case VersionsDocument:
		return vdocVersions, ns, name, "", true
	case CollectionDocument:
		return vdocCollection, ns, name, "", true
	default:
		if v, cut := strings.CutSuffix(file, jsonSuffix); cut && version.Valid(v) {
			return vdocVersion, ns, name, v, true
		}
	}
	return 0, "", "", "", false
}

// Strategy: the version list and the collection document are merged over all members (R-ANS-022); any other path names the document of
// a version, an archive or a role document and is resolved to the first member that holds it (R-REPO-019 step 6).
func (virtualAdapter) Strategy(p formats.Path) formats.Strategy {
	if kind, _, _, _, ok := documentOf(p); ok && kind != vdocVersion {
		return formats.Merge
	}
	return formats.FirstMatch
}

// ArtifactPrefix: the archives of a collection are cached below its directory, "collections/<ns>/<name>/" (R-ANS-002).
func (virtualAdapter) ArtifactPrefix(name string) string {
	if ns, n, ok := collection.SplitName(name); ok {
		return CollectionDir(ns, n) + "/"
	}
	return ""
}

// listEntry is one version of a list document, the form a member contributes and the merge returns. SHA256 is the member's archive
// digest where it knows one; Member is set by the merge.
type listEntry struct {
	Version         string    `json:"version"`
	RequiresAnsible string    `json:"requires_ansible,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	SHA256          string    `json:"sha256,omitempty"`
	Member          string    `json:"member,omitempty"`
}

// listDoc is the document of a local member's versions and the merged list.
type listDoc struct {
	Entries []listEntry `json:"entries"`
}

func (e listEntry) galaxy() galaxydoc.VersionEntry {
	return galaxydoc.VersionEntry{Version: e.Version, RequiresAnsible: e.RequiresAnsible, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
}

func internalDoc(what string, err error) error {
	return errs.E(errs.Internal, errs.CodeInternal, what, errs.WithCause(err))
}

func decodeList(doc []byte) (listDoc, error) {
	var l listDoc
	if err := json.Unmarshal(doc, &l); err != nil {
		return l, internalDoc("A merged version list of the Ansible format does not decode.", err)
	}
	return l, nil
}

// localPageSize is the page the adapter reads a local member's versions in.
const localPageSize = 1000

// LocalDocument renders the document of a local member from its rows and returns the revision it was rendered at (R-ARCH-015). The
// revision is read before the rows, so a commit that raced the read can only make the recorded revision older than the document, which
// the next hit finds out and rebuilds. A member without the collection holds no document.
func (a virtualAdapter) LocalDocument(ctx context.Context, rq *formats.Request, member formats.RepoRef, p formats.Path, _ string) ([]byte, formats.RevSeq, bool, error) {
	kind, ns, n, ver, ok := documentOf(p)
	if !ok {
		return nil, 0, false, errs.E(errs.Internal, errs.CodeInternal, "The path is no document of a collection of the Ansible format.")
	}
	name := ns + "." + n
	st, err := storeOf(rq.Host.FormatData(ID))
	if err != nil {
		return nil, 0, false, err
	}
	row, found, err := rq.Host.Packages().Package(ctx, member, name)
	if err != nil || !found {
		return nil, 0, false, err
	}
	rev := row.Ref.RevSeq
	switch kind {
	case vdocVersions:
		var l listDoc
		for off := 0; ; off += localPageSize {
			page, total, err := st.Versions(ctx, member.ID, name, localPageSize, off)
			if err != nil {
				return nil, 0, false, err
			}
			for _, v := range page {
				l.Entries = append(l.Entries, listEntry{
					Version: v.Version, RequiresAnsible: v.RequiresAnsible, CreatedAt: v.PublishedAt, UpdatedAt: v.PublishedAt,
					SHA256: hex.EncodeToString(v.SHA256[:]),
				})
			}
			if len(page) == 0 || off+len(page) >= total {
				break
			}
		}
		if len(l.Entries) == 0 {
			return nil, rev, false, nil
		}
		doc, err := json.Marshal(l)
		return doc, rev, err == nil, wrapInternal(err)
	case vdocCollection:
		sum, found, err := st.Collection(ctx, member.ID, name)
		if err != nil || !found {
			return nil, rev, false, err
		}
		return galaxydoc.RenderCollection(sum, galaxydoc.Addr{}), rev, true, nil
	default:
		row, found, err := st.Version(ctx, member.ID, name, ver)
		if err != nil || !found {
			return nil, rev, false, err
		}
		v := versionOfRow(row)
		return galaxydoc.RenderVersion(v, galaxydoc.Addr{Repo: rq.Host.Links(rq).Registry(rq.Addressed())}), rev, true, nil
	}
}

func wrapInternal(err error) error {
	if err == nil {
		return nil
	}
	return internalDoc("The Ansible format could not encode a document.", err)
}

// versionOfRow is the neutral form of a stored version.
func versionOfRow(r rows.VersionRow) galaxydoc.Version {
	ns, n, _ := collection.SplitName(r.Name)
	return galaxydoc.Version{
		Namespace: ns, Name: n, Version: r.Version, RequiresAnsible: r.RequiresAnsible, SHA256: r.SHA256, Size: r.Size,
		Dependencies: r.Dependencies, Info: r.CollectionInfo, CreatedAt: r.PublishedAt, UpdatedAt: r.PublishedAt,
		CollectionID: rows.CollectionUUID(r.RepoID, r.Name),
	}
}

// Merge applies the per-version source rule (R-ANS-022): the entry of a version comes from the lowest (tier, position) member that
// holds it, a remote member the caller may not fill supplies the versions it has cached only (R-AUTHZ-008), and the versions the
// members hold with different archive digests are counted.
func (a virtualAdapter) Merge(ctx context.Context, rq *formats.Request, p formats.Path, parts []formats.MemberDocument) ([]byte, error) {
	kind, ns, name, _, ok := documentOf(p)
	switch {
	case ok && kind == vdocVersions:
		return a.mergeVersions(ctx, rq, ns, name, parts)
	case ok && kind == vdocCollection:
		return mergeTimes(parts)
	}
	return nil, errs.E(errs.Internal, errs.CodeInternal, "The path is no merged document of the Ansible format.")
}

// mergeVersions merges the version lists of the members.
func (a virtualAdapter) mergeVersions(ctx context.Context, rq *formats.Request, ns, name string, parts []formats.MemberDocument) ([]byte, error) {
	budget := newPageBudget()
	var in []merge.Part
	for _, d := range parts {
		ps, err := a.f.memberParts(ctx, rq, d, ns, name, budget)
		if err != nil {
			return nil, err
		}
		in = append(in, ps...)
	}
	a.f.recordCollisions(ctx, rq, ns, name, parts, in, budget)
	m := merge.MergeLists(in)
	out := listDoc{Entries: make([]listEntry, len(m.Entries))}
	for i, e := range m.Entries {
		src := in[m.Source[i]]
		le := listEntry{Version: e.Version, RequiresAnsible: e.RequiresAnsible, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Member: src.Member}
		if sum, ok := src.SHA256[e.Version]; ok {
			le.SHA256 = hex.EncodeToString(sum[:])
		}
		out.Entries[i] = le
	}
	if len(m.Conflicts) > 0 {
		a.f.virtualMetricsOf(rq.Host).conflicts(rq.Repo.Name.String(), len(m.Conflicts))
	}
	doc, err := json.Marshal(out)
	if err != nil {
		return nil, wrapInternal(err)
	}
	return doc, nil
}

// timesDoc is the merged collection document: only the times are merged.
type timesDoc struct {
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// mergeTimes merges the collection documents of the members: the earliest creation and the latest update (R-ANS-022).
func mergeTimes(parts []formats.MemberDocument) ([]byte, error) {
	var ts []merge.Times
	for _, d := range parts {
		if len(d.Doc) == 0 {
			continue
		}
		var t struct {
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		}
		if err := json.Unmarshal(d.Doc, &t); err != nil {
			return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's collection document is not a document the registry reads.", errs.WithCause(err))
		}
		ts = append(ts, merge.Times{CreatedAt: parseDocTime(t.CreatedAt), UpdatedAt: parseDocTime(t.UpdatedAt)})
	}
	c := merge.MergeCollection(nil, ts)
	doc, err := json.Marshal(timesDoc{CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
	return doc, wrapInternal(err)
}

// parseDocTime reads a time of a Galaxy document; one that does not parse is the zero time, which is no information.
func parseDocTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
