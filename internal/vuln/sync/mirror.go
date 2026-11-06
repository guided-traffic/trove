package sync

import (
	"bytes"
	"context"
	"encoding/hex"
	"github.com/guided-traffic/trove/internal/qname"
	"io"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln/match"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The mode repository of R-VULN-005: a source reads its files from a generic local repository of the installation instead of
// the network. The paths in the repository are osv/<ecosystem>/all.zip and kev/known_exploited_vulnerabilities.json.

// MirrorItem is a file of the mirror repository.
type MirrorItem struct {
	Size int64
	// SHA256 is the digest of the stored content, read from PostgreSQL without opening the object.
	SHA256 [32]byte
	// DeployedBy is the principal that deployed the item, "<kind>:<name>"; empty when the row has none.
	DeployedBy string
	// Open opens the content.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// Mirror reads the files of mode repository.
type Mirror interface {
	// Stat resolves the file at path of the repository. A nil item with a nil error is a file that is not there, which leaves
	// the source as it is; a repository that is not a generic local repository is an error with the code
	// mirror_repository_invalid.
	Stat(ctx context.Context, repoKey, path string) (*MirrorItem, error)
}

// RepositoryMirror is the [Mirror] over the repository model and the item store of the installation. The getters are read at
// run time: the item store is built after the area that owns the sync.
type RepositoryMirror struct {
	Repos func() repository.Reader
	Items func() *items.Store
}

var _ Mirror = RepositoryMirror{}

// Stat implements [Mirror].
func (m RepositoryMirror) Stat(ctx context.Context, key, path string) (*MirrorItem, error) {
	var repos repository.Reader
	var itemStore *items.Store
	if m.Repos != nil {
		repos = m.Repos()
	}
	if m.Items != nil {
		itemStore = m.Items()
	}
	if repos == nil || itemStore == nil {
		return nil, errs.E(errs.Unavailable, errs.CodeInternal, "the item store of this replica is not wired yet")
	}
	name, perr := qname.ParseRepo(key)
	if perr != nil {
		return nil, problemf(errs.CodeMirrorRepositoryInvalid, "the repository "+key+" is not a repository name <project>/<key>", perr)
	}
	repo, err := repos.ByName(ctx, name)
	if e, ok := errs.As(err); ok && e.Kind == errs.NotFound {
		return nil, problemf(errs.CodeMirrorRepositoryInvalid, "the repository "+key+" does not exist", err)
	}
	if err != nil {
		return nil, err
	}
	if repo.Class != repository.Local || repo.Format != "generic" || repo.Disabled {
		return nil, problemf(errs.CodeMirrorRepositoryInvalid, "the repository "+key+" is not an enabled generic local repository", nil)
	}
	it, found, err := itemStore.Live(ctx, items.RepoOf(repo), path)
	if err != nil {
		return nil, err
	}
	if !found || it.Kind != items.KindFile {
		return nil, nil
	}
	if it, err = itemStore.Usable(ctx, it); err != nil { // a missing object that is back is served again (Q-STOR-153)
		return nil, err
	}
	by := it.ModifiedBy
	if by == "" {
		by = it.CreatedBy
	}
	return &MirrorItem{
		Size: it.Size, SHA256: it.Digest, DeployedBy: by,
		Open: func(ctx context.Context) (io.ReadCloser, error) { return itemStore.Open(ctx, it, nil) },
	}, nil
}

// stat resolves a mirrored file through the configured Mirror.
func (s *Service) stat(ctx context.Context, repoKey, path string) (*MirrorItem, error) {
	if s.o.Mirror == nil || repoKey == "" {
		return nil, problemf(errs.CodeMirrorRepositoryInvalid, "no mirror repository is configured for the source", nil)
	}
	return s.o.Mirror.Stat(ctx, repoKey, path)
}

// fullMirror is a full sync from the item osv/<ecosystem>/all.zip of the mirror repository (R-VULN-005): the item's SHA-256 is
// compared with archive_sha256, an item that differs runs steps 2 to 5 of the full sync reading the item's blob instead of an
// HTTP body, a missing item leaves the ecosystem as it is, and the import's audit event names the deployer.
func (s *Service) fullMirror(ctx context.Context, run Run, p Params, eco string, ad match.Adapter, st store.SyncState, cp *checkpoint) (Result, error) {
	res := Result{Ecosystem: eco, Kind: kindFull}
	started := s.now()
	item, err := s.stat(ctx, s.o.Config.Sources.OSV.Repository, "osv/"+eco+"/all.zip")
	if err != nil {
		return res, err
	}
	if item == nil {
		res.Outcome, res.Note = outcomeUnchanged, "osv/"+eco+"/all.zip is not in the mirror repository"
		s.countRun(store.SourceOSV, eco, kindFull, outcomeUnchanged)
		return res, nil
	}
	if st.Complete() && bytes.Equal(st.ArchiveSHA256, item.SHA256[:]) && !adapterStale(eco, st) {
		return s.finishUnchanged(ctx, eco, kindFull)
	}
	if item.Size > int64(s.o.Config.Sources.OSV.MaxArchiveSize) {
		return res, problemf(errs.CodeArchiveTooLarge, "the item is larger than vulnerabilities.sources.osv.max_archive_size", nil)
	}
	rc, err := item.Open(ctx)
	if err != nil {
		return res, err
	}
	defer rc.Close()
	arch, err := s.spooled(ctx, rc, item.Size)
	if err != nil {
		return res, err
	}
	defer arch.Close()
	if arch.sha256 != item.SHA256 {
		return res, problemf(errs.CodeArchiveDigestMismatch, "the content of the item differs from its stored digest", nil)
	}
	arch.ident = "sha256:" + hex.EncodeToString(item.SHA256[:])
	return s.applyArchive(ctx, run, p, cp, fullInput{
		eco: eco, ad: ad, state: st, arch: arch, started: started,
		mirror: &mirrorFacts{sha256: item.SHA256, deployedBy: item.DeployedBy},
	})
}
