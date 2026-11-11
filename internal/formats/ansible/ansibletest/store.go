// Package ansibletest is the in-memory twin of the data port of the Ansible Galaxy format (internal/formatdata/ansible): the format's unit
// tests and the conformance kit run the real handlers and hooks against it, with the semantics of the PostgreSQL store that a handler can
// tell apart (the order of a version list, the tombstones keyed by the number, the final import tasks). It is a test helper.
package ansibletest

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
)

// Store is the twin. The zero value is empty and ready.
type Store struct {
	mu         sync.Mutex
	versions   map[key]rows.VersionRow
	tombstones map[key]rows.Tombstone
	tasks      []rows.Task
	// Audits are the audit contexts of the writes, in order, for a test that asks who acted.
	Audits []rows.Audit
	// Published are the PublishInfo values of the stored versions, in order.
	Published []rows.PublishInfo
	// Now is the clock of the documentation rows (the age of a queued fetch row); nil is the wall clock.
	Now func() time.Time
	// DocsJobs are the jobs ansible-docs-fill the store was asked to queue, in order.
	DocsJobs []DocsFillJob
	docs     map[key]docRow
	fetches  map[key]fetchRow
}

type key struct {
	repo          int64
	name, version string
}

func (s *Store) lock() func() {
	s.mu.Lock()
	if s.versions == nil {
		s.versions, s.tombstones = map[key]rows.VersionRow{}, map[key]rows.Tombstone{}
	}
	return s.mu.Unlock
}

// PutVersion stores the row.
func (s *Store) PutVersion(_ context.Context, _ formats.Tx, v rows.VersionRow, a rows.Audit, p rows.PublishInfo) error {
	defer s.lock()()
	s.versions[key{v.RepoID, v.Name, v.Version}] = v
	s.Audits, s.Published = append(s.Audits, a), append(s.Published, p)
	return nil
}

// RemoveVersion deletes the row and leaves the tombstone of the number.
func (s *Store) RemoveVersion(_ context.Context, _ formats.Tx, repoID int64, name, stored string, sha256 []byte, tombstone bool, a rows.Audit) error {
	defer s.lock()()
	delete(s.versions, key{repoID, name, stored})
	if !tombstone {
		return nil
	}
	k := key{repoID, name, version.Number(stored)}
	if _, ok := s.tombstones[k]; ok {
		return nil
	}
	t := rows.Tombstone{RepoID: repoID, Name: name, Version: k.version, DeletedBy: a.Actor.ID}
	copy(t.SHA256[:], sha256)
	s.tombstones[k] = t
	s.Audits = append(s.Audits, a)
	return nil
}

// Version reads a row.
func (s *Store) Version(_ context.Context, repoID int64, name, ver string) (rows.VersionRow, bool, error) {
	defer s.lock()()
	v, ok := s.versions[key{repoID, name, ver}]
	return v, ok, nil
}

func (s *Store) sorted(repoID int64, name string) []rows.VersionRow {
	var out []rows.VersionRow
	for k, v := range s.versions {
		if k.repo == repoID && k.name == name {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].SortKey, out[j].SortKey) > 0 })
	return out
}

// Versions lists a collection newest first.
func (s *Store) Versions(_ context.Context, repoID int64, name string, limit, offset int) ([]rows.VersionRow, int, error) {
	defer s.lock()()
	all := s.sorted(repoID, name)
	if offset >= len(all) {
		return nil, len(all), nil
	}
	return all[offset:min(offset+limit, len(all))], len(all), nil
}

// Collection computes the summary of a collection.
func (s *Store) Collection(_ context.Context, repoID int64, name string) (rows.CollectionSummary, bool, error) {
	defer s.lock()()
	all := s.sorted(repoID, name)
	if len(all) == 0 {
		return rows.CollectionSummary{}, false, nil
	}
	ns, n, _ := collection.SplitName(name)
	c := rows.CollectionSummary{Namespace: ns, Name: n, CollectionID: rows.CollectionUUID(repoID, name), HighestVersion: all[0].Version, CreatedAt: all[0].PublishedAt, UpdatedAt: all[0].PublishedAt}
	for _, v := range all {
		if v.PublishedAt.Before(c.CreatedAt) {
			c.CreatedAt = v.PublishedAt
		}
		if v.PublishedAt.After(c.UpdatedAt) {
			c.UpdatedAt = v.PublishedAt
		}
	}
	return c, true, nil
}

// Tombstoned reads the tombstone of a number.
func (s *Store) Tombstoned(_ context.Context, repoID int64, name, ver string) ([]byte, bool, error) {
	defer s.lock()()
	t, ok := s.tombstones[key{repoID, name, version.Number(ver)}]
	return t.SHA256[:], ok, nil
}

// ClearTombstone deletes a tombstone.
func (s *Store) ClearTombstone(_ context.Context, repoID int64, name, ver string, a rows.Audit) (bool, error) {
	defer s.lock()()
	k := key{repoID, name, version.Number(ver)}
	_, ok := s.tombstones[k]
	delete(s.tombstones, k)
	if ok {
		s.Audits = append(s.Audits, a)
	}
	return ok, nil
}

// Tombstones lists the tombstones after the cursor.
func (s *Store) Tombstones(_ context.Context, repoID int64, after string, limit int) ([]rows.Tombstone, error) {
	defer s.lock()()
	var out []rows.Tombstone
	for k, t := range s.tombstones {
		if k.repo == repoID && k.name+"/"+k.version > after {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+"/"+out[i].Version < out[j].Name+"/"+out[j].Version })
	return out[:min(limit, len(out))], nil
}

// CreateTask stores a task.
func (s *Store) CreateTask(_ context.Context, _ formats.Tx, t rows.Task, a rows.Audit) error {
	defer s.lock()()
	s.tasks = append(s.tasks, t)
	s.Audits = append(s.Audits, a)
	return nil
}

// Task reads a task.
func (s *Store) Task(_ context.Context, repoID int64, id string) (rows.Task, bool, error) {
	defer s.lock()()
	for _, t := range s.tasks {
		if t.ID == id && t.RepoID == repoID {
			return t, true, nil
		}
	}
	return rows.Task{}, false, nil
}

// Tasks lists the tasks newest first.
func (s *Store) Tasks(_ context.Context, repoID int64, f rows.TaskFilter, after string, limit int) ([]rows.Task, error) {
	defer s.lock()()
	var out []rows.Task
	for i := len(s.tasks) - 1; i >= 0; i-- {
		t := s.tasks[i]
		if t.RepoID == repoID && (f.State == "" || t.State == f.State) && (f.Name == "" || t.Namespace+"."+t.Name == f.Name) && (f.Version == "" || t.Version == f.Version) {
			out = append(out, t)
		}
	}
	if after != "" {
		for i, t := range out {
			if t.ID == after {
				out = out[i+1:]
				break
			}
		}
	}
	return out[:min(limit, len(out))], nil
}

// RebuildRows does nothing: the reindex is tested against PostgreSQL.
func (s *Store) RebuildRows(context.Context, int64, rows.ArchiveReader, string) (string, bool, error) {
	return "", true, nil
}

// RecordAnnounced records nothing: the remote class is not under test here.
func (s *Store) RecordAnnounced(context.Context, formats.Tx, int64, []rows.Announced) ([]rows.Announced, error) {
	return nil, nil
}

// Announced finds nothing.
func (s *Store) Announced(context.Context, int64, string, string) (rows.Announced, bool, error) {
	return rows.Announced{}, false, nil
}

// AnnouncedNames lists nothing.
func (s *Store) AnnouncedNames(context.Context, int64, string) ([]rows.Announced, error) {
	return nil, nil
}

// ViewSummaries returns nothing: the twin has no generic version ids to key a summary by; the screens' statements are tested against
// PostgreSQL.
func (s *Store) ViewSummaries(context.Context, []int64) ([]rows.ViewSummary, error) { return nil, nil }

// ViewDetails returns nothing, for the same reason.
func (s *Store) ViewDetails(context.Context, []int64, bool) ([]rows.ViewDetail, error) {
	return nil, nil
}

// ViewFind finds nothing.
func (s *Store) ViewFind(context.Context, []string, string, string, int) ([]int64, error) {
	return nil, nil
}

// ViewStored lists the stored versions newest first.
func (s *Store) ViewStored(_ context.Context, repoID int64, name string, limit int) ([]string, error) {
	defer s.lock()()
	var out []string
	for _, v := range s.sorted(repoID, name) {
		out = append(out, v.Version)
	}
	return out[:min(limit, len(out))], nil
}

// ViewWithheld lists nothing.
func (s *Store) ViewWithheld(context.Context, []string) ([]int64, error) { return nil, nil }
