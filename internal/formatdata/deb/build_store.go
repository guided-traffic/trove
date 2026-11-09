package deb

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// buildStore is the group of the data port that reads and writes the suites and generations of the index builds (R-DEB-010 to R-DEB-014).
type buildStore struct{ *group }

// The states of the index state read (R-DEB-014): a suite with changes no build has published is pending, whatever its row says, unless
// its last build failed.
const (
	statePending = "pending"
	stateCurrent = "current"
	stateFailed  = "failed"
)

// pollEvery is how often the wait of an index state read looks at the suite row (R-DEB-014).
const pollEvery = 250 * time.Millisecond

// microsTime reads a time the statements return as microseconds since the epoch; 0 is no time.
func microsTime(us int64) *time.Time {
	if us == 0 {
		return nil
	}
	v := time.UnixMicro(us).UTC()
	return &v
}

func suiteOf(repoID int64, suite string, change, built, generation int64, releaseUS, publishedUS int64, state, reason string, signedBy []string) rows.Suite {
	s := rows.Suite{
		RepoID: repoID, Suite: suite, ChangeSeq: change, BuiltSeq: built, Generation: generation,
		ReleaseDate: microsTime(releaseUS), PublishedAt: microsTime(publishedUS), State: state, Reason: reason, SignedBy: signedBy,
	}
	if s.State != stateFailed {
		s.State = stateCurrent
		if change > built {
			s.State = statePending
		}
	}
	return s
}

// Suites lists the suites of a repository with their state: the index state read of R-DEB-014.
func (s buildStore) Suites(ctx context.Context, repoID int64) ([]rows.Suite, error) {
	var out []rows.Suite
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.DebSuiteStates(ctx, repoID)
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range list {
			out = append(out, suiteOf(repoID, r.Suite, r.ChangeSeq, r.BuiltSeq, r.Generation, r.ReleaseUs, r.PublishedUs, r.State, r.Reason, r.SignedBy))
		}
		return nil
	})
	return out, err
}

func (s buildStore) suite(ctx context.Context, repoID int64, suite string) (rows.Suite, bool, error) {
	var out rows.Suite
	found := false
	err := s.read(ctx, nil, func(qs *q.Queries) error {
		list, err := qs.DebSuiteState(ctx, q.DebSuiteStateParams{RepoID: repoID, Suite: suite})
		if err != nil || len(list) == 0 {
			found = false
			return err
		}
		r := list[0]
		out, found = suiteOf(repoID, suite, r.ChangeSeq, r.BuiltSeq, r.Generation, r.ReleaseUs, r.PublishedUs, r.State, r.Reason, r.SignedBy), true
		return nil
	})
	return out, found, err
}

// WaitPublished returns the suite once its built_seq has reached seq, or after max with the state it has reached: the waitFor of
// R-DEB-014. It holds no transaction while it waits and reads the row at most every 250 ms.
func (s buildStore) WaitPublished(ctx context.Context, repoID int64, suite string, seq int64, maxWait time.Duration) (rows.Suite, bool, error) {
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		cur, found, err := s.suite(ctx, repoID, suite)
		if err != nil || !found || cur.BuiltSeq >= seq {
			return cur, found, err
		}
		select {
		case <-ctx.Done():
			return cur, true, ctx.Err()
		case <-deadline.C:
			return cur, true, nil
		case <-tick.C:
		}
	}
}

// snapshot makes the transaction a REPEATABLE READ one. It must run before the transaction's first query.
func snapshot(ctx context.Context, tx meta.Tx) error {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return errForeignTx
	}
	_, err := ptx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	return err
}

// GenerationInputs reads, in the build's snapshot tx, the suites whose change_seq exceeds their built_seq (every suite when full) with
// the paragraphs of their coordinates and their published generations (R-DEB-010). tx must be new: the isolation level is set here.
func (s buildStore) GenerationInputs(ctx context.Context, tx meta.Tx, repoID int64, full bool) (rows.BuildInputs, error) {
	var out rows.BuildInputs
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
	due, err := qs.DebDueSuites(ctx, q.DebDueSuitesParams{RepoID: repoID, Every: full})
	if err != nil || len(due) == 0 {
		return out, err
	}
	names := make([]string, 0, len(due))
	for _, d := range due {
		names = append(names, d.Suite)
	}
	entries, err := qs.DebBuildEntries(ctx, q.DebBuildEntriesParams{RepoID: repoID, Suites: names})
	if err != nil {
		return out, err
	}
	gens, err := qs.DebGenerationsOf(ctx, q.DebGenerationsOfParams{RepoID: repoID, Suites: names})
	if err != nil {
		return out, err
	}
	for _, d := range due {
		bs := rows.BuildSuite{
			Suite: rows.Suite{
				RepoID: repoID, Suite: d.Suite, ChangeSeq: d.ChangeSeq, BuiltSeq: d.BuiltSeq, Generation: d.Generation,
				ReleaseDate: microsTime(d.ReleaseUs),
			},
			Now: out.Now,
		}
		for _, e := range entries {
			if e.Suite == d.Suite {
				bs.Entries = append(bs.Entries, rows.IndexEntry{
					Suite: e.Suite, Component: e.Component, Architecture: e.Architecture, Package: e.Package, Version: e.Version,
					Control: e.Control, Filename: e.Filename, Size: e.Size, MD5: e.Md5, SHA256: e.Sha256,
				})
			}
		}
		for _, g := range gens {
			if g.Suite != d.Suite {
				continue
			}
			files := map[string]string{}
			if err := json.Unmarshal(g.Files, &files); err != nil {
				return out, err
			}
			bs.Generations = append(bs.Generations, rows.GenerationRow{Generation: g.Generation, SupersededAt: microsTime(g.SupersededUs), Files: files})
		}
		out.Suites = append(out.Suites, bs)
	}
	return out, nil
}

// RecordGeneration writes a build's own rows in the publication's transaction (R-DEB-010). applied is false when a newer build has
// published already.
func (s buildStore) RecordGeneration(ctx context.Context, tx meta.Tx, in rows.GenerationRecord) (bool, error) {
	qs, err := txQueries(tx)
	if err != nil {
		return false, err
	}
	if in.Drop {
		return true, qs.DebDropSuite(ctx, q.DebDropSuiteParams{RepoID: in.RepoID, Suite: in.Suite, ReadSeq: in.ReadSeq})
	}
	n, err := qs.DebApplyGeneration(ctx, q.DebApplyGenerationParams{
		RepoID: in.RepoID, Suite: in.Suite, ReadSeq: in.ReadSeq, Generation: in.Generation,
		ReleaseUnix: in.ReleaseDate.UTC().Unix(),
	})
	if err != nil || n == 0 {
		return false, err
	}
	files, err := json.Marshal(in.Files)
	if err != nil {
		return false, err
	}
	signed := slices.Clone(in.SignedBy)
	if signed == nil {
		signed = []string{}
	}
	if err := qs.DebInsertGeneration(ctx, q.DebInsertGenerationParams{
		RepoID: in.RepoID, Suite: in.Suite, Generation: in.Generation, BuiltSeq: in.ReadSeq, Files: files, SignedBy: signed,
	}); err != nil {
		return false, err
	}
	if err := qs.DebSupersedeGenerations(ctx, q.DebSupersedeGenerationsParams{RepoID: in.RepoID, Suite: in.Suite, Generation: in.Generation}); err != nil {
		return false, err
	}
	if len(in.DropGenerations) > 0 {
		if err := qs.DebDeleteGenerations(ctx, q.DebDeleteGenerationsParams{RepoID: in.RepoID, Suite: in.Suite, Generations: in.DropGenerations}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// FailBuild records that a build published nothing for a suite: the generation stays, the state says why (R-DEB-010).
func (s buildStore) FailBuild(ctx context.Context, in rows.BuildFailure) error {
	return s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		return qs.DebFailSuite(ctx, q.DebFailSuiteParams{RepoID: in.RepoID, Suite: in.Suite, ReadSeq: in.ReadSeq, Reason: in.Reason})
	})
}
