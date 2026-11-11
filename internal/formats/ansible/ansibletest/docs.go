package ansibletest

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

// The twin of the documentation group of the data port (R-ANS-033 to R-ANS-036). It keeps the sets as they were put and answers the
// reads with the rules of the PostgreSQL store that a handler can tell apart: the states, a queued row that is older than an hour counting
// as none, the tree in type and name order with the counts before the query, and the match of a term against the name (a substring) or
// the search text (a prefix of a word).

// queuedFetchLife is how long a queued fetch row counts (R-ANS-033).
const queuedFetchLife = time.Hour

type docRow struct {
	set   rows.DocSet
	built time.Time
}

type fetchRow struct {
	state, reason string
	at            time.Time
}

// DocsFillJob is one job ansible-docs-fill the twin was asked to queue.
type DocsFillJob struct {
	RepoID        int64
	Name, Version string
}

func (s *Store) docInit() {
	if s.docs == nil {
		s.docs, s.fetches = map[key]docRow{}, map[key]fetchRow{}
	}
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// PutDocs stores the set and deletes the fetch row of the version.
func (s *Store) PutDocs(_ context.Context, _ formats.Tx, repoID int64, name, version string, d rows.DocSet) error {
	defer s.lock()()
	s.docInit()
	k := key{repoID, name, version}
	s.docs[k] = docRow{set: d, built: s.now()}
	delete(s.fetches, k)
	return nil
}

// RemoveDocs deletes the rows of a version.
func (s *Store) RemoveDocs(_ context.Context, _ formats.Tx, repoID int64, name, version string) error {
	defer s.lock()()
	s.docInit()
	k := key{repoID, name, version}
	delete(s.docs, k)
	delete(s.fetches, k)
	return nil
}

// SetDocFetch records the fetch row of a version.
func (s *Store) SetDocFetch(_ context.Context, _ formats.Tx, repoID int64, name, version, state, reason string) error {
	defer s.lock()()
	s.docInit()
	s.fetches[key{repoID, name, version}] = fetchRow{state: state, reason: reason, at: s.now()}
	return nil
}

// EnqueueDocsFill records the queued fetch row and the job, unless the version already has a queued one.
func (s *Store) EnqueueDocsFill(_ context.Context, _ formats.Tx, repoID int64, name, version string) error {
	defer s.lock()()
	s.docInit()
	k := key{repoID, name, version}
	if f, ok := s.fetches[k]; ok && f.state == rows.DocFetchQueued && s.now().Sub(f.at) < queuedFetchLife {
		return nil
	}
	s.fetches[k] = fetchRow{state: rows.DocFetchQueued, at: s.now()}
	s.DocsJobs = append(s.DocsJobs, DocsFillJob{RepoID: repoID, Name: name, Version: version})
	return nil
}

func (s *Store) docState(k key) rows.DocState {
	if r, ok := s.docs[k]; ok {
		return rows.DocState{
			State: rows.DocAvailable, Source: r.set.Source, Entries: len(r.set.Entries), Truncated: r.set.Truncated, BuiltAt: r.built,
		}
	}
	f, ok := s.fetches[k]
	switch {
	case !ok:
	case f.state == rows.DocFetchQueued && s.now().Sub(f.at) < queuedFetchLife:
		return rows.DocState{State: rows.DocPending}
	case f.state == rows.DocFetchUnavailable:
		return rows.DocState{State: rows.DocUnavailable, Reason: f.reason}
	}
	return rows.DocState{State: rows.DocNone}
}

// DocState reads the state of a version.
func (s *Store) DocState(_ context.Context, repoID int64, name, version string) (rows.DocState, error) {
	defer s.lock()()
	s.docInit()
	return s.docState(key{repoID, name, version}), nil
}

// DocTree reads the tree: the entries in type and name order, those that match q, and the counts before q.
func (s *Store) DocTree(_ context.Context, repoID int64, name, version string, q *rows.DocQuery) (rows.DocTree, error) {
	defer s.lock()()
	s.docInit()
	k := key{repoID, name, version}
	t := rows.DocTree{State: s.docState(k), Counts: map[string]int{}}
	r, ok := s.docs[k]
	if !ok {
		return t, nil
	}
	t.Warnings = append(t.Warnings, r.set.Warnings...)
	entries := append([]rows.DocEntry(nil), r.set.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		return entries[i].Name < entries[j].Name
	})
	t.CountBeforeQuery = len(entries)
	for _, e := range entries {
		t.Counts[e.Type]++
		if q == nil || matchEntry(e, q.Terms) {
			t.Entries = append(t.Entries, rows.DocEntryHead{Type: e.Type, Name: e.Name, ShortDescription: e.ShortDescription, HasDocs: e.HasDocs})
		}
	}
	for _, f := range docFiles(r.set) {
		if title := rows.DocFileTitle(f); q == nil || matchTerms(title, "", q.Terms) {
			t.Files = append(t.Files, rows.DocFileHead{Name: f.Name, Title: title})
		}
	}
	return t, nil
}

// docFiles lists the README first, then the files of docs/ in name order.
func docFiles(d rows.DocSet) []rows.DocFile {
	var out []rows.DocFile
	if d.Readme != nil {
		out = append(out, *d.Readme)
	}
	files := append([]rows.DocFile(nil), d.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(out, files...)
}

func matchEntry(e rows.DocEntry, terms []string) bool { return matchTerms(e.Name, e.SearchText, terms) }

// matchTerms is the rule of R-ANS-036: every term matches the name as a case-insensitive substring, or the search text as the prefix of one
// of its words.
func matchTerms(name, search string, terms []string) bool {
	words := strings.FieldsFunc(strings.ToLower(search), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	lower := strings.ToLower(name)
	for _, term := range terms {
		term = strings.ToLower(term)
		ok := strings.Contains(lower, term)
		for _, w := range words {
			if ok {
				break
			}
			ok = strings.HasPrefix(w, term)
		}
		if !ok {
			return false
		}
	}
	return true
}

// DocEntry reads one entry.
func (s *Store) DocEntry(_ context.Context, repoID int64, name, version, typ, entry string) (rows.DocEntry, bool, error) {
	defer s.lock()()
	s.docInit()
	for _, e := range s.docs[key{repoID, name, version}].set.Entries {
		if e.Type == typ && e.Name == entry {
			return e, true, nil
		}
	}
	return rows.DocEntry{}, false, nil
}

// DocFile reads the README or a file of docs/.
func (s *Store) DocFile(_ context.Context, repoID int64, name, version, file string) (rows.DocFile, bool, error) {
	defer s.lock()()
	s.docInit()
	for _, f := range docFiles(s.docs[key{repoID, name, version}].set) {
		if f.Name == file {
			return f, true, nil
		}
	}
	return rows.DocFile{}, false, nil
}

// DocSet reads the whole set.
func (s *Store) DocSet(_ context.Context, repoID int64, name, version string) (rows.DocSet, bool, error) {
	defer s.lock()()
	s.docInit()
	r, ok := s.docs[key{repoID, name, version}]
	return r.set, ok, nil
}

// RebuildDocs does nothing: the reindex is tested against PostgreSQL.
func (s *Store) RebuildDocs(context.Context, int64, rows.ArchiveReader, string) (string, bool, error) {
	return "", true, nil
}

// RebuildRemoteDocs does nothing, for the same reason.
func (s *Store) RebuildRemoteDocs(context.Context, int64, string) (string, bool, error) {
	return "", true, nil
}
