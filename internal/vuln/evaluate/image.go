package evaluate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// ImageMatch is one advisory that a detection found to affect an image, in the terms of the lifecycle of findings.
type ImageMatch struct {
	ID             string
	Severity       advisory.Severity
	CVSS           float64
	KnownExploited bool
	ModifiedUs     int64
	FixAvailable   bool
	Fixed          []string
	// Components is the JSON of the packages of the image the advisory affects, stored on the finding.
	Components json.RawMessage
}

// ImageApply is a finished detection of one manifest.
type ImageApply struct {
	RepoID int64
	Name   string
	// Digest is the manifest's digest as the registry spells it: the version of the finding (R-VULN-012).
	Digest  string
	Trigger vuln.Trigger
	Matches []ImageMatch
	// DataComplete is true when the detection ran against a complete database, the condition for resolving a finding (R-VULN-011).
	DataComplete bool
}

// ImageScope reads the repository of a manifest and says whether its images are scanned at all (R-VULN-002, R-VULN-010): false for a
// repository that is gone or not active, whose scanning is off, or with matchPublicAdvisories never.
func (s *Service) ImageScope(ctx context.Context, tx meta.Tx, repoID int64) (bool, error) {
	ev, err := store.EvalOf(tx)
	if err != nil {
		return false, err
	}
	row, found, err := ev.RepositoryInputs(ctx, repoID)
	if err != nil || !found || row.Lifecycle != string(repository.Active) || repository.Class(row.Class) == repository.Virtual {
		return false, err
	}
	v, err := viewOfRow(row, Ecosystem{Name: advisory.EcosystemOCI})
	if err != nil {
		return false, err
	}
	return v.Settings.Enabled && v.Settings.MatchPublicAdvisories != vuln.MatchNever, nil
}

// ApplyImage brings the findings of one manifest in line with a detection, in the caller's transaction, through the lifecycle every
// format's findings go through (R-VULN-012): first detection, re-detection with the earlier status, the reopening of a dismissal on a
// material change, the resolution of what is no longer found and the journal of the blocks it sets. Nothing here is particular to images
// except the identity: the finding's version is the manifest's digest.
func (s *Service) ApplyImage(ctx context.Context, tx meta.Tx, in ImageApply) error {
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	row, found, err := ev.RepositoryInputs(ctx, in.RepoID)
	if err != nil || !found {
		return err
	}
	eco := Ecosystem{Name: advisory.EcosystemOCI}
	rv, err := viewOfRow(row, eco)
	if err != nil {
		return err
	}
	images, err := store.ImageTxOf(tx)
	if err != nil {
		return err
	}
	existing, err := images.Findings(ctx, in.RepoID, in.Name, in.Digest)
	if err != nil {
		return err
	}
	pkgID, err := images.PackageID(ctx, in.RepoID, in.Name)
	if err != nil {
		return err
	}
	found2 := make(map[string]*matched, len(in.Matches))
	for _, m := range in.Matches {
		found2[m.ID] = &matched{
			ID: m.ID, Kind: advisory.KindVulnerability, Severity: m.Severity, CVSS: m.CVSS, KnownExploited: m.KnownExploited,
			ModifiedUs: m.ModifiedUs, FixAvailable: m.FixAvailable, Fixed: m.Fixed, Components: m.Components,
		}
	}
	list := make([]*store.Finding, len(existing))
	for i := range existing {
		list[i] = &existing[i]
	}
	started := time.Now()
	r := &run{s: s, tx: tx, ev: ev, eco: eco, repos: func(id int64) (repoView, bool) { return rv, id == rv.ID }, trigger: in.Trigger, canResolve: in.DataComplete, rule: s.rule}
	v := store.StoredVersion{RepoID: in.RepoID, PackageID: pkgID, Name: in.Name, Version: in.Digest}
	r.st.versions = 1
	if err := r.apply(ctx, rv, v, found2, list, nil, nil, false); err != nil {
		return fmt.Errorf("the findings of %s@%s in repository %s: %w", in.Name, in.Digest, strconv.FormatInt(in.RepoID, 10), err)
	}
	for _, m := range in.Matches {
		if err := images.SetComponents(ctx, in.RepoID, in.Name, in.Digest, m.ID, m.Components); err != nil {
			return err
		}
	}
	r.finish(started)
	return nil
}

// ResolveImageFindings resolves findings of images by id with version_removed: the manifest is gone from the repository.
func (s *Service) ResolveImageFindings(ctx context.Context, tx meta.Tx, ids []string) error {
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	done, err := ev.Resolve(ctx, ids, vuln.ResolvedVersionRemoved)
	if err != nil {
		return err
	}
	return s.resolvedEvents(ctx, ev, done, vuln.ResolvedVersionRemoved)
}

// ResolveImageRepository resolves every open finding of a repository with scanning_disabled (R-VULN-002).
func (s *Service) ResolveImageRepository(ctx context.Context, tx meta.Tx, repoID int64) error {
	ev, err := store.EvalOf(tx)
	if err != nil {
		return err
	}
	done, err := ev.ResolveRepo(ctx, repoID)
	if err != nil {
		return err
	}
	return s.resolvedEvents(ctx, ev, done, vuln.ResolvedScanningDisabled)
}
