package report

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// Source names of the source state.
const (
	SourceOSV = "osv"
	SourceKEV = "kev"
	// SourceTrivy is the vulnerability database of the image scanning; it has one ecosystem, OCI.
	SourceTrivy = "trivy"
)

// SourceView is one source and ecosystem as the sources operation shows it (R-VULN-021).
type SourceView struct {
	Source, Ecosystem, Mode, State string
	// Repository is the mirror repository in the mode repository, "" otherwise.
	Repository             string
	Stale                  bool
	SyncedAt, FullSyncedAt *time.Time
	AttemptedAt, NextRunAt *time.Time
	Failures               int
	// LastError is the problem object of the last failed run, nil when there is none.
	LastError                      json.RawMessage
	Records, Rejected, Unevaluable int
	// FullRejected is the number of records the last full sync rejected; Rejected is that of the last run of any kind.
	FullRejected int
}

// SourceConfig is what the view of the sources reads of the configuration.
type SourceConfig struct {
	// Enabled is vulnerabilities.enabled: while false every mode is off.
	Enabled bool
	// OSVMode and KEVMode are the modes of the sources (online, repository, off).
	OSVMode, KEVMode string
	// TrivyMode is the mode of the vulnerability database of the image scanning; "" when the images are not scanned.
	TrivyMode string
	// Ecosystems are the ecosystems the OSV source imports (config EffectiveEcosystems).
	Ecosystems []string
	// StaleAfter is vulnerabilities.stale_after; zero never marks a source stale.
	StaleAfter time.Duration
}

// SourceViews is the one view of vuln_source_state, for the sources operation and the system information (A6): an entry for the
// KEV catalog and for every configured ecosystem, with state empty where the source never ran (a restore removes the rows), and
// for a stored row of an ecosystem the configuration no longer names, with the mode off, because nothing syncs it; in the order of
// source and ecosystem. The mirror repository and the next run are the operation's own and are not set here.
func SourceViews(cfg SourceConfig, rows []store.SourceRow, now time.Time) []SourceView {
	have := map[string]store.SourceRow{}
	for _, r := range rows {
		have[r.Source+"\x00"+r.Ecosystem] = r
	}
	configured := map[string]bool{SourceKEV + "\x00": true}
	for _, e := range cfg.Ecosystems {
		configured[SourceOSV+"\x00"+e] = true
	}
	if cfg.TrivyMode != "" {
		configured[SourceTrivy+"\x00"+advisory.EcosystemOCI] = true
	}
	keys := map[string]bool{}
	for k := range configured {
		keys[k] = true
	}
	for k := range have {
		keys[k] = true
	}
	order := make([]string, 0, len(keys))
	for k := range keys {
		order = append(order, k)
	}
	sort.Strings(order)
	out := make([]SourceView, 0, len(order))
	for _, k := range order {
		r, ok := have[k]
		if !ok {
			src, eco, _ := strings.Cut(k, "\x00")
			r = store.SourceRow{Source: src, Ecosystem: eco, State: "empty"}
		}
		mode := "off"
		switch {
		case !cfg.Enabled || !configured[k]:
		case r.Source == SourceKEV:
			mode = cfg.KEVMode
		case r.Source == SourceTrivy:
			mode = cfg.TrivyMode
		default:
			mode = cfg.OSVMode
		}
		out = append(out, SourceView{
			Source: r.Source, Ecosystem: r.Ecosystem, Mode: mode, State: r.State, SyncedAt: r.SyncedAt,
			FullSyncedAt: r.FullSyncedAt, AttemptedAt: r.AttemptedAt, Failures: r.Failures, LastError: r.LastError,
			Records: r.Records, Rejected: r.Rejected, Unevaluable: r.Unevaluable, FullRejected: r.FullRejected,
			Stale: cfg.StaleAfter > 0 && r.SyncedAt != nil && now.Sub(*r.SyncedAt) > cfg.StaleAfter,
		})
	}
	return out
}

// Sources returns the state of every configured source and ecosystem, in the order of source and ecosystem. nextRun answers the
// next scheduled run of a task by its name (vuln.TaskSyncOSV, vuln.TaskSyncKEV), nil for none.
func (s *Service) Sources(ctx context.Context, nextRun func(task string) *time.Time) ([]SourceView, error) {
	rows, err := s.st.Sources(ctx)
	if err != nil {
		return nil, err
	}
	out := SourceViews(s.sourceConfig(), rows, s.now())
	for i := range out {
		v := &out[i]
		if v.Mode == "repository" {
			v.Repository = s.cfg.OSVRepository
			if v.Source == SourceKEV {
				v.Repository = s.cfg.KEVRepository
			}
		}
		if nextRun != nil && v.Mode != "off" {
			task := vuln.TaskSyncOSV
			if v.Source == SourceKEV {
				task = vuln.TaskSyncKEV
			}
			v.NextRunAt = nextRun(task)
		}
	}
	return out, nil
}

func (s *Service) sourceConfig() SourceConfig {
	return SourceConfig{
		Enabled: s.cfg.Enabled, OSVMode: s.cfg.OSVMode, KEVMode: s.cfg.KEVMode, Ecosystems: s.cfg.Ecosystems, StaleAfter: s.cfg.StaleAfter,
	}
}

// ClaimedMatch is a public advisory that names an internal name (R-VULN-010).
type ClaimedMatch struct {
	Ecosystem, Advisory, Name string
	Kind                      advisory.Kind
	// ClaimRepository is the repository whose claim covers the name, "" when no claim does any longer.
	ClaimRepository string
	FirstSeenAt     time.Time
}

// ClaimedMatches returns at most limit matches after the position, with the repository of the claim that covers each name.
func (s *Service) ClaimedMatches(ctx context.Context, after *store.ClaimedAfter, limit int) ([]ClaimedMatch, error) {
	rows, err := s.st.ClaimedMatches(ctx, after, limit)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	repos, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	formatOf := map[string]string{}
	for _, r := range repos {
		if eco := r.VulnerabilityEcosystem(); eco != "" {
			formatOf[eco] = r.Format
		}
	}
	out := make([]ClaimedMatch, 0, len(rows))
	for _, r := range rows {
		m := ClaimedMatch{Ecosystem: r.Ecosystem, Advisory: r.Advisory, Name: r.Name, Kind: r.Kind, FirstSeenAt: r.FirstSeenAt}
		// The holder is the rule's: the most specific claim of a local repository that does not set always (R-VULN-010).
		if holder, internal, err := s.rule.Claim(ctx, formatOf[r.Ecosystem], r.Name); err == nil && internal {
			m.ClaimRepository = holder
		}
		out = append(out, m)
	}
	return out, nil
}
