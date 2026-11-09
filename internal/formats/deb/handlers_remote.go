package deb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// What a client sees of a remote repository (R-DEB-019 to R-DEB-022). Three routes serve every class and are answered here for the
// remote one (registerRemoteHandler); the routes of a flat repository are the remote class's alone.
func init() {
	for _, route := range []string{routeDists, routeByHash, routePoolGet} {
		registerRemoteHandler(route, func(f *Format) formats.HandlerFunc { return f.handleRemote })
	}
	for _, route := range []string{routeFlatRelease, routeFlatIndex, routeFlatFile} {
		registerHandler(route, func(f *Format) formats.HandlerFunc { return f.handleRemote })
	}
}

// debMember reads the deb member of a repository's stored document.
func debMember(config []byte) json.RawMessage {
	var doc struct {
		Deb json.RawMessage `json:"deb"`
	}
	_ = json.Unmarshal(config, &doc) //nolint:errcheck // a document the service stored reads; one written around it yields the defaults
	return doc.Deb
}

func errNotInRelease() error {
	return errs.E(errs.NotFound, errs.CodeDebNotInRelease, "The release of the repository does not list this file.")
}

// handleRemote answers a read of a remote repository: it decides the permission, then serves the file by what it is (R-DEB-021,
// R-DEB-022). Release documents and the files a release lists are metadata documents of the engine; by-hash files, patches and package
// files are artifacts it fills once.
func (f *Format) handleRemote(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	p := rq.Path.Path
	rp := analyzeRemote(p)
	// The routes of a flat repository match by path whatever the class (Q-DEB-45); a repository of another class holds no such file.
	if rp.kind == rkNone || rq.Repo.Class != formats.Remote {
		return errNoSuchFile()
	}
	info := f.Classify(rq.Path)
	// A package file is decided on its name and version, everything else on its path (R-DEB-003, R-DEB-004).
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: info.Package, Version: info.Version, Path: p, Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	switch rp.kind {
	case rkNoHash:
		return errs.E(errs.NotFound, errs.CodeDebNotInRelease, "Only the SHA-256 and SHA-512 by-hash files of an archive are served.")
	case rkRelease, rkIndex:
		st, err := storeOf(rq.Host.FormatData(ID))
		if err != nil {
			return err
		}
		if rp.kind == rkRelease {
			return f.serveRelease(ctx, rq, w, st, rp)
		}
		return f.serveIndex(ctx, rq, w, st)
	}
	return f.serveContent(ctx, rq, w)
}

// serveContent serves an artifact: filled once under the engine's flight, verified against the digest the gate announces, served with
// ranges and conditional requests from the stored item, immutable (R-DEB-021, R-DEB-022).
func (f *Format) serveContent(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	mt := MediaType
	if !strings.HasSuffix(rq.Path.Path, ".deb") && !strings.HasSuffix(rq.Path.Path, ".udeb") {
		mt = "application/octet-stream"
	}
	return rq.Host.Remote().Artifact(ctx, rq, rq.Path, formats.Digests{}, immutableWriter{ResponseWriter: w, mediaType: mt})
}

// certsOf reads the certificates of deb.upstreamKeys; none for a remote without keys.
func certsOf(set Settings) ([]openpgp.Certificate, error) {
	if set.UpstreamKeys == "" {
		return nil, nil
	}
	certs, err := openpgp.ParseStoredCertificates([]byte(set.UpstreamKeys), openpgp.Limits{})
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream keys of the repository do not read.", errs.WithCause(err))
	}
	return certs, nil
}

// docView is a document as a response describes it: the digest of its bytes, when this replica's copy was fetched and the engine's
// Cache-Status.
type docView struct {
	sum         [32]byte
	fetchedAt   time.Time
	cacheStatus string
}

// metadata fetches a metadata document through the engine and reads its bytes. A document the upstream does not have is the 404 of a
// negative entry, with the Cache-Status the engine rendered. A failed verification of a release is counted and written as the security
// event (R-DEB-020).
func (f *Format) metadata(ctx context.Context, rq *formats.Request, st RemoteStore, dir, p string) (docView, []byte, error) {
	doc, err := rq.Host.Remote().Metadata(ctx, rq, formats.Path{Path: p}, "")
	if err == nil && doc.Status.Negative {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no such file.", errs.WithHeader("Cache-Status", doc.Status.Header))
	}
	if err != nil {
		f.noteSignature(ctx, rq, st, dir, err)
		return docView{}, nil, err
	}
	body, err := doc.Bytes(ctx)
	if err != nil {
		return docView{}, nil, err
	}
	return docView{sum: doc.Digest, fetchedAt: doc.FetchedAt, cacheStatus: doc.Status.Header}, body, nil
}

// noteSignature counts a release that failed its verification and writes the security event, at most once an hour per directory.
func (f *Format) noteSignature(ctx context.Context, rq *formats.Request, st RemoteStore, dir string, err error) {
	reason := SignatureReason(err)
	if reason == "" {
		return
	}
	repo := rq.RepoRef()
	f.replicaOf(rq.Host).met.signatureFailed(repo.Name.String(), reason)
	st.RecordSignatureInvalid(ctx, repo.ID, repo.Name.Project, repo.Name.Key, dir, reason)
}

func isNotFound(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Kind == errs.NotFound
}

// serveRelease serves InRelease, Release or Release.gpg of a directory, byte for byte as the upstream sent it (R-DEB-019). A Release
// and its Release.gpg are one pair: a request for either fetches both, and with keys the pair verifies before the answer. A suite for
// which the upstream has neither InRelease nor Release is recorded absent (Q-DEB-41).
func (f *Format) serveRelease(ctx context.Context, rq *formats.Request, w http.ResponseWriter, st RemoteStore, rp remotePath) error {
	set := SettingsOf(debMember(rq.Repo.Config))
	switch rp.doc {
	case FileInRelease:
		v, body, err := f.metadata(ctx, rq, st, rp.dir, rq.Path.Path)
		if err != nil {
			return f.absentOnMissing(ctx, rq, st, rp, FileRelease, err)
		}
		return writeDoc(rq, w, v, body, "text/plain; charset=utf-8")
	}
	relV, relBody, err := f.metadata(ctx, rq, st, rp.dir, joinDir(rp.dir, FileRelease))
	if err != nil {
		return f.absentOnMissing(ctx, rq, st, rp, FileInRelease, err)
	}
	sigV, sigBody, err := f.metadata(ctx, rq, st, rp.dir, joinDir(rp.dir, FileReleaseGPG))
	if err != nil {
		return err
	}
	if set.UpstreamKeys != "" {
		if err := f.verifyPair(ctx, rq, st, rp.dir, relV, relBody, sigBody, set); err != nil {
			return err
		}
	}
	if rp.doc == FileReleaseGPG {
		return writeDoc(rq, w, sigV, sigBody, "application/pgp-signature")
	}
	return writeDoc(rq, w, relV, relBody, "text/plain; charset=utf-8")
}

// absentOnMissing answers the 404 of a release document and, where the upstream has neither InRelease nor Release for a dists/
// directory, records the suite absent so that the shadowing check can settle it (Q-DEB-41).
func (f *Format) absentOnMissing(ctx context.Context, rq *formats.Request, st RemoteStore, rp remotePath, other string, err error) error {
	if !isNotFound(err) || !strings.HasPrefix(rp.dir, dirDists+"/") {
		return err
	}
	if _, _, oerr := f.metadata(ctx, rq, st, rp.dir, joinDir(rp.dir, other)); isNotFound(oerr) {
		if aerr := st.RecordAbsent(ctx, rq.RepoRef().ID, rp.dir); aerr != nil {
			return aerr
		}
	}
	return err
}

// verifyPair verifies a Release against its Release.gpg with the keys of the remote (R-DEB-019, R-DEB-020) and records the Release
// verified. A pair that does not verify is 502 deb_upstream_signature_invalid.
func (f *Format) verifyPair(ctx context.Context, rq *formats.Request, st RemoteStore, dir string, rel docView, relBody, sigBody []byte, set Settings) error {
	certs, err := certsOf(set)
	if err != nil {
		return err
	}
	signer, err := openpgp.VerifyDetached(relBody, sigBody, certs, time.Now(), openpgp.Limits{})
	if err != nil {
		perr := releaseError(err, true)
		f.noteSignature(ctx, rq, st, dir, perr)
		return perr
	}
	parsed, err := release.Parse(relBody)
	if err != nil {
		return releaseError(err, false)
	}
	suite := ""
	if strings.HasPrefix(dir, dirDists+"/") {
		suite = suiteOf(dir)
	}
	if err := parsed.CheckSuite(suite, time.Now()); err != nil {
		return releaseError(err, false)
	}
	repoID := rq.RepoRef().ID
	have, err := st.Releases(ctx, repoID, []string{dir})
	if err != nil {
		return err
	}
	for _, r := range have {
		if r.Document == rows.DocRelease && r.SHA256 == rel.sum && r.VerifiedBy != "" {
			return nil
		}
	}
	_, err = st.RecordVerified(ctx, parsed.Record(repoID, dir, rows.DocRelease, rel.sum, signer, true))
	return err
}

// ensureRelease makes sure the directory has a stored release it may name files from: it fetches InRelease, else Release with its
// Release.gpg, through the engine, which verifies and records what it stores. It reports whether the upstream has one.
func (f *Format) ensureRelease(ctx context.Context, rq *formats.Request, st RemoteStore, dir string, set Settings) (bool, error) {
	_, _, err := f.metadata(ctx, rq, st, dir, joinDir(dir, FileInRelease))
	if err == nil {
		return true, nil
	}
	if !isNotFound(err) {
		return false, err
	}
	relV, relBody, err := f.metadata(ctx, rq, st, dir, joinDir(dir, FileRelease))
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if set.UpstreamKeys != "" {
		_, sigBody, err := f.metadata(ctx, rq, st, dir, joinDir(dir, FileReleaseGPG))
		if err != nil {
			return false, err
		}
		if err := f.verifyPair(ctx, rq, st, dir, relV, relBody, sigBody, set); err != nil {
			return false, err
		}
	}
	return true, nil
}

// serveIndex serves a file a release names (R-DEB-021): a metadata document at its plain path, served only while its SHA-256 is the
// one the stored release lists. A path the release does not list is 404 deb_not_in_release without a request to the upstream; an
// index of a directory without a stored release fetches the release first. A document that is not the listed file is fetched again;
// when the upstream's file is not the listed one either, 502 deb_index_mismatch and nothing new is stored.
func (f *Format) serveIndex(ctx context.Context, rq *formats.Request, w http.ResponseWriter, st RemoteStore) error {
	p := rq.Path.Path
	repoID := rq.RepoRef().ID
	set := SettingsOf(debMember(rq.Repo.Config))
	dir, rel, ok, err := releaseDirOf(ctx, st, repoID, p)
	if err != nil {
		return err
	}
	if !ok {
		if dir, rel, ok, err = f.discoverRelease(ctx, rq, st, p, set); err != nil || !ok {
			if err == nil {
				err = errNotInRelease()
			}
			return err
		}
	}
	if err := f.requireVerified(ctx, rq, st, dir, set); err != nil {
		return err
	}
	listed, found, err := st.ListedFile(ctx, nil, repoID, dir, rel)
	if err != nil {
		return err
	}
	if !found {
		if analyzeRemote(p).kind == rkIndex && strings.Contains(p, ".diff/") {
			// The Index of a pdiff directory that the release does not list: passed through (R-DEB-021).
			v, body, err := f.metadata(ctx, rq, st, dir, p)
			if err != nil {
				return err
			}
			return writeDoc(rq, w, v, body, contentTypeOf(p))
		}
		return errNotInRelease()
	}
	v, body, err := f.metadata(ctx, rq, st, dir, p)
	if err != nil {
		return err
	}
	if v.sum != listed.SHA256 {
		if v, body, err = f.exchangeIndex(ctx, rq, p, listed); err != nil {
			return err
		}
	}
	return writeDoc(rq, w, v, body, contentTypeOf(p))
}

// exchangeIndex reads the index the stored release lists straight from the upstream, outside the cache, when the stored document is
// another file (it is older than the release, or the other way round): the file is served when it is the listed one and verified
// against its hash, and the stored document is replaced when its period ends. The upstream's file that is not the listed one is the
// inconsistent upstream of R-DEB-021.
func (f *Format) exchangeIndex(ctx context.Context, rq *formats.Request, p string, listed rows.ReleaseFile) (docView, []byte, error) {
	mismatch := errs.E(errs.UpstreamFailed, errs.CodeDebIndexMismatch,
		"The upstream served an index that is not the file its release lists; nothing is stored.")
	resp, err := rq.Host.Remote().Exchange(ctx, rq, formats.Exchange{Method: http.MethodGet, Path: p, Header: http.Header{"Accept-Encoding": {"identity"}}})
	if err != nil {
		if errors.Is(err, formats.ErrOffline) {
			return docView{}, nil, mismatch
		}
		return docView{}, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return docView{}, nil, mismatch
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxDocumentBytes+1))
	if err != nil {
		return docView{}, nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "The upstream's index cannot be read.", errs.WithCause(err))
	}
	if len(body) > MaxDocumentBytes || sha256.Sum256(body) != listed.SHA256 {
		return docView{}, nil, mismatch
	}
	return docView{sum: listed.SHA256, fetchedAt: time.Now()}, body, nil
}

// discoverRelease finds the release directory of a path no directory has a stored release for: it asks for the release of each
// directory above the path, the shortest first, and takes the first the upstream has.
func (f *Format) discoverRelease(ctx context.Context, rq *formats.Request, st RemoteStore, p string, set Settings) (dir, rel string, ok bool, err error) {
	for _, cand := range dirCandidates(p) {
		found, err := f.ensureRelease(ctx, rq, st, cand, set)
		if err != nil {
			return "", "", false, err
		}
		if !found {
			continue
		}
		if d, r, ok, err := releaseDirOf(ctx, st, rq.RepoRef().ID, p); err != nil || ok {
			return d, r, ok, err
		}
	}
	return "", "", false, nil
}

// requireVerified fails closed for a directory whose stored release no key of the remote verifies (R-DEB-020, Q-DEB-42): nothing
// names files from it until a release verifies. The release is asked for again, so a revalidation that the stored copy's period would
// delay still happens when the document has expired.
func (f *Format) requireVerified(ctx context.Context, rq *formats.Request, st RemoteStore, dir string, set Settings) error {
	if set.UpstreamKeys == "" {
		return nil
	}
	repoID := rq.RepoRef().ID
	r, found, err := st.Release(ctx, repoID, dir)
	if err != nil || (found && r.VerifiedBy != "") {
		return err
	}
	if _, err := f.ensureRelease(ctx, rq, st, dir, set); err != nil {
		return err
	}
	if r, found, err = st.Release(ctx, repoID, dir); err != nil || (found && r.VerifiedBy != "") {
		return err
	}
	return errs.E(errs.UpstreamFailed, errs.CodeDebUpstreamSignatureInvalid,
		"No key of deb.upstreamKeys verifies the stored release of this directory; nothing is served from it until a release does.")
}

// contentTypeOf is the media type of a file of an archive by its name.
func contentTypeOf(p string) string {
	switch {
	case strings.HasSuffix(p, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(p, ".xz"):
		return "application/x-xz"
	case strings.HasSuffix(p, ".bz2"):
		return "application/x-bzip2"
	case strings.HasSuffix(p, ".zst"):
		return "application/zstd"
	case strings.HasSuffix(p, ".lzma"):
		return "application/x-lzma"
	case strings.HasSuffix(p, "/Release.gpg") || p == FileReleaseGPG:
		return "application/pgp-signature"
	case strings.HasSuffix(p, "/Packages") || strings.HasSuffix(p, "/Sources") || strings.HasSuffix(p, "/Index"):
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// writeDoc answers a document as it is: its bytes, an ETag of its SHA-256, the Last-Modified of this copy, and the Cache-Status of the
// engine. A conditional request that matches is answered 304. The cache rule is no-cache: a client asks again, and the answer is a 304
// while the document is the same.
func writeDoc(rq *formats.Request, w http.ResponseWriter, v docView, body []byte, contentType string) error {
	etag := `"` + hex.EncodeToString(v.sum[:]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-cache")
	if v.cacheStatus != "" {
		h.Set("Cache-Status", v.cacheStatus)
	}
	modified := v.fetchedAt.UTC().Truncate(time.Second)
	if !modified.IsZero() {
		h.Set("Last-Modified", modified.Format(http.TimeFormat))
	}
	notModified := false
	if inm := rq.Header.Get("If-None-Match"); inm != "" {
		notModified = ifNoneMatch(inm, etag)
	} else if ims, err := http.ParseTime(rq.Header.Get("If-Modified-Since")); err == nil && !modified.IsZero() {
		notModified = !modified.After(ims)
	}
	if notModified {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method != http.MethodHead {
		_, err := w.Write(body)
		return err
	}
	return nil
}
