package deb

import (
	"context"
	"encoding/hex"
	"path"
	"slices"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// The notes of the index step (Q-DEB-91): a suite or an index the pass skipped, with the reason.
const (
	noteSuiteUnavailable  = "suite_unavailable"
	noteReleaseUnverified = "release_unverified"
	noteReleaseExpired    = "release_expired"
	noteIndexUnlisted     = "index_unlisted"
)

// indexVariants lists the compressions an index file is fetched in, as the release lists them: the plain file and each suffix.
func indexVariants() []string { return []string{"", ".xz", ".gz", ".bz2", ".lzma", ".zst"} }

// MirrorSync is the index step of a pass (R-DEB-024, Mirror discovery; Q-DEB-91). For each suite of the scope it asks the pass's
// upstream for the release document (stored, revalidated once per attempt), checks it against the current keys and its Valid-Until, and
// keeps the index files the scope names in every variant the release lists, by hash when the release says Acquire-By-Hash and at their
// plain path otherwise; then it parses the Packages (and Sources) indexes into rows in the pass, where the job of R-DEB-023 would
// have waited for a client or a queue. A suite the upstream does not have is recorded absent and noted; a release no key verifies or
// that has expired plans nothing. A rate-limit answer or any other error of the upstream ends the call.
func (s *Store) MirrorSync(ctx context.Context, up discover.Upstream, sc rows.MirrorScope) (suites, notes []string, err error) {
	j := &RemoteJobs{Store: s}
	info, ok, err := j.repo(ctx, sc.RepoID)
	if err != nil {
		return nil, nil, err
	}
	if !ok || info.Class != "remote" {
		return nil, nil, errs.E(errs.Internal, errs.CodeInternal, "The repository of the mirror pass is not a remote repository.")
	}
	certs, err := info.certs()
	if err != nil {
		return nil, nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream keys of the repository do not read.", errs.WithCause(err))
	}
	if len(certs) == 0 {
		return nil, nil, errs.E(errs.Unprocessable, errs.CodeDebUpstreamKeysRequired, "A mirror needs deb.upstreamKeys to verify the releases it keeps.")
	}
	for _, suite := range sc.Suites {
		dir := "dists/" + suite
		rel, note, err := j.mirrorRelease(ctx, up, info, certs, sc.RepoID, suite, dir)
		if err != nil {
			return nil, nil, err
		}
		if note != "" {
			notes = append(notes, note+": "+suite)
			continue
		}
		unlisted, err := j.mirrorIndexes(ctx, up, info, sc, dir, rel)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range unlisted {
			notes = append(notes, noteIndexUnlisted+": "+dir+"/"+p)
		}
		suites = append(suites, suite)
	}
	return suites, notes, nil
}

// mirrorRelease reads the release of a suite for a pass and judges it: a note names why it plans nothing.
func (j *RemoteJobs) mirrorRelease(ctx context.Context, up discover.Upstream, info repoInfo, certs []openpgp.Certificate, repoID int64, suite, dir string) (*release.Release, string, error) {
	now := j.now()
	body, found, err := up.Document(ctx, join(dir, rows.DocInRelease), "")
	if err != nil {
		return nil, "", err
	}
	var rel *release.Release
	if found {
		var rerr error
		if rel, _, rerr = release.Signed(body, certs, now); rerr != nil {
			return nil, noteReleaseUnverified, nil //nolint:nilerr // the release is noted and plans nothing (Q-DEB-32)
		}
	} else {
		doc, ok, err := up.Document(ctx, join(dir, rows.DocRelease), "")
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, noteSuiteUnavailable, j.Store.RecordAbsent(ctx, repoID, dir)
		}
		if err := j.verifyPair(ctx, up, info, repoID, dir, doc); err != nil {
			return nil, "", err
		}
		stored, ok, err := j.Store.Release(ctx, repoID, dir)
		if err != nil {
			return nil, "", err
		}
		if !ok || stored.VerifiedBy == "" {
			return nil, noteReleaseUnverified, nil
		}
		if rel, err = release.Parse(doc); err != nil {
			return nil, noteReleaseUnverified, nil //nolint:nilerr // a Release that does not parse plans nothing
		}
	}
	if !rel.ValidUntil.IsZero() && now.After(rel.ValidUntil) {
		return nil, noteReleaseExpired, nil
	}
	if err := rel.CheckSuite(suite, now); err != nil {
		return nil, noteReleaseUnverified, nil //nolint:nilerr // the release is another suite's
	}
	return rel, "", nil
}

// mirrorIndexes fetches the index files of the scope in each listed variant and parses the Packages and Sources indexes. It returns the
// indexes of the scope that the release does not list.
func (j *RemoteJobs) mirrorIndexes(ctx context.Context, up discover.Upstream, info repoInfo, sc rows.MirrorScope, dir string, rel *release.Release) ([]string, error) {
	files, err := j.Store.Files(ctx, sc.RepoID, dir)
	if err != nil {
		return nil, err
	}
	archs := slices.Clone(sc.Architectures)
	if !slices.Contains(archs, "all") {
		archs = append(archs, "all")
	}
	type want struct {
		base  string
		parse bool
	}
	var wants []want
	for _, comp := range sc.Components {
		for _, arch := range archs {
			wants = append(wants, want{comp + "/binary-" + arch + "/Packages", true})
		}
		if sc.Sources {
			wants = append(wants, want{comp + "/source/Sources", true})
		}
		for _, lang := range sc.Translations {
			wants = append(wants, want{comp + "/i18n/Translation-" + lang, false})
		}
	}
	var unlisted []string
	for _, w := range wants {
		listed := 0
		for _, suffix := range indexVariants() {
			f, ok := rows.FileOf(files, w.base+suffix)
			if !ok {
				continue
			}
			listed++
			if err := j.fetchVariant(ctx, up, rel, dir, f); err != nil {
				return nil, err
			}
		}
		if listed == 0 {
			unlisted = append(unlisted, w.base)
			continue
		}
		if w.parse {
			if err := j.parseIndex(ctx, up, info, ParseArgs{RepoID: sc.RepoID, Dir: dir, Path: w.base}); err != nil {
				return nil, err
			}
		}
	}
	return unlisted, nil
}

// fetchVariant keeps one listed file of an index the way a client asks for it: by its hash when the release says Acquire-By-Hash and at
// its plain path otherwise. A client takes the strongest hash a release lists, so with a SHA512 section the file is kept by that hash
// as well (Q-DEB-92). A variant the upstream does not serve is skipped. The bytes are not read back.
func (j *RemoteJobs) fetchVariant(ctx context.Context, up discover.Upstream, rel *release.Release, dir string, f rows.ReleaseFile) error {
	if !rel.ByHash {
		_, _, err := up.Document(ctx, join(dir, f.Path), "")
		return err
	}
	hashes := []string{"SHA256/" + hex.EncodeToString(f.SHA256[:])}
	if d, ok := rel.SHA512[f.Path]; ok {
		hashes = append(hashes, "SHA512/"+d)
	}
	for _, h := range hashes {
		if _, _, err := up.Artifact(ctx, join(dir, path.Dir(f.Path)+"/by-hash/"+h), 0); err != nil {
			return err
		}
	}
	return nil
}
