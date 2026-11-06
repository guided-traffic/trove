// Package enforce applies the download policy of the vulnerability scanning (R-VULN-015, R-VULN-016): it resolves the policy of a
// repository for the item lookup, turns the lookup's blocking row into a verdict, evaluates a remote miss before it is
// fetched, and renders the refusal: the 403 with its code and text, the metric trove_vuln_downloads_blocked_total and the
// audit event vulnerability.download_blocked.
//
// Every route that serves a stored item calls it after its serve-time check; none decides on its own. A nil *Enforcer is a
// replica that has not wired the scanning: it resolves no policy, so no lookup reads a block and nothing is refused.
//
// The package reads no table on the serving path: the policy comes from the replica's snapshot of the repository, the data
// state from the replica's cache (vuln.States) and the blocking finding from the item lookup. A query runs only when a
// download is refused, to name the package and version in the text (store.Enforce), and its answer is kept for a minute.
package enforce

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/metrics"
)

// DedupWindow is the window of the audit deduplication (R-VULN-016, R-OPS-015): one event per repository, version and client
// address.
const DedupWindow = time.Minute

// maxTracked bounds the maps of the deduplication and of the names.
const maxTracked = 4096

// NameReader names a stored version for the text of a refusal; *store.Enforce implements it.
type NameReader interface {
	VersionNames(ctx context.Context, versionID int64) (pkg, version string, found bool, err error)
}

// Options configure [New].
type Options struct {
	// Enabled and Enforcement are vulnerabilities.enabled and vulnerabilities.enforcement.
	Enabled     bool
	Enforcement vuln.Enforcement
	// States answers the state of an ecosystem's data; nil is a replica that has not wired the scanning, which resolves
	// every policy as disabled.
	States vuln.States
	// Covered is the repository-level part of the scope rule (R-VULN-010): whether versions of the repository can be in
	// scope. A repository that is not covered is never refused for missing data (R-VULN-015, Q-VULN-75). Nil covers every
	// repository, which is the fail-closed answer.
	Covered func(repository.Repository) bool
	// Evaluator evaluates a remote miss; nil evaluates none (only the missing-data rule applies).
	Evaluator vuln.Evaluator
	// Names reads the package and version of a stored version when a download is refused; nil names the path only.
	Names NameReader
	// Metrics and Audit are optional.
	Metrics *metrics.Metrics
	Audit   audit.Recorder
	// BaseURL is server.base_url, the origin of the UI link in the refusal text; empty gives a path-only link.
	BaseURL string
	Log     *slog.Logger
	// Now replaces the clock, for tests.
	Now func() time.Time
}

// Enforcer applies the policy. It is safe for concurrent use.
type Enforcer struct {
	o   Options
	log *slog.Logger

	mu    sync.Mutex
	seen  map[string]time.Time
	names map[int64]namedVersion
}

type namedVersion struct {
	pkg, version string
	at           time.Time
}

// New returns the enforcer.
func New(o Options) *Enforcer {
	if o.Now == nil {
		o.Now = time.Now
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	if o.Enforcement == "" {
		o.Enforcement = vuln.EnforceBlocks
	}
	return &Enforcer{o: o, log: log, seen: map[string]time.Time{}, names: map[int64]namedVersion{}}
}

// PolicyOf is the download policy of a repository, from the replica's snapshot of it (no query). It is the zero, disabled
// policy for a nil enforcer and for one without a data state.
func (e *Enforcer) PolicyOf(r repository.Repository) vuln.Policy {
	if e == nil || e.o.States == nil {
		return vuln.Policy{}
	}
	p := r.VulnerabilityPolicy(e.o.Enabled, e.o.Enforcement)
	p.Covered = e.o.Covered == nil || e.o.Covered(r)
	return p
}

// Decide is the verdict on a download of a stored item: the policy and blocking row the lookup returned, and the state of
// the ecosystem's data (R-VULN-015). An item that belongs to no version (versionID 0) is no download of a version.
func (e *Enforcer) Decide(p vuln.Policy, row vuln.BlockRow, versionID int64) vuln.Block {
	if e == nil || e.o.States == nil || !p.Enabled || versionID == 0 {
		return vuln.Block{}
	}
	return e.verdict(p, row)
}

// verdict is [vuln.Verdict] that reads the state of the ecosystem's data only when it decides: the state matters only to a
// repository whose onMissingData is block, and only when no finding blocks already, so the download of a version in a
// repository that allows missing data never touches the state cache (R-VULN-015, R-ARCH-032).
func (e *Enforcer) verdict(p vuln.Policy, row vuln.BlockRow) vuln.Block {
	if !p.Covered && p.OnMissingData == vuln.MissingBlock {
		// Not covered: the version is internal or not covered, never pending, so the missing-data rule does not apply
		// (R-VULN-015); complete data makes the rule a no-op without reading the state.
		return vuln.Verdict(p, row, advisory.DataState{Complete: true})
	}
	if p.OnMissingData != vuln.MissingBlock {
		return vuln.Verdict(p, row, advisory.DataState{})
	}
	if b := vuln.Verdict(p, row, advisory.DataState{Complete: true}); b.Active() {
		return b
	}
	return vuln.Verdict(p, row, e.o.States.State(p.Ecosystem))
}

// Download is the refused download, as the route that serves it knows it.
type Download struct {
	// Addressed is the repository the client addressed, which the caller can read: the text never names a member of a
	// virtual repository or any other repository (R-VULN-016).
	Addressed string
	// Store is the repository that stores the version, RepoID its id and Format its format; they are the audit event's
	// repository and the metric's label.
	Store  string
	RepoID string
	Format string
	// Path is the item's path; VersionID the stored version (0: the version is named by Package and Version).
	Path      string
	VersionID int64
	// Package and Version are the normalized names when the route knows them (a remote miss); a stored version's are read
	// when empty.
	Package, Version string
	// Ecosystem names the advisory page of a miss without a finding.
	Ecosystem string
	// Stored is false for a remote miss refused before the fetch.
	Stored bool
	Block  vuln.Block
	Actor  audit.Actor
	Source audit.Source
	// Endpoint is the endpoint the request arrived on.
	Endpoint string
}

// Refuse counts the block, records the audit event (at most once a minute per repository, version and client address) and
// returns the refusal; it returns nil when nothing blocks and in the mode report, where the block is counted and recorded
// with enforced false and the download goes on.
func (e *Enforcer) Refuse(ctx context.Context, d Download) error {
	if e == nil || !d.Block.Active() {
		return nil
	}
	pkg, version := e.named(ctx, d)
	e.count(d)
	e.record(ctx, d, pkg, version)
	if !d.Block.Refused() {
		return nil
	}
	return refusal(e.o.BaseURL, d, pkg, version)
}

// named returns the package and version of the refused download: those the route knows, else those of the stored version.
func (e *Enforcer) named(ctx context.Context, d Download) (pkg, version string) {
	if d.Package != "" && d.Version != "" {
		return d.Package, d.Version
	}
	if d.VersionID == 0 || e.o.Names == nil {
		return d.Package, d.Version
	}
	now := e.o.Now()
	e.mu.Lock()
	n, ok := e.names[d.VersionID]
	e.mu.Unlock()
	if ok && now.Sub(n.at) < DedupWindow {
		return n.pkg, n.version
	}
	p, v, found, err := e.o.Names.VersionNames(ctx, d.VersionID)
	if err != nil {
		e.log.WarnContext(ctx, "the blocked version could not be named", "repository", d.Store, "error", err)
		return "", ""
	}
	if !found {
		return "", ""
	}
	e.mu.Lock()
	if len(e.names) >= maxTracked {
		clear(e.names)
	}
	e.names[d.VersionID] = namedVersion{pkg: p, version: v, at: now}
	e.mu.Unlock()
	return p, v
}

func (e *Enforcer) count(d Download) {
	if e.o.Metrics == nil {
		return
	}
	mode := string(vuln.EnforceBlocks)
	if !d.Block.Enforced {
		mode = string(vuln.EnforceReport)
	}
	e.o.Metrics.DownloadsBlocked.WithLabelValues(d.Format, string(d.Block.Reason), mode).Inc()
}

func (e *Enforcer) record(ctx context.Context, d Download, pkg, version string) {
	if e.o.Audit == nil {
		return
	}
	what := pkg + "@" + version
	if pkg == "" || version == "" {
		what = d.Path
	}
	if !e.first(d.Store + "\x00" + what + "\x00" + d.Source.ClientAddr) {
		return
	}
	details := map[string]any{
		"package": pkg, "version": version, "reason": string(d.Block.Reason), "enforced": d.Block.Enforced, "stored": d.Stored,
	}
	if d.Block.AdvisoryID != "" {
		details["advisory"] = d.Block.AdvisoryID
	}
	body, err := json.Marshal(details)
	if err != nil { // cannot happen: strings and booleans
		return
	}
	outcome := audit.Denied
	if !d.Block.Enforced {
		outcome = audit.Success
	}
	e.o.Audit.Record(ctx, audit.Event{
		Type: audit.EventVulnDownloadBlocked, Actor: d.Actor, Outcome: outcome, Reason: string(d.Block.Reason), Source: d.Source,
		Subject:    audit.Subject{Kind: "repository", ID: d.RepoID, Name: d.Store},
		Repository: d.Store, Path: d.Path, Endpoint: d.Endpoint, Details: body,
	})
}

// first reports whether key has not been seen within the dedup window, and marks it seen.
func (e *Enforcer) first(key string) bool {
	now := e.o.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.seen[key]; ok && now.Sub(t) < DedupWindow {
		return false
	}
	if len(e.seen) > maxTracked {
		for k, t := range e.seen {
			if now.Sub(t) >= DedupWindow {
				delete(e.seen, k)
			}
		}
	}
	e.seen[key] = now
	return true
}

// Miss is a remote artifact that is about to be fetched.
type Miss struct {
	Repo repository.Repository
	// Addressed is the key of the repository the client addressed, which the refusal names (R-VULN-016); empty means Repo
	// itself.
	Addressed string
	// Package and Version are the normalized names of the format's classification; a miss without a version is no
	// download of a version and is not gated.
	Package, Version string
	Path             string
	Actor            audit.Actor
	Source           audit.Source
	Endpoint         string
}

// GateMiss is the gate of the remote miss (R-VULN-015 "Remote miss"): the version a path maps to is evaluated before anything
// is fetched, and the rule of the item lookup applies. It returns the refusal, or nil when the fetch may go on (also in the
// mode report, after counting the would-be block). A blocked version is not fetched, stored or served.
func (e *Enforcer) GateMiss(ctx context.Context, m Miss) error {
	if e == nil || e.o.States == nil || m.Version == "" {
		return nil
	}
	return e.gate(ctx, e.PolicyOf(m.Repo), m)
}

// gate is GateMiss for a resolved policy.
func (e *Enforcer) gate(ctx context.Context, p vuln.Policy, m Miss) error {
	if !p.Enabled {
		return nil
	}
	var block vuln.Block
	if e.o.Evaluator != nil {
		b, err := e.o.Evaluator.EvaluateMiss(ctx, vuln.Miss{RepoKey: m.Repo.Name, Format: m.Repo.Format, Name: m.Package, Version: m.Version})
		if err != nil {
			return err
		}
		if b.Active() {
			// The evaluation names the block; whether it is enforced is the installation's mode, not the evaluation's.
			block = vuln.Block{Reason: b.Reason, FindingID: b.FindingID, AdvisoryID: b.AdvisoryID, Enforced: p.Enforcement != vuln.EnforceReport}
		}
	}
	if !block.Active() {
		block = e.verdict(p, vuln.BlockRow{})
	}
	addressed := m.Addressed
	if addressed == "" {
		addressed = m.Repo.Name
	}
	return e.Refuse(ctx, Download{
		Addressed: addressed, Store: m.Repo.Name, RepoID: m.Repo.ID, Format: m.Repo.Format, Path: m.Path,
		Package: m.Package, Version: m.Version, Ecosystem: p.Ecosystem, Stored: false, Block: block,
		Actor: m.Actor, Source: m.Source, Endpoint: m.Endpoint,
	})
}

// refusal builds the 403 of R-VULN-016: the code, a text that names the package, version, repository, reason and where to
// look, the advisory and finding as extension members of the problem, and Cache-Control: no-store so that no cache keeps the
// refusal after a dismissal.
func refusal(baseURL string, d Download, pkg, version string) error {
	name := pkg
	if pkg != "" && version != "" {
		name = pkg + "@" + version
	}
	if name == "" {
		name = d.Path
	}
	opts := []errs.Option{errs.WithHeader("Cache-Control", "no-store")}
	if d.Block.AdvisoryID != "" {
		opts = append(opts, errs.WithExtension("advisory", d.Block.AdvisoryID))
	}
	if d.Block.FindingID != "" {
		opts = append(opts, errs.WithExtension("findingId", d.Block.FindingID))
	}
	text := fmt.Sprintf("trove blocked %s in %s: %s. Details: %s", name, d.Addressed, reasonText(d.Block), link(baseURL, d))
	return errs.E(errs.Forbidden, d.Block.Code(), oneLine(text), opts...)
}

func reasonText(b vuln.Block) string {
	var s string
	switch b.Reason {
	case vuln.BlockMalicious:
		s = "malicious package"
	case vuln.BlockTriage:
		s = "blocked in triage"
	default:
		return "no advisory data has been imported for this ecosystem yet and the repository blocks what it cannot evaluate"
	}
	if b.AdvisoryID != "" {
		s += " (" + b.AdvisoryID + ")"
	}
	return s
}

// link is the UI page the text points to: the finding when there is one, the advisory page for a miss without a finding, the
// report otherwise.
func link(baseURL string, d Download) string {
	base := strings.TrimRight(baseURL, "/")
	switch {
	case d.Block.FindingID != "":
		return base + "/ui/vulnerabilities/findings/" + url.PathEscape(d.Block.FindingID)
	case d.Block.AdvisoryID != "" && d.Ecosystem != "":
		return base + "/ui/vulnerabilities/advisories/" + url.PathEscape(d.Ecosystem) + "/" + url.PathEscape(d.Block.AdvisoryID)
	}
	return base + "/ui/vulnerabilities"
}

// oneLine replaces control characters, so that a package name cannot split a header or a line of a body.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
