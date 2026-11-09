package deb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/index"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/repository"
)

// The media types of the generated files (R-DEB-005).
const (
	mediaRelease   = "text/plain; charset=utf-8"
	mediaSignature = "application/pgp-signature"
	mediaGzip      = "application/gzip"
	mediaXZ        = "application/x-xz"
)

// ReasonNoSigningKey is the reason of a suite whose build found no usable primary key (R-DEB-016).
const ReasonNoSigningKey = "no_signing_key"

// classLocal is the class of a repository whose publication triggers the builds of the virtual repositories that list it.
const classLocal = "local"

// maxDateWaits bounds the loop that waits for the clock: a Release Date is never later than the database time minus 59 s, so the wait
// is at most a minute and a half for a key bound a moment ago (Q-DEB-40).
const maxDateWaits = 4

// IndexRepo is what a build needs of its repository: the reference the publication port writes to and the settings of its indexes.
type IndexRepo struct {
	Ref items.RepoRef
	// Project and Key are the repository's names; Settings are the Release fields and the compression (R-DEB-008).
	Project, Key string
	Settings     index.Settings
}

// Repos looks a repository up for a build. found is false for a repository that no longer exists or is no local Debian repository,
// which ends the job quietly.
type Repos interface {
	IndexRepo(ctx context.Context, id int64) (IndexRepo, bool, error)
}

// Indexer builds and publishes the indexes of a local repository (R-DEB-010): the handler of the job deb-index.
type Indexer struct {
	// Store holds the rows and the keys; Publisher is the publication port of the item store (Q-STOR-182), asked when a build runs
	// because the item store is built after the data port.
	Store     *Store
	Publisher func() items.Publisher
	Repos     Repos
	// ByHashKeep is deb.by_hash_keep.
	ByHashKeep time.Duration
	Log        *slog.Logger
	// Sleep waits for d or until ctx ends; nil waits on a timer. A test replaces it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Virtual finds a virtual repository and the members that serve a name; Local reads the index files of a local member; Remote gives a
	// remote member's release and index through the background path. Without Virtual the indexer builds local repositories only.
	Virtual VirtualRepos
	Local   MemberIndexes
	Remote  *RemoteJobs
	// VirtualInterval is deb.virtual_index_interval: the least time between two builds of a virtual repository that a member's
	// publication triggers (R-DEB-026).
	VirtualInterval time.Duration
	// Observe receives the measurements of R-DEB-033; nil records none.
	Observe Observer
	// BeforeCommit, if set, runs after the blobs are written and before the commit's transaction would run (fault injection of the
	// tests, R-DEB-010): an error stops the build there.
	BeforeCommit func(suite string) error
}

// The outcomes of a build of a suite (R-DEB-033).
const (
	OutcomePublished = "published"
	OutcomeUnchanged = "unchanged"
	OutcomeFailed    = "failed"
)

// Observer receives what the metrics of R-DEB-033 count: trove_deb_index_builds_total, trove_deb_index_build_seconds and
// trove_deb_index_delay_seconds, each labelled with the class of the repository (local or virtual).
type Observer interface {
	// Build is one build of one suite of a repository of class and its outcome; Seconds the time it took.
	Build(class, outcome string, seconds float64)
	// Delay is the time from the first change a published generation covers to its commit.
	Delay(class string, seconds float64)
}

func (x *Indexer) built(class, outcome string, started time.Time) {
	if x.Observe != nil {
		x.Observe.Build(class, outcome, time.Since(started).Seconds())
	}
}

var bound atomic.Pointer[Indexer]

// Bound returns the Indexer the job runs, or nil. The integration tests set its fault injection through it.
func Bound() *Indexer { return bound.Load() }

// BindIndexer sets the Indexer the job deb-index runs. The composition root calls it on a replica that claims jobs; a process that
// binds none fails the job permanently.
func BindIndexer(x *Indexer) { bound.Store(x) }

// RunIndexJob is the handler of the job deb-index (R-DEB-010): one build of the indexes of a local repository from the rows of a
// snapshot, signed and published through the publication port.
func RunIndexJob(ctx context.Context, run *jobs.Run) error {
	x := bound.Load()
	if x == nil {
		return jobs.Permanent(errors.New("deb: this process builds no indexes"))
	}
	var args IndexJobArgs
	if len(run.Args) > 0 {
		if err := json.Unmarshal(run.Args, &args); err != nil {
			return jobs.Permanent(err)
		}
	}
	repoID, err := strconv.ParseInt(run.TargetKey, 10, 64)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("deb: the job names no repository: %w", err))
	}
	return x.Build(ctx, repoID, args)
}

func (x *Indexer) sleep(ctx context.Context, d time.Duration) error {
	if x.Sleep != nil {
		return x.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (x *Indexer) log() *slog.Logger {
	if x.Log != nil {
		return x.Log
	}
	return slog.Default()
}

// snapshotInputs reads the suites a build is for in one REPEATABLE READ snapshot and holds no transaction afterwards.
func (x *Indexer) snapshotInputs(ctx context.Context, repoID int64, full bool) (rows.BuildInputs, error) {
	var in rows.BuildInputs
	err := x.Store.buildStore.o.DB.InTx(ctx, func(tx meta.Tx) (err error) {
		in, err = x.Store.GenerationInputs(ctx, tx, repoID, full)
		return err
	})
	return in, err
}

func (x *Indexer) databaseNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := x.Store.buildStore.read(ctx, nil, func(qs *q.Queries) error {
		t, err := qs.DebDatabaseNow(ctx)
		now = time.UnixMicro(t).UTC()
		return err
	})
	return now, err
}

// Build runs one build: the suites with changes (every suite when full) are rendered, signed and published, each in one transaction
// of its own (R-DEB-010). A suite whose build fails keeps its generation and gets the state failed with the reason; the first failure
// is returned so that the job is retried. A repository without a usable primary key publishes nothing and fails its suites with
// no_signing_key, which a key change or a restore's replay fixes by enqueueing a build again (R-DEB-016).
func (x *Indexer) Build(ctx context.Context, repoID int64, args IndexJobArgs) error {
	args = args.normalized()
	if x.Virtual != nil {
		vr, ok, err := x.Virtual.Virtual(ctx, repoID)
		if err != nil {
			return err
		}
		if ok {
			return x.buildVirtual(ctx, vr, args)
		}
	}
	repo, found, err := x.Repos.IndexRepo(ctx, repoID)
	if err != nil || !found {
		return err
	}
	in, err := x.snapshotInputs(ctx, repoID, args.Full)
	if err != nil || len(in.Suites) == 0 {
		if err == nil {
			x.built(repo.Ref.Class, OutcomeUnchanged, time.Now())
		}
		return err
	}
	keys, err := x.openKeys(ctx, repoID, in.Now)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		for _, s := range in.Suites {
			if ferr := x.Store.FailBuild(ctx, rows.BuildFailure{RepoID: repoID, Suite: s.Suite.Suite, ReadSeq: s.Suite.ChangeSeq, Reason: ReasonNoSigningKey}); ferr != nil {
				return ferr
			}
		}
		x.log().Warn("the indexes of a repository were not built: it has no primary signing key", "repository", repo.Project+"/"+repo.Key)
		return nil
	}
	var first error
	for _, s := range in.Suites {
		started := time.Now()
		outcome, err := x.buildSuite(ctx, repo, s, keys, args.Full)
		x.built(repo.Ref.Class, outcome, started)
		if err != nil {
			x.log().Error("the build of a suite failed", "repository", repo.Project+"/"+repo.Key, "suite", s.Suite.Suite, "error", err)
			if ferr := x.Store.FailBuild(ctx, rows.BuildFailure{RepoID: repoID, Suite: s.Suite.Suite, ReadSeq: s.Suite.ChangeSeq, Reason: reasonOf(err)}); ferr != nil {
				err = errors.Join(err, ferr)
			}
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func reasonOf(err error) string {
	var me *memberError
	switch {
	case errors.As(err, &me):
		return me.reason + ": " + me.member

	case errors.Is(err, openpgp.ErrTooEarly):
		return "key_too_young"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	}
	return "build_error"
}

// openKeys opens the secrets of the keys that sign. The secrets are cleared as soon as the keys are parsed.
func (x *Indexer) openKeys(ctx context.Context, repoID int64, now time.Time) ([]*openpgp.Key, error) {
	signing, err := x.Store.SigningKeys(ctx, repoID)
	if err != nil {
		return nil, err
	}
	var out []*openpgp.Key
	for _, sk := range signing {
		k, err := openpgp.ParseSecret(sk.Secret, nil, now, openpgp.DefaultLimits())
		clear(sk.Secret)
		if err != nil {
			return nil, fmt.Errorf("deb: a signing key cannot be read back: %w", err)
		}
		out = append(out, k)
	}
	return out, nil
}

// earliestOf is the first time every key can sign (Q-DEB-40).
func earliestOf(keys []*openpgp.Key) time.Time {
	var t time.Time
	for _, k := range keys {
		if e := k.EarliestSignature(); e.After(t) {
			t = e
		}
	}
	return t
}

// chooseDate picks the Release Date, waiting for the database clock when the rules ask for a date that would run ahead of it
// (Q-DEB-30). The wait holds no transaction.
func (x *Indexer) chooseDate(ctx context.Context, now time.Time, previous *time.Time, earliest time.Time) (time.Time, error) {
	for i := 0; i < maxDateWaits; i++ {
		date, wait := index.PickDate(now, previous, earliest)
		if wait <= 0 {
			return date, nil
		}
		if err := x.sleep(ctx, wait+10*time.Millisecond); err != nil {
			return time.Time{}, err
		}
		var err error
		if now, err = x.databaseNow(ctx); err != nil {
			return time.Time{}, err
		}
	}
	return time.Time{}, errors.New("deb: the clock of the database does not reach the time a Release may be dated")
}

func componentsOf(files map[string]string) []string {
	var out []string
	for p := range files {
		if c, _, ok := strings.Cut(p, "/binary-"); ok && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

func generationsOf(rs []rows.GenerationRow) []index.Generation {
	out := make([]index.Generation, 0, len(rs)+1)
	for _, g := range rs {
		out = append(out, index.Generation{Number: g.Generation, Files: g.Files, SupersededAt: g.SupersededAt})
	}
	return out
}

func mediaOf(path string) string {
	switch {
	case strings.HasSuffix(path, ".xz"):
		return mediaXZ
	case strings.HasSuffix(path, ".gz"):
		return mediaGzip
	}
	return mediaRelease
}

func generated(path string, body []byte, media string, at time.Time) items.GeneratedFile {
	return items.GeneratedFile{Path: repository.Cleaned{Path: path}, Body: bytes.NewReader(body), Size: int64(len(body)), MediaType: media, ModifiedAt: at}
}

// buildSuite renders, signs and publishes one suite (R-DEB-010, R-DEB-016). The blobs are written by the port before its transaction;
// nothing is held while this function renders, compresses, signs or waits.
func (x *Indexer) buildSuite(ctx context.Context, repo IndexRepo, bs rows.BuildSuite, keys []*openpgp.Key, full bool) (string, error) {
	outcome, err := x.publishSuite(ctx, repo, bs, keys, full)
	if err != nil {
		return OutcomeFailed, err
	}
	return outcome, nil
}

func (x *Indexer) publishSuite(ctx context.Context, repo IndexRepo, bs rows.BuildSuite, keys []*openpgp.Key, full bool) (string, error) {
	suite := bs.Suite.Suite
	gens := generationsOf(bs.Generations)
	var current index.Generation
	for _, g := range gens {
		if g.SupersededAt == nil {
			current = g
		}
	}
	pub := x.Publisher()
	if pub == nil {
		return OutcomeFailed, errors.New("deb: the publication port is not available on this replica")
	}
	if len(bs.Entries) == 0 && full {
		return OutcomePublished, x.dropSuite(ctx, pub, repo, bs.Suite, gens)
	}
	date, err := x.chooseDate(ctx, bs.Now, bs.Suite.ReleaseDate, earliestOf(keys))
	if err != nil {
		return OutcomeFailed, err
	}
	set := repo.Settings
	set.Description = repo.Project + "/" + repo.Key + " " + suite
	built, err := index.Build(suite, bs.Entries, componentsOf(current.Files), set, date)
	if err != nil {
		return OutcomeFailed, err
	}
	return x.publishBuilt(ctx, pub, repo, bs.Suite, bs.Generations, bs.Now, built, date, keys, full, nil, bs.Suite.DirtySince, current.Files)
}

// publishBuilt signs a rendered suite and publishes it through the port in one transaction with the record of its generation (R-DEB-010,
// R-DEB-016). row is the suite's row as the snapshot read it, gens its generations, currentFiles the index files of the generation it
// replaces; inputs is what a virtual build compared (R-DEB-026). The blobs are written by the port before its transaction.
func (x *Indexer) publishBuilt(ctx context.Context, pub items.Publisher, repo IndexRepo, row rows.Suite, genRows []rows.GenerationRow, now time.Time,
	built *index.Suite, date time.Time, keys []*openpgp.Key, full bool, inputs json.RawMessage, dirty *time.Time, currentFiles map[string]string,
) (string, error) {
	suite := row.Suite
	gens := generationsOf(genRows)
	prefix := "dists/" + suite + "/"
	inRelease, err := openpgp.ClearSign(built.Release, keys, date)
	if err != nil {
		return OutcomeFailed, err
	}
	detached, err := openpgp.DetachSign(built.Release, keys, date)
	if err != nil {
		return OutcomeFailed, err
	}
	newFiles := make(map[string]string, len(built.Files))
	files := []items.GeneratedFile{
		generated(prefix+"Release", built.Release, mediaRelease, date),
		generated(prefix+"InRelease", inRelease, mediaRelease, date),
		generated(prefix+"Release.gpg", detached, mediaSignature, date),
	}
	for _, f := range built.Files {
		newFiles[f.Path] = f.SHA256
		files = append(files,
			generated(prefix+f.Path, f.Body, mediaOf(f.Path), date),
			generated(prefix+index.ByHashPath(f.Path, f.SHA256), f.Body, mediaOf(f.Path), date))
	}

	// Retention runs here and nowhere else (Q-DEB-33): the generation this build replaces is superseded now, the new one is current.
	next := row.Generation + 1
	all := make([]index.Generation, 0, len(gens)+1)
	for _, g := range gens {
		if g.SupersededAt == nil {
			t := now
			g.SupersededAt = &t
		}
		all = append(all, g)
	}
	all = append(all, index.Generation{Number: next, Files: newFiles})
	kept, dropped := index.Retain(all, now, x.ByHashKeep)
	given := index.GivenUp(kept, dropped, currentFiles, newFiles)
	remove := make([]repository.Cleaned, 0, len(given))
	for _, p := range given {
		remove = append(remove, repository.Cleaned{Path: prefix + p})
	}
	rec := rows.GenerationRecord{
		RepoID: row.RepoID, Suite: suite, ReadSeq: row.ChangeSeq, Generation: next, PrevGeneration: row.Generation, Full: full, Inputs: inputs,
		ReleaseDate: date, Files: newFiles,
		SignedBy: fingerprints(keys),
	}
	for _, g := range dropped {
		rec.DropGenerations = append(rec.DropGenerations, g.Number)
	}
	if x.BeforeCommit != nil {
		if err := x.BeforeCommit(suite); err != nil {
			return OutcomeFailed, err
		}
	}
	err = pub.Publish(ctx, repo.Ref, files, remove, func(ctx context.Context, tx meta.Tx) error {
		applied, err := x.Store.RecordGeneration(ctx, tx, rec)
		if err != nil {
			return err
		}
		if !applied {
			return items.ErrSuperseded
		}
		return nil
	})
	if errors.Is(err, items.ErrSuperseded) {
		x.log().Info("a newer build published the suite first", "repository", repo.Project+"/"+repo.Key, "suite", suite)
		return OutcomeUnchanged, nil
	}
	if err != nil {
		return OutcomeFailed, err
	}
	if x.Observe != nil && dirty != nil {
		if dbNow, nerr := x.databaseNow(ctx); nerr == nil {
			x.Observe.Delay(repo.Ref.Class, dbNow.Sub(*dirty).Seconds())
		}
	}
	if repo.Ref.Class == classLocal {
		x.triggerVirtuals(ctx, repo)
	}
	return OutcomePublished, nil
}

// dropSuite removes a suite that has no files from a full build: its generated items and its rows (R-DEB-014).
func (x *Indexer) dropSuite(ctx context.Context, pub items.Publisher, repo IndexRepo, row rows.Suite, gens []index.Generation) error {
	suite := row.Suite
	prefix := "dists/" + suite + "/"
	paths := map[string]bool{"Release": true, "InRelease": true, "Release.gpg": true}
	for _, g := range gens {
		for file, sum := range g.Files {
			paths[file] = true
			paths[index.ByHashPath(file, sum)] = true
		}
	}
	remove := make([]repository.Cleaned, 0, len(paths))
	for p := range paths {
		remove = append(remove, repository.Cleaned{Path: prefix + p})
	}
	slices.SortFunc(remove, func(a, b repository.Cleaned) int { return strings.Compare(a.Path, b.Path) })
	rec := rows.GenerationRecord{RepoID: row.RepoID, Suite: suite, ReadSeq: row.ChangeSeq, Drop: true}
	err := pub.Publish(ctx, repo.Ref, nil, remove, func(ctx context.Context, tx meta.Tx) error {
		_, err := x.Store.RecordGeneration(ctx, tx, rec)
		return err
	})
	if errors.Is(err, items.ErrSuperseded) {
		return nil
	}
	return err
}

func fingerprints(keys []*openpgp.Key) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Fingerprint)
	}
	return out
}
