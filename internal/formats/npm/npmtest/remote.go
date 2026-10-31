package npmtest

import (
	"context"
	"sort"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// remoteRec is the record of a stored upstream document and the facts of its versions
// (npm_remote_packages and npm_remote_versions).
type remoteRec struct {
	fill     rows.RemoteFill
	versions map[string]*remoteVersion
}

type remoteVersion struct {
	rows.RemoteVersion
	listed bool
}

// RemoteState is what a test sets for TarballFacts: the state of the package document's
// cache entry and the repository's setting, which the database reads from the cache
// entry and the repository row.
type RemoteState struct {
	DocStored, DocCurrent, ServeRemoved bool
	DocGone                             bool
}

// ApplyFill records a fill, with the stale-copy rule of the statements.
func (s *Store) ApplyFill(_ context.Context, _ formats.Tx, f rows.RemoteFill) (rows.RemoteApplied, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote == nil {
		s.remote = map[string]*remoteRec{}
	}
	k := pkgKey(f.RepoID, f.Name)
	cur := s.remote[k]
	var out rows.RemoteApplied
	if cur != nil && !s.remoteState.DocGone {
		ordered := !f.DocModified.IsZero() && !cur.fill.DocModified.IsZero()
		switch {
		case ordered && f.DocModified.Before(cur.fill.DocModified):
			return rows.RemoteApplied{Stale: true}, nil
		case ordered && f.DocModified.After(cur.fill.DocModified):
			out.Newer = true
		case !f.FetchedAt.IsZero() && cur.fill.FetchedAt.After(f.FetchedAt):
			return rows.RemoteApplied{Superseded: true}, nil
		}
	}
	if cur == nil {
		cur = &remoteRec{versions: map[string]*remoteVersion{}}
		s.remote[k] = cur
	}
	cur.fill = f
	listed := map[string]bool{}
	for _, v := range f.Versions {
		listed[v.Version] = true
	}
	for name, v := range cur.versions {
		if v.listed && !listed[name] {
			v.listed = false
			out.Removed = append(out.Removed, rows.RemoteVersion{Version: name, Integrity: v.Integrity})
		}
	}
	sort.Slice(out.Removed, func(i, j int) bool { return out.Removed[i].Version < out.Removed[j].Version })
	for _, v := range f.Versions {
		if rec, ok := cur.versions[v.Version]; ok {
			if !rec.listed {
				out.Relisted = append(out.Relisted, v.Version)
			}
			rec.RemoteVersion, rec.listed = v, true
			continue
		}
		cur.versions[v.Version] = &remoteVersion{RemoteVersion: v, listed: true}
	}
	sort.Strings(out.Relisted)
	s.Fills = append(s.Fills, f)
	return out, nil
}

// SetRemoteState sets what TarballFacts reports of the document's entry.
func (s *Store) SetRemoteState(st RemoteState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteState = st
}

// TarballFacts reads the record of a version.
func (s *Store) TarballFacts(_ context.Context, repo int64, name, version string) (rows.TarballFacts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := rows.TarballFacts{
		DocStored: s.remoteState.DocStored, DocCurrent: s.remoteState.DocCurrent, ServeRemoved: s.remoteState.ServeRemoved,
	}
	if s.remoteState.DocGone {
		out.DocGone = s.now()
	}
	if rec := s.remote[pkgKey(repo, name)]; rec != nil {
		if v := rec.versions[version]; v != nil {
			out.Known, out.Listed, out.Integrity, out.Shasum = true, v.listed, v.Integrity, v.Shasum
		}
	}
	return out, nil
}

// AuditFacts are what AuditNames reports beyond the twin's own rows, which the database
// reads from the claims, the pin rules and every local repository of the installation.
type AuditFacts struct {
	Local, Claimed, Pinned map[string]bool
}

// SetAuditFacts sets the names AuditNames reports local, claimed and pinned.
func (s *Store) SetAuditFacts(f AuditFacts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = f
	s.AuditQueries = 0
}

// AuditNames reports the names: proxied when ApplyFill recorded a document of the name in
// the remote, local when a package of that name was published here or AuditFacts says so.
// Every call counts in AuditQueries, the statements the database would run.
func (s *Store) AuditNames(_ context.Context, remoteID, virtualID int64, names []string) ([]rows.AuditName, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AuditQueries++
	local := map[string]bool{}
	for _, p := range s.pkgs {
		local[p.name] = true
	}
	seen := map[string]bool{}
	var out []rows.AuditName
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, rows.AuditName{
			Name: n, Proxied: s.remote[pkgKey(remoteID, n)] != nil, Local: local[n] || s.audit.Local[n],
			Claimed: s.audit.Claimed[n], Pinned: virtualID != 0 && s.audit.Pinned[n],
		})
	}
	return out, nil
}
