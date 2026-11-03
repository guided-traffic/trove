package report

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// Source names of the source state.
const (
	SourceOSV = "osv"
	SourceKEV = "kev"
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
}

// Sources returns the state of every configured source and ecosystem, in the order of source and ecosystem. nextRun answers the
// next scheduled run of a task by its name (vuln.TaskSyncOSV, vuln.TaskSyncKEV), nil for none.
func (s *Service) Sources(ctx context.Context, nextRun func(task string) *time.Time) ([]SourceView, error) {
	_, rows, err := s.dataStates(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]store.SourceRow{}
	for _, r := range rows {
		have[r.Source+"\x00"+r.Ecosystem] = r
	}
	keys := map[string]bool{SourceKEV + "\x00": true}
	for _, e := range s.cfg.Ecosystems {
		keys[SourceOSV+"\x00"+e] = true
	}
	for k := range have {
		keys[k] = true
	}
	order := make([]string, 0, len(keys))
	for k := range keys {
		order = append(order, k)
	}
	sort.Strings(order)
	now := s.now()
	out := make([]SourceView, 0, len(order))
	for _, k := range order {
		r, ok := have[k]
		if !ok {
			src, eco, _ := strings.Cut(k, "\x00")
			r = store.SourceRow{Source: src, Ecosystem: eco, State: "empty"}
		}
		v := SourceView{
			Source: r.Source, Ecosystem: r.Ecosystem, Mode: s.mode(r.Source), State: r.State, SyncedAt: r.SyncedAt,
			FullSyncedAt: r.FullSyncedAt, AttemptedAt: r.AttemptedAt, Failures: r.Failures, LastError: r.LastError,
			Records: r.Records, Rejected: r.Rejected, Unevaluable: r.Unevaluable, Stale: s.stale(r.SyncedAt, now),
		}
		if v.Mode == "repository" {
			v.Repository = s.cfg.OSVRepository
			if r.Source == SourceKEV {
				v.Repository = s.cfg.KEVRepository
			}
		}
		if nextRun != nil && v.Mode != "off" {
			task := vuln.TaskSyncOSV
			if r.Source == SourceKEV {
				task = vuln.TaskSyncKEV
			}
			v.NextRunAt = nextRun(task)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) mode(source string) string {
	if !s.cfg.Enabled {
		return "off"
	}
	if source == SourceKEV {
		return s.cfg.KEVMode
	}
	return s.cfg.OSVMode
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
	claims, err := s.st.Claims(ctx)
	if err != nil {
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
	type compiled struct {
		format, repo string
		pat          repository.ClaimPattern
	}
	var pats []compiled
	for _, c := range claims {
		if p, err := repository.ParseClaimPattern(c.Pattern); err == nil {
			pats = append(pats, compiled{format: c.Format, repo: c.Repository, pat: p})
		}
	}
	out := make([]ClaimedMatch, 0, len(rows))
	for _, r := range rows {
		m := ClaimedMatch{Ecosystem: r.Ecosystem, Advisory: r.Advisory, Name: r.Name, Kind: r.Kind, FirstSeenAt: r.FirstSeenAt}
		for _, c := range pats {
			if c.format == formatOf[r.Ecosystem] && c.pat.Match(r.Name) {
				m.ClaimRepository = c.repo
				break
			}
		}
		out = append(out, m)
	}
	return out, nil
}
