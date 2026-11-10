package sync

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/imagescan"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The vulnerability database of the image scanning (R-VULN-035): one archive, published as an OCI artifact, that the task
// vuln-sync-trivy-db copies into the object store. A worker never downloads it from the publisher itself; it fetches the copy once and
// keeps it unpacked on its local disk (imagescan.LocalDatabase). In mode repository the archive comes from a generic local repository
// and the installation makes no request at all (R-VULN-005).

// TrivyMirrorPath is the path of the archive in the mirror repository of mode repository.
const TrivyMirrorPath = "trivy/db.tar.gz"

// trivyLayerMedia is the media type of the archive's layer.
const trivyLayerMedia = "application/vnd.aquasec.trivy.db.layer.v1.tar+gzip"

// maxTrivyManifest bounds the manifest and the token answer of the publisher's registry.
const maxTrivyManifest = 1 << 20

// DBCommitter commits a spooled archive to the object store and links it in the same transaction. *blob.Store implements it.
type DBCommitter interface {
	Commit(ctx context.Context, sp *blob.Spooled, link blob.LinkFunc) (blob.CommitResult, error)
}

// RunTrivyDB is the handler of vuln-sync-trivy-db. Like the other sources it records what ended a failing run on its row, and it leaves the
// database in use as it is: the images are evaluated against the last good one until a new one has been imported completely.
func (s *Service) RunTrivyDB(ctx context.Context, run Run, committer DBCommitter) error {
	cfg := s.o.Config.Sources.Trivy
	if !s.o.Config.Enabled || cfg.Mode == "off" {
		return run.SetResult(map[string]string{"skipped": "the source is off"})
	}
	var at attempt
	res, err := guarded(ctx, s, store.SourceTrivy, advisory.EcosystemOCI, kindFull, &at, func() (Result, error) {
		return s.syncTrivy(ctx, committer, &at)
	})
	if err != nil {
		if released(ctx) {
			return context.Cause(ctx)
		}
		if ctx.Err() != nil {
			s.recordEnd(ctx, store.SourceTrivy, advisory.EcosystemOCI, kindFull, at.at, context.Cause(ctx))
			return context.Cause(ctx)
		}
		s.recordFailure(ctx, store.SourceTrivy, advisory.EcosystemOCI, kindFull, at.at, err)
		return err
	}
	return run.SetResult(res)
}

func (s *Service) syncTrivy(ctx context.Context, committer DBCommitter, at *attempt) (Result, error) {
	res := Result{Ecosystem: advisory.EcosystemOCI, Kind: kindFull}
	if err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		if err := s.st.Ensure(ctx, db, store.SourceTrivy, advisory.EcosystemOCI); err != nil {
			return err
		}
		at.at, err = s.st.Attempt(ctx, db, store.SourceTrivy, advisory.EcosystemOCI)
		return err
	}); err != nil {
		return res, err
	}
	var st store.SyncState
	if err := s.o.DB.Read(ctx, func(db q.DBTX) (err error) {
		st, _, err = s.st.Get(ctx, db, store.SourceTrivy, advisory.EcosystemOCI)
		return err
	}); err != nil {
		return res, err
	}
	cfg := s.o.Config.Sources.Trivy
	dctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	var sp *blob.Spooled
	var err error
	var unchanged bool
	if cfg.Mode == "repository" {
		sp, unchanged, err = s.trivyFromMirror(dctx, cfg, st)
	} else {
		sp, unchanged, err = s.trivyFromRegistry(dctx, cfg, st)
	}
	if err != nil {
		return res, err
	}
	if unchanged {
		return s.trivyUnchanged(ctx, res)
	}
	defer func() { _ = sp.Close() }()
	return s.importTrivy(ctx, committer, sp, res)
}

// importTrivy checks the archive, stores it and makes it the database in use, in the transaction that links its blob.
func (s *Service) importTrivy(ctx context.Context, committer DBCommitter, sp *blob.Spooled, res Result) (Result, error) {
	md, err := imagescan.ReadDBMetadata(sp.Reader())
	if err != nil {
		return res, problemf(errs.CodeArchiveLimit, err.Error(), nil)
	}
	digest := "sha256:" + sp.SHA256.Hex()
	if _, err := committer.Commit(ctx, sp, func(ctx context.Context, tx meta.Tx) error {
		return s.linkTrivy(ctx, tx, sp, digest, md)
	}); err != nil {
		return res, fmt.Errorf("commit the vulnerability database: %w", err)
	}
	res.Outcome = outcomeChanged
	res.Records = 1
	s.countRun(store.SourceTrivy, advisory.EcosystemOCI, kindFull, outcomeChanged)
	s.log.InfoContext(ctx, "vulnerability database imported", "digest", digest, "built", md.UpdatedAt)
	return res, nil
}

// linkTrivy is the link of the commit protocol: the row of the database in use, the state of the source, and the wake-up of the scan.
func (s *Service) linkTrivy(ctx context.Context, tx meta.Tx, sp *blob.Spooled, digest string, m imagescan.DBMetadata) error {
	var blobKey [32]byte
	copy(blobKey[:], sp.SHA256.Bytes())
	if err := store.PutDB(ctx, tx, store.ImageDB{Digest: digest, Blob: blobKey, Size: sp.Size, UpdatedAt: m.UpdatedAt, NextUpdate: m.NextUpdate}); err != nil {
		return err
	}
	db, err := store.DBOf(tx)
	if err != nil {
		return err
	}
	cur, err := s.st.Lock(ctx, db, store.SourceTrivy, advisory.EcosystemOCI)
	if err != nil {
		return err
	}
	now := s.now()
	next := cur
	next.State, next.SyncedAt, next.FullSyncedAt = store.StateComplete, now, now
	next.ETag, next.Records = digest, 1
	next.DataEpoch = cur.DataEpoch + 1
	if err := s.st.Save(ctx, db, next); err != nil {
		return err
	}
	if err := s.o.Changes.Notify(tx, cluster.AreaVulnData, vuln.DataChangePayloadVersion, vuln.DataChange{Ecosystem: advisory.EcosystemOCI, Epoch: next.DataEpoch}); err != nil {
		return err
	}
	if s.o.Jobs == nil {
		return nil
	}
	_, err = s.o.Jobs.Enqueue(ctx, tx, jobs.EnqueueSpec{Kind: vuln.KindImageScan, DedupKey: vuln.DedupImageScan})
	return err
}

// trivyUnchanged records a run that found the database in use still current. A publisher that stopped publishing shows as stale, because
// the state keeps the time the source last succeeded and the sources API and alert compare that with the age of the data separately.
func (s *Service) trivyUnchanged(ctx context.Context, res Result) (Result, error) {
	err := s.o.DB.InTx(ctx, func(tx metaTx) error {
		db, err := store.DBOf(tx)
		if err != nil {
			return err
		}
		cur, err := s.st.Lock(ctx, db, store.SourceTrivy, advisory.EcosystemOCI)
		if err != nil {
			return err
		}
		cur.SyncedAt = s.now()
		return s.st.Save(ctx, db, cur)
	})
	if err != nil {
		return res, err
	}
	res.Outcome = outcomeUnchanged
	s.countRun(store.SourceTrivy, advisory.EcosystemOCI, kindFull, outcomeUnchanged)
	return res, nil
}

// trivyFromMirror reads the archive of mode repository: unchanged when the item's digest is the one imported last, and a missing item
// leaves the source as it is.
func (s *Service) trivyFromMirror(ctx context.Context, cfg config.VulnerabilityTrivy, st store.SyncState) (*blob.Spooled, bool, error) {
	if s.o.Mirror == nil {
		return nil, false, problemf(errs.CodeMirrorRepositoryInvalid, "the mirror repository is not available on this replica", nil)
	}
	item, err := s.o.Mirror.Stat(ctx, cfg.Repository, TrivyMirrorPath)
	if err != nil {
		return nil, false, err
	}
	if item == nil {
		return nil, true, nil
	}
	digest := "sha256:" + hex.EncodeToString(item.SHA256[:])
	if st.ETag == digest && st.Complete() {
		return nil, true, nil
	}
	rc, err := item.Open(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rc.Close() }()
	sp, err := s.o.Spool.Ingest(ctx, rc, blob.IngestOptions{ContentLength: item.Size, MaxSize: int64(cfg.MaxArchiveSize)})
	return sp, false, err
}

// trivyFromRegistry reads the artifact of mode online from the registry that publishes it, through the outbound client of the advisory
// sources: its egress rules, proxy and redirect limits apply, and the only thing the request says about the installation is that it asks
// for the public database.
func (s *Service) trivyFromRegistry(ctx context.Context, cfg config.VulnerabilityTrivy, st store.SyncState) (*blob.Spooled, bool, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, false, problemf(errs.CodeArchiveLimit, "vulnerabilities.sources.trivy.url is not an https URL", nil)
	}
	token := ""
	manifest, status, challenge, err := s.trivyGet(ctx, cfg.URL+"manifests/"+cfg.Reference, "application/vnd.oci.image.manifest.v1+json", token)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusUnauthorized {
		if token, err = s.trivyToken(ctx, base, challenge); err != nil {
			return nil, false, err
		}
		if manifest, status, _, err = s.trivyGet(ctx, cfg.URL+"manifests/"+cfg.Reference, "application/vnd.oci.image.manifest.v1+json", token); err != nil {
			return nil, false, err
		}
	}
	if status != http.StatusOK {
		return nil, false, problemf(errs.CodeUpstreamFailed, fmt.Sprintf("the registry answered the database manifest with %d", status), nil)
	}
	layer, err := trivyLayer(manifest)
	if err != nil {
		return nil, false, problemf(errs.CodeArchiveLimit, err.Error(), nil)
	}
	if layer.Digest == st.ETag && st.Complete() {
		return nil, true, nil
	}
	if layer.Size > int64(cfg.MaxArchiveSize) {
		return nil, false, problemf(errs.CodeArchiveTooLarge, fmt.Sprintf("the database is %d bytes, vulnerabilities.sources.trivy.max_archive_size is %d", layer.Size, int64(cfg.MaxArchiveSize)), nil)
	}
	return s.trivyBlob(ctx, cfg, layer.Digest, layer.Size, token)
}

type trivyDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func trivyLayer(manifest []byte) (trivyDescriptor, error) {
	var m struct {
		Layers []trivyDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return trivyDescriptor{}, fmt.Errorf("the database manifest: %w", err)
	}
	for _, l := range m.Layers {
		if l.MediaType == trivyLayerMedia {
			if !strings.HasPrefix(l.Digest, "sha256:") {
				return trivyDescriptor{}, errors.New("the database layer has no SHA-256 digest")
			}
			return l, nil
		}
	}
	return trivyDescriptor{}, errors.New("the manifest holds no layer of the database's media type")
}

func (s *Service) trivyBlob(ctx context.Context, cfg config.VulnerabilityTrivy, digest string, size int64, token string) (*blob.Spooled, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL+"blobs/"+digest, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, problemf(errs.CodeUpstreamFailed, fmt.Sprintf("the registry answered the database layer with %d", resp.StatusCode), nil)
	}
	sum, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		return nil, false, err
	}
	sp, err := s.o.Spool.Ingest(ctx, resp.Body, blob.IngestOptions{ContentLength: size, MaxSize: int64(cfg.MaxArchiveSize), Expect: blob.Expected{SHA256: sum}})
	return sp, false, err
}

// trivyGet makes one request of the registry protocol and answers its body within the bound, its status and its challenge.
func (s *Service) trivyGet(ctx context.Context, target, accept, token string) ([]byte, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.o.HTTP.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTrivyManifest))
	if err != nil {
		return nil, 0, "", err
	}
	return body, resp.StatusCode, resp.Header.Get("WWW-Authenticate"), nil
}

// trivyToken answers a Bearer challenge of the registry. The realm must be https: the outbound client's egress rules decide whether its
// host may be reached at all.
func (s *Service) trivyToken(ctx context.Context, base *url.URL, challenge string) (string, error) {
	params := parseChallenge(challenge)
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return "", problemf(errs.CodeUpstreamFailed, "the registry names no usable token service for "+base.Host, nil)
	}
	v := realm.Query()
	if svc := params["service"]; svc != "" {
		v.Set("service", svc)
	}
	if sc := params["scope"]; sc != "" {
		v.Set("scope", sc)
	}
	realm.RawQuery = v.Encode()
	body, status, _, err := s.trivyGet(ctx, realm.String(), "application/json", "")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", problemf(errs.CodeUpstreamFailed, fmt.Sprintf("the token service answered %d", status), nil)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", problemf(errs.CodeUpstreamFailed, "the token service's answer cannot be read", nil)
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	return tok.AccessToken, nil
}

// parseChallenge reads the parameters of a Bearer challenge.
func parseChallenge(h string) map[string]string {
	out := map[string]string{}
	_, rest, ok := strings.Cut(h, " ")
	if !ok {
		return out
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			out[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	return out
}
