package ansible

import (
	"context"
	"errors"
	"strconv"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The reindex of a local repository (R-ANS-012, Q-ANS-26, Q-ANS-32): the job ansible-reindex reads the manifest of every stored archive
// and rewrites the version rows from it, creates the rows of archives that have none (an import by copy, a restore) and raises the
// revision of the collections it changed. It checkpoints by archive path and never changes stored bytes.

// Jobs enqueues a job in the caller's transaction; *jobs.Queue implements it.
type Jobs interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
	// Running returns the job that runs under a dedup key.
	Running(ctx context.Context, tx meta.Tx, key string) (jobs.Job, bool, error)
}

// ReindexDedupKey is the dedup key of the reindex job of a repository: "index:<repository id>", the key R-REPO-016 uses.
func ReindexDedupKey(repoID int64) string { return "index:" + strconv.FormatInt(repoID, 10) }

// Reindex enqueues the reindex the endpoint asks for (R-REPO-016), in the caller's transaction. It implements api/v1.Reindexer. A
// reindex that waits or runs enqueues nothing and is reported as the conflict with that job.
func (s *Store) Reindex(ctx context.Context, tx meta.Tx, repoID int64, by string) (job jobs.Job, conflict bool, err error) {
	if s.o.Jobs == nil {
		return jobs.Job{}, false, errNotBuilt("the reindex queue")
	}
	key := ReindexDedupKey(repoID)
	if running, ok, err := s.o.Jobs.Running(ctx, tx, key); err != nil {
		return jobs.Job{}, false, err
	} else if ok {
		return running, true, nil
	}
	id := strconv.FormatInt(repoID, 10)
	res, err := s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: ReindexJobKind, DedupKey: key, TargetKind: "repository", TargetKey: id, CreatedBy: by})
	if err != nil {
		return jobs.Job{}, false, err
	}
	return res.Job, !res.Created, nil
}

// reindexBatch is the number of archives one step reads.
const reindexBatch = 50

// RebuildResult counts what a step of the reindex did.
type RebuildResult struct {
	// Read, Created and Rewritten count the archives read, the rows created and the rows rewritten.
	Read, Created, Rewritten int
	// Skipped counts the archives left alone: a tombstoned number, a path that is not the canonical path of the archive's identity and
	// an archive the reader refused. They are findings of the repository, not failures of the job.
	Skipped int
	// Next is the checkpoint to continue from and Done says the step reached the last archive.
	Next string
	Done bool
}

// RebuildRows is one step of the job (the LocalStore method): see [Store.RebuildStep].
func (s localStore) RebuildRows(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (string, bool, error) {
	res, err := s.RebuildStep(ctx, repoID, reader, checkpoint)
	return res.Next, res.Done, err
}

// RebuildStep reads the next archives after the checkpoint and rewrites their rows. Each archive is one transaction of its own, so a
// drain between two leaves rows that are all current.
func (s localStore) RebuildStep(ctx context.Context, repoID int64, reader rows.ArchiveReader, checkpoint string) (RebuildResult, error) {
	var list []q.AnsibleArchivesAfterRow
	if err := s.read(ctx, nil, func(qs *q.Queries) (err error) {
		list, err = qs.AnsibleArchivesAfter(ctx, q.AnsibleArchivesAfterParams{RepoID: repoID, After: checkpoint, RowLimit: reindexBatch})
		return err
	}); err != nil {
		return RebuildResult{}, err
	}
	res := RebuildResult{Next: checkpoint, Done: len(list) < reindexBatch}
	for _, a := range list {
		res.Next = a.Path
		res.Read++
		facts, err := reader.ReadArchive(ctx, rows.ArchiveRef{ItemID: a.ItemID, Path: a.Path, SHA256: digestOf(a.Sha256), Size: a.Size})
		if err != nil {
			var ce *collection.Error
			if errors.As(err, &ce) {
				res.Skipped++
				continue
			}
			return res, err
		}
		name := facts.Namespace + "." + facts.Name
		sv, verr := version.Parse(facts.Version)
		if verr != nil || a.Path != "collections/"+facts.Namespace+"/"+facts.Name+"/"+collection.FileName(facts.Namespace, facts.Name, facts.Version) {
			res.Skipped++
			continue
		}
		created, rewritten, skipped, err := s.rebuildOne(ctx, repoID, a, name, facts, sv)
		if err != nil {
			return res, err
		}
		switch {
		case skipped:
			res.Skipped++
		case created:
			res.Created++
		case rewritten:
			res.Rewritten++
		}
	}
	return res, nil
}

func (s localStore) rebuildOne(ctx context.Context, repoID int64, a q.AnsibleArchivesAfterRow, name string, facts rows.ArchiveFacts, sv version.Semver) (created, rewritten, skipped bool, err error) {
	err = s.o.DB.InTx(ctx, func(tx meta.Tx) error {
		created, rewritten, skipped = false, false, false
		qs, err := txQueries(tx)
		if err != nil {
			return err
		}
		if t, err := qs.AnsibleGetTombstone(ctx, q.AnsibleGetTombstoneParams{RepoID: repoID, Name: name, Version: version.Number(facts.Version)}); err != nil {
			return err
		} else if len(t) > 0 {
			skipped = true // a deleted number stays withheld, whatever archive is left behind (R-ANS-029)
			return nil
		}
		deps, err := jsonOf(facts.Dependencies)
		if err != nil {
			return err
		}
		info := facts.Info
		if len(info) == 0 {
			info = []byte(`{}`)
		}
		if a.HasRow {
			n, err := qs.AnsibleRewriteVersion(ctx, q.AnsibleRewriteVersionParams{
				SortKey: version.SortKey(sv), RequiresAnsible: facts.RequiresAnsible, Dependencies: deps, CollectionInfo: info, ItemID: a.ItemID,
			})
			if err != nil || n == 0 {
				return err
			}
			rewritten = true
		} else {
			if err := qs.AnsibleInsertVersionFromItem(ctx, q.AnsibleInsertVersionFromItemParams{
				Name: name, Version: facts.Version, SortKey: version.SortKey(sv), RequiresAnsible: facts.RequiresAnsible, Dependencies: deps,
				CollectionInfo: info, ItemID: a.ItemID,
			}); err != nil {
				return err
			}
			created = true
		}
		_, err = qs.AnsibleBumpPackage(ctx, q.AnsibleBumpPackageParams{RepoID: repoID, Name: name})
		return err
	})
	return created, rewritten, skipped, err
}

// reindexCheckpoint is the state of the job a drain resumes from.
type reindexCheckpoint struct {
	After string `json:"after"`
	// RowsDone says the version rows are rebuilt; DocsAfter is the checkpoint of the documentation step that follows (R-ANS-044).
	RowsDone  bool   `json:"rowsDone,omitempty"`
	DocsAfter string `json:"docsAfter,omitempty"`
}

// ReindexHandler is the handler of the job ansible-reindex: it reads the repository named by the job's target and steps through its
// archives with reader until the last (R-ANS-012). A job for a repository that is not a local Ansible Galaxy repository (a virtual one,
// whose reindex only drops its merged documents) has nothing to rebuild.
func ReindexHandler(st *Store, reader rows.ArchiveReader) jobs.Handler {
	return func(ctx context.Context, run *jobs.Run) error {
		repoID, err := strconv.ParseInt(run.TargetKey, 10, 64)
		if err != nil {
			return jobs.Permanent(errNotBuilt("a reindex without a repository"))
		}
		repos, err := q.New(run.DB()).AnsibleReplayRepository(ctx, repoID)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return nil
		}
		var cp reindexCheckpoint
		if _, err := run.LoadCheckpoint(&cp); err != nil {
			return err
		}
		var total RebuildResult
		for !cp.RowsDone {
			res, err := st.RebuildStep(ctx, repoID, reader, cp.After)
			total.Read += res.Read
			total.Created += res.Created
			total.Rewritten += res.Rewritten
			total.Skipped += res.Skipped
			if err != nil {
				return err
			}
			cp.After, cp.RowsDone = res.Next, res.Done
			if err := run.SaveCheckpoint(ctx, cp); err != nil {
				return err
			}
		}
		for {
			next, done, err := st.RebuildDocs(ctx, repoID, reader, cp.DocsAfter)
			if err != nil {
				return err
			}
			cp.DocsAfter = next
			if err := run.SaveCheckpoint(ctx, cp); err != nil {
				return err
			}
			if done {
				break
			}
		}
		return run.SetResult(map[string]int{"read": total.Read, "created": total.Created, "rewritten": total.Rewritten, "skipped": total.Skipped})
	}
}
