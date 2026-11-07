package report

import (
	"context"
	"slices"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// SummaryFilter is the filter of the summary: repositories and formats, narrowed to the open findings.
func SummaryFilter(repos []int64, hasRepos bool, formats []string) store.Filter {
	return store.Filter{Repos: repos, HasRepos: hasRepos, Formats: formats, Statuses: vuln.OpenStatuses()}
}

// Coverage counts the readable repositories by the evaluation state of their scanning.
type Coverage struct{ Evaluated, PendingData, Internal, NotCovered int64 }

// DataAge is the state of one ecosystem's advisory data.
type DataAge struct {
	Ecosystem string
	Complete  bool
	Stale     bool
	SyncedAt  *time.Time
}

// Summary is the open findings the caller can read (R-VULN-021).
type Summary struct {
	BySeverity      Counts
	Vulnerability   int64
	Malicious       int64
	NeedsTriage     int64
	BlockedVersions int64
	FixAvailable    int64
	Coverage        Coverage
	Data            []DataAge
}

// Summary counts the open findings in scope, the versions they block, the coverage of the readable repositories and the age
// of the advisory data. It is computed per request over what the caller can read and cached nowhere (R-VULN-019).
func (s *Service) Summary(ctx context.Context, sc Scope, f store.Filter) (Summary, error) {
	f = s.withPolicy(sc.snap, f)
	rows, err := s.st.SummaryCounts(ctx, sc.Scope, f)
	if err != nil {
		return Summary{}, err
	}
	var out Summary
	for _, r := range rows {
		out.BySeverity.Add(r.Severity, r.Total)
		if r.Kind == advisory.KindMalicious {
			out.Malicious += r.Total
		} else {
			out.Vulnerability += r.Total
		}
		out.NeedsTriage += r.NeedsTriage
		out.FixAvailable += r.FixAvailab
	}
	if out.BlockedVersions, err = s.st.BlockedVersions(ctx, sc.Scope, f); err != nil {
		return Summary{}, err
	}
	data, _, err := s.dataStates(ctx)
	if err != nil {
		return Summary{}, err
	}
	for _, id := range sc.Readable {
		r, _ := sc.snap.Repository(id)
		if f.HasRepos && !slices.Contains(f.Repos, id) {
			continue
		}
		switch s.evaluation(ctx, r, "", data) {
		case vuln.EvaluationEvaluated:
			out.Coverage.Evaluated++
		case vuln.EvaluationPendingData:
			out.Coverage.PendingData++
		case vuln.EvaluationInternal:
			out.Coverage.Internal++
		default:
			out.Coverage.NotCovered++
		}
	}
	for _, eco := range s.ecosystems(data) {
		d := data[eco]
		out.Data = append(out.Data, DataAge{Ecosystem: eco, Complete: d.Complete, Stale: d.Stale, SyncedAt: d.SyncedAt})
	}
	return out, nil
}

// ecosystems lists the ecosystems the installation imports and those that have a source row, sorted.
func (s *Service) ecosystems(data map[string]DataInfo) []string {
	set := map[string]bool{}
	for _, e := range s.cfg.Ecosystems {
		set[e] = true
	}
	for e := range data {
		set[e] = true
	}
	out := make([]string, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

// TrendBasis says how the counts of the trend are made (R-VULN-021).
const TrendBasis = "A finding counts under its current severity, from the day it was detected until the day it resolved; dismissed findings count until they resolve."

// MaxTrendDays is the longest range of the trend.
const MaxTrendDays = 400

// TrendPoint is the open findings at the end of one day.
type TrendPoint struct {
	Date   string
	Counts Counts
}

// Trend is the findings detected and not resolved at the end of each day from..to, both included (R-VULN-021, Q-VULN-16):
// the detections and the resolutions per day and severity are two aggregate queries over the findings the caller can read,
// and the day's counts are their running difference. It needs no snapshot table.
func (s *Service) Trend(ctx context.Context, sc Scope, f store.Filter, from, to time.Time) ([]TrendPoint, error) {
	from, to = from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour)
	if to.Before(from) {
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The query parameter \"to\" must not be before \"from\".")
	}
	days := int(to.Sub(from).Hours()/24) + 1
	if days > MaxTrendDays {
		return nil, errs.E(errs.Invalid, errs.CodeInvalid, "The range of the trend is at most 400 days.")
	}
	detected, resolved, err := s.st.Trend(ctx, sc.Scope, f, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	delta := map[string]*Counts{}
	at := func(day string) *Counts {
		c, ok := delta[day]
		if !ok {
			c = &Counts{}
			delta[day] = c
		}
		return c
	}
	for _, d := range detected {
		at(d.Day).Add(d.Severity, d.N)
	}
	for _, d := range resolved {
		at(d.Day).Add(d.Severity, -d.N)
	}
	run := Counts{}
	if c, ok := delta[from.AddDate(0, 0, -1).Format(time.DateOnly)]; ok {
		run = *c
	}
	out := make([]TrendPoint, 0, days)
	for i := range days {
		day := from.AddDate(0, 0, i).Format(time.DateOnly)
		if c, ok := delta[day]; ok {
			run.Critical += c.Critical
			run.High += c.High
			run.Medium += c.Medium
			run.Low += c.Low
			run.None += c.None
			run.Unknown += c.Unknown
		}
		out = append(out, TrendPoint{Date: day, Counts: run.floored()})
	}
	return out, nil
}

// floored keeps a count that rounding of the two sums could push below zero at zero.
func (c Counts) floored() Counts {
	return Counts{max(c.Critical, 0), max(c.High, 0), max(c.Medium, 0), max(c.Low, 0), max(c.None, 0), max(c.Unknown, 0)}
}
