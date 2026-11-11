package ansible

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// docsStore is the documentation group of the data port (R-ANS-033 to R-ANS-036, the interface ansible.DocsStore): the statements on
// ansible_version_docs, ansible_doc_entries, ansible_doc_files and ansible_doc_fetches (queries/ansible_docs.sql). The documentation is
// derived data: every write is a replacement of the rows of one version, in the transaction of the publish, the fill or the removal.
type docsStore struct{ *group }

// readmeFile is the file_name of the README in ansible_doc_files (R-ANS-033).
const readmeFile = "README.md"

// docWarning is the stored form of a warning.
type docWarning struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PutDocs implements ansible.DocsStore: the rows of the version are deleted and written again, and its fetch row goes (the documentation
// that exists is the answer; the fetch row only explains the lack of it).
func (s docsStore) PutDocs(ctx context.Context, tx meta.Tx, repoID int64, name, version string, d rows.DocSet) error {
	return s.read(ctx, tx, func(qs *q.Queries) error {
		if _, err := qs.AnsibleDocsDeleteVersion(ctx, q.AnsibleDocsDeleteVersionParams{RepoID: repoID, Name: name, Version: version}); err != nil {
			return err
		}
		if err := qs.AnsibleDocsDeleteFetch(ctx, q.AnsibleDocsDeleteFetchParams{RepoID: repoID, Name: name, Version: version}); err != nil {
			return err
		}
		warns := make([]docWarning, 0, len(d.Warnings))
		for _, w := range d.Warnings[:min(len(d.Warnings), rows.MaxDocWarnings)] {
			warns = append(warns, docWarning(w))
		}
		wj, err := json.Marshal(warns)
		if err != nil {
			return err
		}
		var total int64
		files := docFiles(d)
		for _, f := range files {
			total += int64(len(f.Body))
		}
		for _, e := range d.Entries {
			total += int64(len(e.Doc) + len(e.ReadmeMD) + len(e.ReadmeHTML) + len(e.SearchText))
		}
		if err := qs.AnsibleDocsInsertVersion(ctx, q.AnsibleDocsInsertVersionParams{
			RepoID: repoID, Name: name, Version: version, Source: d.Source, Generator: int32(d.Generator), //nolint:gosec // a small constant
			EntryCount: int32(len(d.Entries)), TotalBytes: total, Truncated: d.Truncated, Warnings: wj, //nolint:gosec // at most 5,000
		}); err != nil {
			return err
		}
		for _, e := range d.Entries {
			if err := qs.AnsibleDocsInsertEntry(ctx, q.AnsibleDocsInsertEntryParams{
				RepoID: repoID, Name: name, Version: version, ContentType: e.Type, ContentName: e.Name, ShortDescription: e.ShortDescription,
				HasDocs: e.HasDocs, Doc: string(e.Doc), ReadmeMd: e.ReadmeMD, ReadmeHtml: e.ReadmeHTML, SearchText: e.SearchText,
			}); err != nil {
				return err
			}
		}
		for _, f := range files {
			if err := qs.AnsibleDocsInsertFile(ctx, q.AnsibleDocsInsertFileParams{
				RepoID: repoID, Name: name, Version: version, FileName: f.Name, Format: f.Format, Body: f.Body,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// docFiles is the README, named README.md, and the files of docs/.
func docFiles(d rows.DocSet) []rows.DocFile {
	var out []rows.DocFile
	if d.Readme != nil {
		r := *d.Readme
		r.Name = readmeFile
		out = append(out, r)
	}
	return append(out, d.Files...)
}

// RemoveDocs implements ansible.DocsStore.
func (s docsStore) RemoveDocs(ctx context.Context, tx meta.Tx, repoID int64, name, version string) error {
	return s.read(ctx, tx, func(qs *q.Queries) error {
		if _, err := qs.AnsibleDocsDeleteVersion(ctx, q.AnsibleDocsDeleteVersionParams{RepoID: repoID, Name: name, Version: version}); err != nil {
			return err
		}
		return qs.AnsibleDocsDeleteFetch(ctx, q.AnsibleDocsDeleteFetchParams{RepoID: repoID, Name: name, Version: version})
	})
}

// SetDocFetch implements ansible.DocsStore.
func (s docsStore) SetDocFetch(ctx context.Context, tx meta.Tx, repoID int64, name, version, state, reason string) error {
	return s.read(ctx, tx, func(qs *q.Queries) error {
		return qs.AnsibleDocsUpsertFetch(ctx, q.AnsibleDocsUpsertFetchParams{RepoID: repoID, Name: name, Version: version, State: state, Reason: reason})
	})
}

// DocsFillArgs are the arguments of the job ansible-docs-fill.
type DocsFillArgs struct {
	RepoID  int64  `json:"repoId"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// EnqueueDocsFill implements ansible.DocsStore: the queued fetch row and the job, in tx. The job kind coalesces on the key of the version,
// so a second enqueue finds the first and queues nothing.
func (s docsStore) EnqueueDocsFill(ctx context.Context, tx meta.Tx, repoID int64, name, version string) error {
	if s.o.Jobs == nil {
		return errNotBuilt("the documentation fetch queue")
	}
	if err := s.SetDocFetch(ctx, tx, repoID, name, version, rows.DocFetchQueued, ""); err != nil {
		return err
	}
	_, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{
		Kind: DocsFillJobKind, DedupKey: DocsFillKey(repoID, name, version), Args: DocsFillArgs{RepoID: repoID, Name: name, Version: version},
		TargetKind: "repository", TargetKey: strconv.FormatInt(repoID, 10),
	})
	return err
}

// DocState implements ansible.DocsStore.
func (s docsStore) DocState(ctx context.Context, repoID int64, name, version string) (rows.DocState, error) {
	var st rows.DocState
	err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		st, err = docState(ctx, qs, repoID, name, version)
		return err
	})
	return st, err
}

func docState(ctx context.Context, qs *q.Queries, repoID int64, name, version string) (rows.DocState, error) {
	v, err := qs.AnsibleDocsGetVersion(ctx, q.AnsibleDocsGetVersionParams{RepoID: repoID, Name: name, Version: version})
	if err == nil {
		return rows.DocState{
			State: rows.DocAvailable, Source: v.Source, Entries: int(v.EntryCount), Truncated: v.Truncated, BuiltAt: v.BuiltAt.Time.UTC(),
		}, nil
	}
	if !postgres.IsNoRows(err) {
		return rows.DocState{}, err
	}
	f, err := qs.AnsibleDocsGetFetch(ctx, q.AnsibleDocsGetFetchParams{RepoID: repoID, Name: name, Version: version})
	switch {
	case postgres.IsNoRows(err):
		return rows.DocState{State: rows.DocNone}, nil
	case err != nil:
		return rows.DocState{}, err
	case f.State == rows.DocFetchQueued && f.Fresh:
		return rows.DocState{State: rows.DocPending}, nil
	case f.State == rows.DocFetchUnavailable:
		return rows.DocState{State: rows.DocUnavailable, Reason: f.Reason}, nil
	case f.State == rows.DocFetchNone && f.Fresh:
		return rows.DocState{State: rows.DocNone, NoneKnown: true}, nil
	}
	return rows.DocState{State: rows.DocNone}, nil
}

// DocTree implements ansible.DocsStore.
func (s docsStore) DocTree(ctx context.Context, repoID int64, name, version string, query *rows.DocQuery) (rows.DocTree, error) {
	var tree rows.DocTree
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		st, err := docState(ctx, qs, repoID, name, version)
		if err != nil {
			return err
		}
		tree.State = st
		if st.State != rows.DocAvailable {
			return nil
		}
		terms := []string{}
		if query != nil {
			terms = query.Terms
		}
		ents, err := qs.AnsibleDocsTree(ctx, q.AnsibleDocsTreeParams{
			RepoID: repoID, Name: name, Version: version, HasTerms: len(terms) > 0, Terms: terms,
		})
		if err != nil {
			return err
		}
		for _, e := range ents {
			tree.Entries = append(tree.Entries, rows.DocEntryHead{Type: e.ContentType, Name: e.ContentName, ShortDescription: e.ShortDescription, HasDocs: e.HasDocs})
		}
		counts, err := qs.AnsibleDocsTypeCounts(ctx, q.AnsibleDocsTypeCountsParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		tree.Counts = map[string]int{}
		for _, c := range counts {
			tree.Counts[c.ContentType] = int(c.Entries)
			tree.CountBeforeQuery += int(c.Entries)
		}
		heads, err := qs.AnsibleDocsFileHeads(ctx, q.AnsibleDocsFileHeadsParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		for _, h := range heads {
			title := rows.DocFileTitle(rows.DocFile{Name: h.FileName, Format: h.Format, Body: h.Head})
			if matchTitle(title, terms) {
				tree.Files = append(tree.Files, rows.DocFileHead{Name: h.FileName, Title: title})
			}
		}
		// The README first, then the files in name order (the statement orders by the byte order of the name).
		sort.SliceStable(tree.Files, func(i, j int) bool {
			return tree.Files[i].Name == readmeFile && tree.Files[j].Name != readmeFile
		})
		v, err := qs.AnsibleDocsGetVersion(ctx, q.AnsibleDocsGetVersionParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		var ws []docWarning
		_ = json.Unmarshal(v.Warnings, &ws) //nolint:errcheck // a column this package wrote; an unreadable one is no warnings
		for _, w := range ws {
			tree.Warnings = append(tree.Warnings, rows.DocWarning(w))
		}
		return nil
	})
	return tree, err
}

// matchTitle is the rule for a file: every term is a case-insensitive substring of the title (R-ANS-036).
func matchTitle(title string, terms []string) bool {
	lower := strings.ToLower(title)
	return !slices.ContainsFunc(terms, func(t string) bool { return !strings.Contains(lower, strings.ToLower(t)) })
}

func entryOf(typ, name, short string, hasDocs bool, doc json.RawMessage, md, html, search string) rows.DocEntry {
	e := rows.DocEntry{Type: typ, Name: name, ShortDescription: short, HasDocs: hasDocs, SearchText: search, ReadmeMD: md, ReadmeHTML: html}
	if hasDocs {
		e.Doc = doc
	}
	return e
}

// DocEntry implements ansible.DocsStore.
func (s docsStore) DocEntry(ctx context.Context, repoID int64, name, version, typ, entry string) (rows.DocEntry, bool, error) {
	var out rows.DocEntry
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		r, err := qs.AnsibleDocsGetEntry(ctx, q.AnsibleDocsGetEntryParams{RepoID: repoID, Name: name, Version: version, ContentType: typ, ContentName: entry})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		out = entryOf(r.ContentType, r.ContentName, r.ShortDescription, r.HasDocs, r.Doc, r.ReadmeMd.String, r.ReadmeHtml.String, r.SearchText)
		return nil
	})
	return out, found, err
}

// DocFile implements ansible.DocsStore.
func (s docsStore) DocFile(ctx context.Context, repoID int64, name, version, file string) (rows.DocFile, bool, error) {
	var out rows.DocFile
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		r, err := qs.AnsibleDocsGetFile(ctx, q.AnsibleDocsGetFileParams{RepoID: repoID, Name: name, Version: version, FileName: file})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found, out = true, rows.DocFile{Name: r.FileName, Format: r.Format, Body: r.Body}
		return nil
	})
	return out, found, err
}

// DocSet implements ansible.DocsStore.
func (s docsStore) DocSet(ctx context.Context, repoID int64, name, version string) (rows.DocSet, bool, error) {
	var set rows.DocSet
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		v, err := qs.AnsibleDocsGetVersion(ctx, q.AnsibleDocsGetVersionParams{RepoID: repoID, Name: name, Version: version})
		if postgres.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		set = rows.DocSet{Source: v.Source, Generator: int(v.Generator), Truncated: v.Truncated}
		var ws []docWarning
		_ = json.Unmarshal(v.Warnings, &ws) //nolint:errcheck // a column this package wrote; an unreadable one is no warnings
		for _, w := range ws {
			set.Warnings = append(set.Warnings, rows.DocWarning(w))
		}
		ents, err := qs.AnsibleDocsAllEntries(ctx, q.AnsibleDocsAllEntriesParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		for _, r := range ents {
			set.Entries = append(set.Entries, entryOf(r.ContentType, r.ContentName, r.ShortDescription, r.HasDocs, r.Doc,
				r.ReadmeMd.String, r.ReadmeHtml.String, r.SearchText))
		}
		files, err := qs.AnsibleDocsAllFiles(ctx, q.AnsibleDocsAllFilesParams{RepoID: repoID, Name: name, Version: version})
		if err != nil {
			return err
		}
		for _, f := range files {
			df := rows.DocFile{Name: f.FileName, Format: f.Format, Body: f.Body}
			if f.FileName == readmeFile {
				set.Readme = &df
			} else {
				set.Files = append(set.Files, df)
			}
		}
		return nil
	})
	return set, found, err
}

// docsReindexBatch is the number of versions one step of the documentation reindex reads.
const docsReindexBatch = 25

// RebuildDocs implements ansible.DocsStore: the step of the reindex for a local repository. It generates the documentation of every
// stored version after the checkpoint (name and version joined by a slash) that has none or has one made by an older generator, from
// the archive, one transaction per version. A reader that cannot generate documentation (it implements no rows.DocsReader) is an error.
func (s docsStore) RebuildDocs(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (string, bool, error) {
	dr, ok := reader.(rows.DocsReader)
	if !ok {
		return checkpoint, false, errNotBuilt("a reader of documentation")
	}
	var list []q.AnsibleDocsStaleVersionsRow
	if err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.AnsibleDocsStaleVersions(ctx, q.AnsibleDocsStaleVersionsParams{
			RepoID: repoID, After: checkpoint, Generator: docgen.Version, RowLimit: docsReindexBatch,
		})
		return err
	}); err != nil {
		return checkpoint, false, err
	}
	next := checkpoint
	for _, v := range list {
		next = v.Name + "/" + v.Version
		set, err := dr.ReadDocs(ctx, rows.ArchiveRef{ItemID: v.ItemID, Path: v.Path, SHA256: digestOf(v.Sha256), Size: v.Size})
		if err != nil {
			// An archive the reader refuses or cannot read is left as it is: the reindex of the rows reports it.
			continue
		}
		if set.Readme == nil && len(set.Files) == 0 && len(set.Entries) == 0 {
			continue
		}
		if err := s.o.DB.InTx(ctx, func(tx meta.Tx) error {
			if err := s.PutDocs(ctx, tx, repoID, v.Name, v.Version, set); err != nil {
				return err
			}
			// The documentation is part of what a virtual repository's cached documents are built from.
			qs, err := txQueries(tx)
			if err != nil {
				return err
			}
			_, err = qs.AnsibleBumpPackage(ctx, q.AnsibleBumpPackageParams{RepoID: repoID, Name: v.Name})
			return err
		}); err != nil {
			return next, false, err
		}
	}
	return next, len(list) < docsReindexBatch, nil
}

// RebuildRemoteDocs implements ansible.DocsStore: the step of the reindex of a remote. The documentation of a remote is derived from the
// documents the engine stored, which only the engine reads, so the step queues the job ansible-docs-fill for every version that is known
// and has no rows; the job reads the stored document while it is fresh and fetches it otherwise (R-ANS-044).
func (s docsStore) RebuildRemoteDocs(ctx context.Context, repoID int64, checkpoint string) (string, bool, error) {
	var list []q.AnsibleDocsRemoteWithoutRowsRow
	if err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.AnsibleDocsRemoteWithoutRows(ctx, q.AnsibleDocsRemoteWithoutRowsParams{RepoID: repoID, After: checkpoint, RowLimit: docsReindexBatch})
		return err
	}); err != nil {
		return checkpoint, false, err
	}
	next := checkpoint
	for _, v := range list {
		next = v.Name + "/" + v.Version
		if err := s.o.DB.InTx(ctx, func(tx meta.Tx) error { return s.EnqueueDocsFill(ctx, tx, repoID, v.Name, v.Version) }); err != nil {
			return next, false, err
		}
	}
	return next, len(list) < docsReindexBatch, nil
}
