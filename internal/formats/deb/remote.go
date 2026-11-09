package deb

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// The remote class (R-DEB-019 to R-DEB-023). The remote engine owns the request path, the freshness, the collapsing of concurrent
// fetches, the negative cache and the circuit (R-REPO-025 to R-REPO-031); this file is what the format adds to it: where the
// archive's files are (below remote.url, at the path the client asks for), what a release document must be before it is stored
// (parsed, and verified against deb.upstreamKeys when the remote has them), and what is recorded of it in the transaction that stores
// it. The gate of the pool files is remote_gate.go, the shadowing check remote_names.go, the answers a client sees handlers_remote.go.
// The upstream's files are passed through byte for byte: trove signs nothing of a remote (Q-DEB-13).

// remoteAdapter embeds the defaults of the contract: no pass-through queries.
type remoteAdapter struct {
	formats.RemoteBase
	f *Format
}

var (
	_ formats.RemoteAdapter = remoteAdapter{}
	_ formats.FactsGate     = remoteAdapter{}
	_ formats.NameServer    = remoteAdapter{}
	_ formats.Revalidator   = remoteAdapter{}
)

// TracksVersionRemoval is false: an APT archive has no removal marks (R-REPO-029).
func (remoteAdapter) TracksVersionRemoval() bool { return false }

// remoteKind is how a path of a remote repository behaves (R-DEB-021, R-DEB-022).
type remoteKind int

const (
	// rkNone is a path outside the layout of an archive.
	rkNone remoteKind = iota
	// rkRelease is a release document of a directory: InRelease, Release or Release.gpg.
	rkRelease
	// rkIndex is a file a release names: any file below dists/ that is no by-hash file and no patch, and an index name of a flat
	// directory. A metadata document served while its SHA-256 is the one the stored release lists.
	rkIndex
	// rkByHash is a SHA-256 or SHA-512 by-hash file: content addressed by the digest in its path.
	rkByHash
	// rkNoHash is a by-hash file of MD5Sum or SHA1: no upstream request, 404.
	rkNoHash
	// rkPatch is a file below a *.diff directory that is no Index: a pdiff patch, passed through as content.
	rkPatch
	// rkContent is a package file or any other file outside dists/: content.
	rkContent
)

// remotePath is a canonical path of a remote repository taken apart.
type remotePath struct {
	kind remoteKind
	// dir and doc name the release directory and document of an rkRelease path.
	dir, doc string
	// algo and digest are the algorithm directory and the hex digest of an rkByHash path.
	algo, digest string
}

func analyzeRemote(p string) remotePath {
	segs := strings.Split(p, "/")
	n := len(segs)
	file := segs[n-1]
	if segs[0] == dirDists {
		if n < 3 {
			return remotePath{}
		}
		if n >= 4 && segs[n-3] == DirByHash {
			switch algo := segs[n-2]; {
			case algo == HashSHA256 && ValidSHA256Hex(file):
				return remotePath{kind: rkByHash, algo: algo, digest: file}
			case algo == "SHA512" && len(file) == 128 && isHex(file):
				return remotePath{kind: rkByHash, algo: algo, digest: file}
			case algo == "MD5Sum" || algo == "SHA1":
				return remotePath{kind: rkNoHash}
			}
			return remotePath{}
		}
		if IsReleaseFile(file) {
			return remotePath{kind: rkRelease, dir: strings.Join(segs[:n-1], "/"), doc: file}
		}
		if strings.HasSuffix(segs[n-2], ".diff") && file != "Index" {
			return remotePath{kind: rkPatch}
		}
		return remotePath{kind: rkIndex}
	}
	if IsReleaseFile(file) {
		return remotePath{kind: rkRelease, dir: strings.Join(segs[:n-1], "/"), doc: file}
	}
	if n >= 2 && strings.HasSuffix(segs[n-2], ".diff") {
		if file == "Index" {
			return remotePath{kind: rkIndex}
		}
		return remotePath{kind: rkPatch}
	}
	if IsIndexName(file) {
		return remotePath{kind: rkIndex}
	}
	return remotePath{kind: rkContent}
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

// suiteOf is the name of the suite a release directory below dists/ is; "" for a flat directory, which has no name to compare.
func suiteOf(dir string) string { return strings.TrimPrefix(dir, dirDists+"/") }

// joinDir is a path below a release directory.
func joinDir(dir, rel string) string {
	if dir == "" {
		return rel
	}
	return dir + "/" + rel
}

// UpstreamRequest maps a canonical path to the upstream request (R-DEB-019): the path below remote.url, the same path the client asked
// for. The address is derived from the repository's URL, never from a host a client named.
func (remoteAdapter) UpstreamRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string) (*http.Request, error) {
	if analyzeRemote(p.Path).kind == rkNone {
		return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	}
	u := url.URL{Scheme: rc.Upstream.Scheme, Host: rc.Upstream.Host, Path: strings.TrimRight(rc.Upstream.Path, "/") + "/" + p.Path}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "", errs.WithCause(err))
	}
	return req, nil
}

// AcceptResponse bounds what is read before a byte is stored: a release document is at most 16 MiB (R-DEB-019, R-DEB-034).
func (remoteAdapter) AcceptResponse(p formats.Path, resp *http.Response) error {
	if rp := analyzeRemote(p.Path); rp.kind == rkRelease {
		if resp.ContentLength > release.MaxBytes {
			return errs.E(errs.UpstreamFailed, errs.CodeDebReleaseInvalid, "The upstream's release document is longer than 16 MiB.")
		}
		resp.Body = &boundedBody{rc: resp.Body, left: release.MaxBytes}
	}
	return nil
}

// boundedBody passes a body through up to a limit and fails with an upstream error beyond it, so a release of an upstream that sends no
// length is refused and not read to the metadata limit.
type boundedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n, errs.E(errs.UpstreamFailed, errs.CodeDebReleaseInvalid, "The upstream's release document is longer than 16 MiB.")
	}
	return n, err
}

func (b *boundedBody) Close() error { return b.rc.Close() }

// ExpectsType declares that the files of an archive are not web pages: whatever type an upstream gives them (text/plain for a Release,
// application/x-xz for an index, octet-stream or a CDN's own for a package) the engine does not block, however
// integrity.blockUnexpectedContentTypes is set (R-REPO-042). A page of HTML is no file of an archive.
func (remoteAdapter) ExpectsType(p formats.Path, mediaType string) bool {
	if analyzeRemote(p.Path).kind == rkNone {
		return false
	}
	mt := strings.ToLower(mediaType)
	return !strings.HasPrefix(mt, "text/html") && mt != "application/xhtml+xml"
}

// releaseFill is a release document that passed PrepareFill: what OnFill records.
type releaseFill struct {
	rel      *release.Release
	dir, doc string
	sum      [32]byte
	signer   string
	keysSet  bool
}

// invalidRelease is the 502 of a release document that is not what it must be (R-DEB-019).
func invalidRelease(detail string, cause error) error {
	return errs.E(errs.UpstreamFailed, errs.CodeDebReleaseInvalid, "The upstream's release document is not accepted: "+detail, errs.WithCause(cause))
}

// PrepareFill checks a fetched release document before anything is stored, outside every transaction (R-ARCH-019, R-DEB-019): at most
// 16 MiB; an InRelease parses as one cleartext-signed message whose Hash header, when present, matches its signatures; the text
// parses as one paragraph with a Date and a checksum section whose lines parse. With deb.upstreamKeys a signature verifies (R-DEB-020),
// and Suite or Codename equals the requested suite and Valid-Until has not passed. A failure stores nothing and answers 502; the stored
// copy stays. A Release.gpg is checked together with its Release when it is served (handlers_remote.go).
func (remoteAdapter) PrepareFill(_ context.Context, rc formats.RemoteContext, p formats.Path, _ string, body io.ReaderAt, size int64) (formats.Prepared, error) {
	rp := analyzeRemote(p.Path)
	if rp.kind != rkRelease || rp.doc == FileReleaseGPG {
		return formats.Prepared{}, nil
	}
	if size > release.MaxBytes {
		return formats.Prepared{}, invalidRelease("it is longer than 16 MiB.", nil)
	}
	doc, err := io.ReadAll(io.NewSectionReader(body, 0, size))
	if err != nil {
		return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The fetched document cannot be read.", errs.WithCause(err))
	}
	keys := SettingsOf(rc.Settings).UpstreamKeys
	var certs []openpgp.Certificate
	if keys != "" {
		if certs, err = openpgp.ParseStoredCertificates([]byte(keys), openpgp.Limits{}); err != nil {
			return formats.Prepared{}, errs.E(errs.Internal, errs.CodeInternal, "The upstream keys of the repository do not read.", errs.WithCause(err))
		}
	}
	now := time.Now()
	var (
		rel    *release.Release
		signer string
	)
	if rp.doc == FileInRelease {
		rel, signer, err = release.Signed(doc, certs, now)
	} else {
		rel, err = release.Parse(doc)
	}
	if err != nil {
		return formats.Prepared{}, releaseError(err, len(certs) > 0)
	}
	if len(certs) > 0 {
		suite := ""
		if strings.HasPrefix(rp.dir, dirDists+"/") {
			suite = suiteOf(rp.dir)
		}
		if err := rel.CheckSuite(suite, now); err != nil {
			return formats.Prepared{}, releaseError(err, false)
		}
	}
	return formats.Prepared{Data: &releaseFill{rel: rel, dir: rp.dir, doc: rp.doc, sum: sha256.Sum256(doc), signer: signer, keysSet: len(certs) > 0}}, nil
}

// signatureReason is the field error that carries the label of a failed verification to the handler, which counts it and writes the
// security event (the fill has no host to do so with).
const signatureReasonPointer = "/signature/reason"

// releaseError maps the failure of reading a release to the answer of R-DEB-019: a signature that does not verify against the keys of
// the remote is deb_upstream_signature_invalid, anything else deb_release_invalid.
func releaseError(err error, keyed bool) error {
	var ve *openpgp.VerifyError
	if errors.As(err, &ve) && keyed {
		return errs.E(errs.UpstreamFailed, errs.CodeDebUpstreamSignatureInvalid,
			"The upstream's release document is not signed by a key of deb.upstreamKeys that was valid then and now ("+ve.Reason+").",
			errs.WithFields(errs.FieldError{Pointer: signatureReasonPointer, Code: ve.Reason, Detail: ve.Detail}), errs.WithCause(err))
	}
	var re *release.Error
	if errors.As(err, &re) {
		return invalidRelease(re.Reason+".", nil)
	}
	return invalidRelease(err.Error()+".", err)
}

// SignatureReason reads the label of a failed verification from the error of a fill ("" for another error).
func SignatureReason(err error) string {
	e, ok := errs.As(err)
	if !ok || e.Code != errs.CodeDebUpstreamSignatureInvalid {
		return ""
	}
	for _, f := range e.Fields {
		if f.Pointer == signatureReasonPointer {
			return f.Code
		}
	}
	return "bad_signature"
}

// OnFill runs inside the transaction that upserts the document's cache entry, under the entry's row lock (R-ARCH-008). A release
// document is recorded with the anti-rollback decision (R-DEB-019, Q-DEB-35, Q-DEB-42): an older release than the floor leaves the
// stored document in place. A plain index is checked against the file the stored release lists, and a Packages index enqueues its
// parse in the same transaction (R-DEB-023). Everything else owns no row.
func (a remoteAdapter) OnFill(ctx context.Context, tx formats.Tx, ev formats.FillEvent) (formats.FillDecision, error) {
	rp := analyzeRemote(ev.Path.Path)
	switch rp.kind {
	case rkRelease:
		rf, ok := ev.Prepared.Data.(*releaseFill)
		if !ok {
			return formats.FillByFetchTime, nil
		}
		st, err := storeOf(ev.Data)
		if err != nil {
			return formats.FillByFetchTime, err
		}
		// The fill the entry keeps (a later fetch is stored already) records nothing (R-REPO-026).
		if !ev.StoredAt.IsZero() && ev.StoredAt.After(ev.FetchedAt) {
			return formats.FillByFetchTime, nil
		}
		if rf.rel.Date.After(ev.FetchedAt.Add(openpgp.ClockSkew)) {
			return formats.FillByFetchTime, invalidRelease("its Date lies more than 10 minutes after the time of the database.", nil)
		}
		dec, err := st.RecordRelease(ctx, tx, rf.rel.Record(ev.Repo.ID, rf.dir, rf.doc, rf.sum, rf.signer, rf.keysSet))
		if err != nil {
			return formats.FillByFetchTime, err
		}
		if dec.RolledBack {
			a.f.replicaOfPort(ev.Data).met.rollback(ev.Repo.Name.String())
			return formats.FillKeepStored, nil
		}
		return formats.FillByFetchTime, nil
	case rkIndex:
		return indexFilled(ctx, tx, ev)
	}
	return formats.FillByFetchTime, nil
}

// indexFilled checks a fetched plain index against the stored release (R-DEB-021): it must be the file the release lists for the path,
// or the fill stores nothing and answers 502 deb_index_mismatch. A Packages index enqueues its parse (R-DEB-023).
func indexFilled(ctx context.Context, tx formats.Tx, ev formats.FillEvent) (formats.FillDecision, error) {
	st, err := storeOf(ev.Data)
	if err != nil {
		return formats.FillByFetchTime, err
	}
	dir, rel, ok, err := releaseDirOf(ctx, st, ev.Repo.ID, ev.Path.Path)
	if err != nil || !ok {
		return formats.FillByFetchTime, err
	}
	listed, found, err := st.ListedFile(ctx, tx, ev.Repo.ID, dir, rel)
	if err != nil || !found {
		return formats.FillByFetchTime, err
	}
	if listed.SHA256 != ev.Digest {
		return formats.FillByFetchTime, errs.E(errs.UpstreamFailed, errs.CodeDebIndexMismatch,
			"The upstream served an index that is not the file its release lists; nothing is stored.")
	}
	if base, _, isIndex := rows.IndexBase(rel); isIndex && !rows.IsSources(base) {
		return formats.FillByFetchTime, st.EnqueueParse(ctx, tx, ev.Repo.ID, dir, base)
	}
	return formats.FillByFetchTime, nil
}

// releaseDirOf finds the release directory a path belongs to: the longest directory above the path that has a stored release
// document. ok is false when none has one. rel is the path relative to the directory.
func releaseDirOf(ctx context.Context, st RemoteStore, repoID int64, p string) (dir, rel string, ok bool, err error) {
	cands := dirCandidates(p)
	rels, err := st.Releases(ctx, repoID, cands)
	if err != nil {
		return "", "", false, err
	}
	best := -1
	for _, r := range rels {
		if r.Document == rows.DocAbsent {
			continue
		}
		for i, c := range cands {
			if c == r.Dir && i > best {
				best = i
			}
		}
	}
	if best < 0 {
		return "", "", false, nil
	}
	dir = cands[best]
	if dir == "" {
		return "", p, true, nil
	}
	return dir, strings.TrimPrefix(p, dir+"/"), true, nil
}

// dirCandidates lists the directories above a path that can be its release directory, the shortest first: below dists/ the suite and
// every directory deeper (a suite may hold slashes, "stable/updates"), in a flat repository the root and every directory above the
// file.
func dirCandidates(p string) []string {
	segs := strings.Split(p, "/")
	var out []string
	if segs[0] == dirDists {
		for i := 2; i < len(segs); i++ {
			out = append(out, strings.Join(segs[:i], "/"))
		}
		return out
	}
	out = append(out, "")
	for i := 1; i < len(segs); i++ {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return out
}

// Rewrite is the identity: the upstream's documents are passed through byte for byte, because trove never signs a remote's files and
// the client verifies them against the upstream's keys (R-DEB-019, Q-DEB-13).
func (remoteAdapter) Rewrite(_ context.Context, _ formats.RemoteContext, _ formats.Path, doc []byte, _ formats.LinkBuilder) ([]byte, error) {
	return doc, nil
}

// RevalidationRequest is the HEAD that revalidates a release document no key of the remote verifies now (R-DEB-020, Q-DEB-42): the
// engine's conditional GET would be answered 304 and renew a release nothing may use, so the document is fetched whole, without
// validators. The HEAD is answered 200 and Unchanged says the document moved, which sends the GET that follows with no validators;
// a 304 can never mark a release verified. Every other document takes the engine's conditional GET.
func (a remoteAdapter) RevalidationRequest(ctx context.Context, rc formats.RemoteContext, p formats.Path, _ string, _ formats.RemoteDocument, get *http.Request) (*http.Request, bool, error) {
	if !a.f.unverified(ctx, rc, p) {
		return nil, false, nil
	}
	req := get.Clone(ctx)
	req.Method = http.MethodHead
	return req, true, nil
}

// Unchanged says the document moved whenever this adapter asked: a release no key verifies is fetched again (see RevalidationRequest).
func (remoteAdapter) Unchanged(formats.Path, formats.RemoteDocument, *http.Response) bool {
	return false
}
