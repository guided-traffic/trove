package deb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// virtualStore is the group of the data port that reads and records the inputs of a virtual repository's builds (R-DEB-025, R-DEB-026).
type virtualStore struct{ *group }

// VirtualInputs reads, in the build's snapshot, the suites of a virtual repository with their generations and the published generations
// of its local members. tx must be new: the isolation level is set here.
func (s virtualStore) VirtualInputs(ctx context.Context, tx meta.Tx, repoID int64, localMembers []int64) (rows.VirtualInputs, error) {
	var out rows.VirtualInputs
	if err := snapshot(ctx, tx); err != nil {
		return out, err
	}
	qs, err := txQueries(tx)
	if err != nil {
		return out, err
	}
	now, err := qs.DebDatabaseNow(ctx)
	if err != nil {
		return out, err
	}
	out.Now = time.UnixMicro(now).UTC()
	suites, err := qs.DebVirtualSuiteRows(ctx, repoID)
	if err != nil {
		return out, err
	}
	names := make([]string, 0, len(suites))
	for _, r := range suites {
		names = append(names, r.Suite)
	}
	gens, err := qs.DebGenerationsOf(ctx, q.DebGenerationsOfParams{RepoID: repoID, Suites: names})
	if err != nil {
		return out, err
	}
	for _, r := range suites {
		vs := rows.VirtualSuite{Suite: rows.Suite{
			RepoID: repoID, Suite: r.Suite, ChangeSeq: r.ChangeSeq, BuiltSeq: r.BuiltSeq, Generation: r.Generation,
			ReleaseDate: microsTime(r.ReleaseUs), CheckedAt: microsTime(r.CheckedUs), Inputs: r.Inputs,
		}}
		for _, g := range gens {
			if g.Suite != r.Suite {
				continue
			}
			files := map[string]string{}
			if err := json.Unmarshal(g.Files, &files); err != nil {
				return out, err
			}
			vs.Generations = append(vs.Generations, rows.GenerationRow{Generation: g.Generation, SupersededAt: microsTime(g.SupersededUs), Files: files})
		}
		out.Suites = append(out.Suites, vs)
	}
	out.Locals = map[int64][]rows.LocalSuite{}
	if len(localMembers) == 0 {
		return out, nil
	}
	locals, err := qs.DebLocalMemberSuites(ctx, localMembers)
	if err != nil {
		return out, err
	}
	for _, l := range locals {
		files := map[string]string{}
		if err := json.Unmarshal(l.Files, &files); err != nil {
			return out, err
		}
		out.Locals[l.RepoID] = append(out.Locals[l.RepoID], rows.LocalSuite{Suite: l.Suite, Generation: l.Generation, Date: microsTime(l.ReleaseUs), Files: files})
	}
	return out, nil
}

// EnsureSuites creates the suite rows of a virtual repository that it does not have.
func (s virtualStore) EnsureSuites(ctx context.Context, repoID int64, suites []string) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		for _, suite := range suites {
			if _, err := qs.DebEnsureSuite(ctx, q.DebEnsureSuiteParams{RepoID: repoID, Suite: suite}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordCheck records that a build checked its members without a change: the inputs it compared and the time (R-DEB-026).
func (s virtualStore) RecordCheck(ctx context.Context, tx meta.Tx, repoID int64, suite string, inputs json.RawMessage, at time.Time) error {
	qs, err := txQueries(tx)
	if err != nil {
		return err
	}
	return qs.DebRecordCheck(ctx, q.DebRecordCheckParams{RepoID: repoID, Suite: suite, Inputs: inputs, CheckedUnix: at.UTC().Unix()})
}

// VirtualsOfMember lists the virtual Debian repositories that list the repository as a member.
func (s virtualStore) VirtualsOfMember(ctx context.Context, memberID int64) ([]int64, error) {
	var out []int64
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		out, err = qs.DebVirtualsOfMember(ctx, memberID)
		return err
	})
	return out, err
}

// LastBuild is the time of the last build or check of a virtual repository.
func (s virtualStore) LastBuild(ctx context.Context, repoID int64) (time.Time, error) {
	var us int64
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		us, err = qs.DebVirtualLastBuild(ctx, repoID)
		return err
	})
	if err != nil || us == 0 {
		return time.Time{}, err
	}
	return time.UnixMicro(us).UTC(), nil
}

// Repositories reads the key, class and upstream of repositories by id.
func (s virtualStore) Repositories(ctx context.Context, ids []int64) ([]rows.RepositoryMeta, error) {
	var out []rows.RepositoryMeta
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.DebRepositoryMeta(ctx, ids)
		if err != nil {
			return err
		}
		for _, r := range list {
			out = append(out, rows.RepositoryMeta{ID: r.ID, Project: r.Project, Key: r.Key, Class: r.Class, Format: r.Format, URL: r.Url})
		}
		return nil
	})
	return out, err
}

// ClaimsFingerprint is a digest of the claim revisions of the Debian repositories.
func (s virtualStore) ClaimsFingerprint(ctx context.Context) (string, error) {
	var out string
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		out, err = qs.DebClaimsFingerprint(ctx)
		return err
	})
	return out, err
}
