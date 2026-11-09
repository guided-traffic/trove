package deb

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// The bounds of the parse of an index (R-DEB-023, R-DEB-034): 1 GiB decompressed, 1,000,000 paragraphs, 1 MiB per field (the
// reader's own bound). The compressed file is a metadata document held in memory, bounded by the repository's metadata limit.
const (
	maxIndexUncompressed = 1 << 30
	maxIndexParagraphs   = 1_000_000
	maxIndexDownload     = 256 << 20
	insertBatch          = 5000
)

// Background gives a job the upstream of one remote repository for one run: the egress, credential, share and throttling rules of a
// mirror pass, without the pass (Q-REPO-272, Host.BackgroundUpstream). The composition root implements it over the format host.
type Background interface {
	Upstream(ctx context.Context, repoID int64, jobID string) (discover.Upstream, error)
}

// RemoteJobs run the jobs of the remote class: the parse of an index (deb-remote-parse), the housekeeping and the re-verification of a
// remote (its deb-index job) and the periodic part that finds the remotes that need them. The composition root builds one value and the
// registered handlers find it at run time.
type RemoteJobs struct {
	Store      *Store
	Background Background
	// Items removes the by-hash files the housekeeping finds orphaned; the function is called when a run needs it, because the item
	// store is built after the jobs are registered.
	Items func() Remover
	// ByHashKeep is deb.by_hash_keep: how long the files of a replaced release stay.
	ByHashKeep time.Duration
	Log        *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// MaxParagraphs bounds the paragraphs of one index; 0 is the bound of R-DEB-034, 1,000,000. A test lowers it.
	MaxParagraphs int
}

func (j *RemoteJobs) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *RemoteJobs) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

// repoInfo is what a job reads of a remote repository's document.
type repoInfo struct {
	Class, Format, Key, Project string
	// Keys is deb.upstreamKeys, "" for none.
	Keys string
	// NotFoundTTL is the remote's not-found period.
	NotFoundTTL time.Duration
}

func (j *RemoteJobs) repo(ctx context.Context, id int64) (repoInfo, bool, error) {
	var row q.DebRemoteRepoMetaRow
	err := j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		row, err = qs.DebRemoteRepoMeta(ctx, id)
		return err
	})
	if err != nil {
		if postgres.IsNoRows(err) {
			return repoInfo{}, false, nil
		}
		return repoInfo{}, false, err
	}
	var doc struct {
		Deb struct {
			UpstreamKeys *string `json:"upstreamKeys"`
		} `json:"deb"`
		Remote struct {
			Cache struct {
				NotFoundTTL *int `json:"notFoundTtlSeconds"`
			} `json:"cache"`
		} `json:"remote"`
	}
	_ = json.Unmarshal(row.Config, &doc) //nolint:errcheck // the service validated the document; a row written around it yields the defaults
	info := repoInfo{Class: row.Class, Format: row.Format, Key: row.Key, Project: row.Project, NotFoundTTL: 300 * time.Second}
	if doc.Deb.UpstreamKeys != nil {
		info.Keys = strings.TrimSpace(*doc.Deb.UpstreamKeys)
	}
	if doc.Remote.Cache.NotFoundTTL != nil {
		info.NotFoundTTL = time.Duration(*doc.Remote.Cache.NotFoundTTL) * time.Second
	}
	return info, true, nil
}

// certs reads the certificates of deb.upstreamKeys; none for a remote without keys.
func (r repoInfo) certs() ([]openpgp.Certificate, error) {
	if r.Keys == "" {
		return nil, nil
	}
	return openpgp.ParseStoredCertificates([]byte(r.Keys), openpgp.Limits{})
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// RunRemoteParseJob is the handler of the job deb-remote-parse (R-DEB-023, Q-DEB-36): it reads the hash the stored release names for
// its index path when it starts, fetches the full index through the background path, and replaces the rows of the index path in one
// transaction. A job with an empty path fetches the release documents of its directory first (and records an absent suite) and
// enqueues the parse of the indexes of its scope that lack rows. It is registered with RegisterJobs.
func (j *RemoteJobs) RunParse(ctx context.Context, run *jobs.Run) error {
	var a ParseArgs
	if err := json.Unmarshal(run.Args, &a); err != nil {
		return jobs.Permanent(fmt.Errorf("deb-remote-parse: the arguments do not read: %w", err))
	}
	info, ok, err := j.repo(ctx, a.RepoID)
	if err != nil || !ok {
		return err // a repository that was deleted has nothing to parse
	}
	if info.Class != "remote" || info.Format != "deb" {
		return nil
	}
	up, err := j.Background.Upstream(ctx, a.RepoID, run.ID)
	if err != nil {
		return err
	}
	if a.Path == "" {
		return j.fetchScope(ctx, up, info, a)
	}
	return j.parseIndex(ctx, up, info, a)
}

// fetchScope is the job of the shadowing check for a suite the remote has no release of: it fetches the release documents, records
// the suite as absent when the upstream has none, and enqueues the parse of the indexes of the scope.
func (j *RemoteJobs) fetchScope(ctx context.Context, up discover.Upstream, info repoInfo, a ParseArgs) error {
	rel, found, err := j.usableRelease(ctx, up, info, a.RepoID, a.Dir)
	if err != nil || !found {
		return err
	}
	_ = rel
	files, err := j.Store.Files(ctx, a.RepoID, a.Dir)
	if err != nil {
		return err
	}
	return j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		parsed, err := qs.DebRemoteIndexRows(ctx, q.DebRemoteIndexRowsParams{RepoID: a.RepoID, Dir: a.Dir})
		if err != nil {
			return err
		}
		for _, ref := range rows.PreferredIndexes(files, false) {
			if !rows.InScope(ref.Path, a.Components, a.Architectures) {
				continue
			}
			if slices.ContainsFunc(parsed, func(p q.DebRemoteIndexRowsRow) bool { return p.Path == ref.Path && sum32(p.Sha256) == ref.SHA256 }) {
				continue
			}
			if err := j.Store.EnqueueParse(ctx, tx, a.RepoID, a.Dir, ref.Path); err != nil {
				return err
			}
		}
		return nil
	})
}

// usableRelease returns the stored release of a directory, fetching it through the background path when none is stored or none is
// verified; found is false when the upstream has none (the suite is then recorded absent) or no key verifies it.
func (j *RemoteJobs) usableRelease(ctx context.Context, up discover.Upstream, info repoInfo, repoID int64, dir string) (rows.Release, bool, error) {
	usable := func(r rows.Release) bool { return info.Keys == "" || r.VerifiedBy != "" }
	rel, found, err := j.Store.Release(ctx, repoID, dir)
	if err != nil {
		return rows.Release{}, false, err
	}
	if found && usable(rel) {
		return rel, true, nil
	}
	if err := j.fetchRelease(ctx, up, info, repoID, dir); err != nil {
		return rows.Release{}, false, err
	}
	rel, found, err = j.Store.Release(ctx, repoID, dir)
	if err != nil || !found || !usable(rel) {
		return rows.Release{}, false, err
	}
	return rel, true, nil
}

// fetchRelease makes the remote engine fetch and store the release documents of a directory (its fill verifies them and records
// them). When the upstream answers 404 or 410 for both InRelease and Release of a dists/ directory the suite is recorded absent
// (Q-DEB-41). With keys and no InRelease, the pair Release and Release.gpg is verified here (R-DEB-019).
func (j *RemoteJobs) fetchRelease(ctx context.Context, up discover.Upstream, info repoInfo, repoID int64, dir string) error {
	_, found, err := up.Document(ctx, join(dir, rows.DocInRelease), "")
	if err != nil || found {
		return err
	}
	doc, found, err := up.Document(ctx, join(dir, rows.DocRelease), "")
	if err != nil {
		return err
	}
	if !found {
		if strings.HasPrefix(dir, "dists/") {
			return j.Store.RecordAbsent(ctx, repoID, dir)
		}
		return nil
	}
	return j.verifyPair(ctx, up, info, repoID, dir, doc)
}

// verifyPair verifies a Release with its Release.gpg against the keys and records it verified. It does nothing for a remote without
// keys, whose Release the fill recorded.
func (j *RemoteJobs) verifyPair(ctx context.Context, up discover.Upstream, info repoInfo, repoID int64, dir string, doc []byte) error {
	certs, err := info.certs()
	if err != nil || len(certs) == 0 {
		return err
	}
	sig, found, err := up.Document(ctx, join(dir, "Release.gpg"), "")
	if err != nil || !found {
		return err
	}
	signer, err := openpgp.VerifyDetached(doc, sig, certs, j.now(), openpgp.Limits{})
	if err != nil {
		j.log().Warn("the Release of a remote does not verify", "repository", info.Project+"/"+info.Key, "dir", dir, "reason", openpgp.Reason(err))
		return nil
	}
	rel, err := release.Parse(doc)
	if err != nil {
		return nil //nolint:nilerr // the fill refused a Release that does not parse; nothing is recorded for it
	}
	_, err = j.Store.RecordVerified(ctx, rel.Record(repoID, dir, rows.DocRelease, sha256.Sum256(doc), signer, true))
	return err
}

// parseIndex parses one index into rows.
func (j *RemoteJobs) parseIndex(ctx context.Context, up discover.Upstream, info repoInfo, a ParseArgs) error {
	rel, ok, err := j.usableRelease(ctx, up, info, a.RepoID, a.Dir)
	if err != nil || !ok {
		return err
	}
	files, err := j.Store.Files(ctx, a.RepoID, a.Dir)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rows.PreferredIndexes(files, true), func(r rows.IndexRef) bool { return r.Path == a.Path })
	if i < 0 {
		// The release does not list the index any more: its rows go.
		return j.dropIndex(ctx, a)
	}
	ref := rows.PreferredIndexes(files, true)[i]
	var have []byte
	if err := j.Store.remoteStore.read(ctx, nil, func(qs *q.Queries) error {
		var err error
		have, err = qs.DebRemoteIndexSha(ctx, q.DebRemoteIndexShaParams{RepoID: a.RepoID, Dir: a.Dir, Path: a.Path})
		if postgres.IsNoRows(err) {
			have, err = nil, nil
		}
		return err
	}); err != nil {
		return err
	}
	if sum32(have) == ref.SHA256 && len(have) == 32 {
		return nil
	}
	body, err := j.fetchIndex(ctx, up, rel, a.Dir, ref)
	if err != nil {
		return err
	}
	if sha256.Sum256(body) != ref.SHA256 {
		return errors.New("the index the upstream served is not the file its release names; the next attempt reads the release again")
	}
	return j.replaceRows(ctx, a, ref, body)
}

func (j *RemoteJobs) dropIndex(ctx context.Context, a ParseArgs) error {
	return j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if err := qs.DebRemoteLockDir(ctx, q.DebRemoteLockDirParams{RepoID: a.RepoID, Dir: a.Dir}); err != nil {
			return err
		}
		return qs.DebRemoteDeleteIndex(ctx, q.DebRemoteDeleteIndexParams{RepoID: a.RepoID, Dir: a.Dir, Path: a.Path})
	})
}

// fetchIndex reads the full index the release names: by its hash when the archive serves by-hash, else at its plain path (Q-REPO-272).
func (j *RemoteJobs) fetchIndex(ctx context.Context, up discover.Upstream, rel rows.Release, dir string, ref rows.IndexRef) ([]byte, error) {
	if rel.ByHash {
		p := join(dir, path.Dir(ref.File)+"/by-hash/SHA256/"+hex.EncodeToString(ref.SHA256[:]))
		body, found, err := up.Artifact(ctx, p, maxIndexDownload)
		if err != nil {
			return nil, err
		}
		if found && body == nil {
			return nil, jobs.Permanent(errors.New("the index is larger than the limit of the parse"))
		}
		if found {
			return body, nil
		}
	}
	body, found, err := up.Document(ctx, join(dir, ref.File), "")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the upstream has no %s", join(dir, ref.File))
	}
	return body, nil
}

var (
	packageRE = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9.+~:-]*$`)
	archRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// errTooMany is the failure of an index with more paragraphs than the bound; the earlier rows stay.
var errTooMany = jobs.Permanent(errors.New("too_many_entries: the index has more than 1,000,000 paragraphs"))

// batch collects the rows of one insert.
type batch struct {
	arg q.DebRemoteInsertEntriesParams
	n   int
}

func (b *batch) add(pkg, version, arch, p string, sum [32]byte, size int64) {
	b.arg.Packages = append(b.arg.Packages, pkg)
	b.arg.Versions = append(b.arg.Versions, version)
	b.arg.Architectures = append(b.arg.Architectures, arch)
	b.arg.Paths = append(b.arg.Paths, p)
	b.arg.Sha256s = append(b.arg.Sha256s, slices.Clone(sum[:]))
	b.arg.Sizes = append(b.arg.Sizes, size)
	b.n++
}

// replaceRows parses the index and replaces the rows of its path in one transaction. A bound that is crossed or a paragraph that does
// not read fails the run and the transaction rolls back, so the earlier rows stay (R-DEB-023).
func (j *RemoteJobs) replaceRows(ctx context.Context, a ParseArgs, ref rows.IndexRef, body []byte) error {
	return j.Store.o.DB.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queries(tx)
		if err != nil {
			return err
		}
		if err := qs.DebRemoteLockDir(ctx, q.DebRemoteLockDirParams{RepoID: a.RepoID, Dir: a.Dir}); err != nil {
			return err
		}
		if err := qs.DebRemoteDeleteEntries(ctx, q.DebRemoteDeleteEntriesParams{RepoID: a.RepoID, Dir: a.Dir, Path: a.Path}); err != nil {
			return err
		}
		// The index row first: the entries reference it.
		if err := qs.DebRemoteUpsertIndex(ctx, q.DebRemoteUpsertIndexParams{
			RepoID: a.RepoID, Dir: a.Dir, Path: a.Path, Sha256: ref.SHA256[:], Entries: 0, Skipped: 0,
		}); err != nil {
			return err
		}
		r, err := decompress(ref.File, body)
		if err != nil {
			return jobs.Permanent(err)
		}
		entries, skipped := 0, 0
		flush := func(b *batch) error {
			if b.n == 0 {
				return nil
			}
			b.arg.RepoID, b.arg.Dir, b.arg.IndexPath = a.RepoID, a.Dir, a.Path
			err := qs.DebRemoteInsertEntries(ctx, b.arg)
			*b = batch{}
			return err
		}
		var b batch
		sources := rows.IsSources(a.Path)
		limit := maxIndexParagraphs
		if j.MaxParagraphs > 0 {
			limit = j.MaxParagraphs
		}
		cr := control.NewReader(r, false)
		for paragraphs := 0; ; paragraphs++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			p, err := cr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return jobs.Permanent(fmt.Errorf("the index does not read: %w", err))
			}
			if paragraphs >= limit {
				return errTooMany
			}
			n := 0
			if sources {
				n = sourceEntries(p, &b)
			} else {
				n = binaryEntry(p, &b)
			}
			if n == 0 {
				skipped++
			}
			entries += n
			if b.n >= insertBatch {
				if err := flush(&b); err != nil {
					return err
				}
			}
		}
		if err := flush(&b); err != nil {
			return err
		}
		return qs.DebRemoteUpsertIndex(ctx, q.DebRemoteUpsertIndexParams{
			RepoID: a.RepoID, Dir: a.Dir, Path: a.Path, Sha256: ref.SHA256[:], Entries: int32(min(entries, 1<<31-1)), //nolint:gosec // bounded above
			Skipped: int32(min(skipped, 1<<31-1)), //nolint:gosec // bounded above
		})
	})
}

// decompress opens an index by the suffix of its file name, under the bound of 1 GiB decompressed (R-DEB-034).
func decompress(file string, body []byte) (io.Reader, error) {
	br := bytes.NewReader(body)
	switch {
	case strings.HasSuffix(file, ".xz"):
		return untrusted.NewXZReaderMax(br, maxIndexUncompressed)
	case strings.HasSuffix(file, ".gz"):
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("the index is not a gzip stream: %w", err)
		}
		return untrusted.Limit(zr, maxIndexUncompressed), nil
	case strings.HasSuffix(file, ".bz2"):
		return untrusted.Limit(bzip2.NewReader(br), maxIndexUncompressed), nil
	case strings.HasSuffix(file, ".zst"):
		return untrusted.NewZstdReaderMax(br, maxIndexUncompressed)
	}
	return untrusted.Limit(br, maxIndexUncompressed), nil
}

// cleanFilename admits the Filename of an entry: relative to the archive root, without an empty, "." or ".." segment after a leading
// "./", and without a control character (R-DEB-034).
func cleanFilename(f string) (string, bool) {
	f = strings.TrimPrefix(f, "./")
	if f == "" || strings.HasPrefix(f, "/") || len(f) > 1024 {
		return "", false
	}
	for seg := range strings.SplitSeq(f, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	for _, r := range f {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return f, true
}

// binaryEntry reads a paragraph of a Packages index: one entry for its Filename, none when a required field is missing or breaks its
// rule (the paragraph is then skipped and counted, R-DEB-023).
func binaryEntry(p control.Paragraph, b *batch) int {
	pkg, version, arch := p.Value("Package"), p.Value("Version"), p.Value("Architecture")
	file, ok := cleanFilename(p.Value("Filename"))
	size, serr := strconv.ParseInt(strings.TrimSpace(p.Value("Size")), 10, 64)
	sum, derr := hex.DecodeString(strings.TrimSpace(p.Value("SHA256")))
	if !ok || serr != nil || size < 0 || derr != nil || len(sum) != 32 ||
		!packageRE.MatchString(pkg) || !versionRE.MatchString(version) || !archRE.MatchString(arch) {
		return 0
	}
	b.add(pkg, version, arch, file, sum32(sum), size)
	return 1
}

// sourceEntries reads a paragraph of a Sources index: one entry for each file of Checksums-Sha256, below its Directory.
func sourceEntries(p control.Paragraph, b *batch) int {
	pkg, version, dir := p.Value("Package"), p.Value("Version"), strings.Trim(p.Value("Directory"), "/")
	if !packageRE.MatchString(pkg) || !versionRE.MatchString(version) {
		return 0
	}
	n := 0
	for line := range strings.SplitSeq(p.Value("Checksums-Sha256"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		sum, err := hex.DecodeString(f[0])
		size, serr := strconv.ParseInt(f[1], 10, 64)
		name := f[2]
		if dir != "" {
			name = dir + "/" + name
		}
		file, ok := cleanFilename(name)
		if err != nil || len(sum) != 32 || serr != nil || size < 0 || !ok {
			continue
		}
		b.add(pkg, version, "source", file, sum32(sum), size)
		n++
	}
	return n
}
