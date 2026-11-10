package report

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// AllStatuses is the status filter that includes the resolved findings, which a request without a status filter leaves out.
func AllStatuses() []string {
	var out []string
	for _, st := range vuln.Statuses() {
		out = append(out, string(st))
	}
	return out
}

// List returns at most limit findings after the position, in the order, so that the caller learns whether a next page
// exists. The scope is the caller's (Scope); the filter's block members are set from the repositories' policy here.
func (s *Service) List(ctx context.Context, sc Scope, f store.Filter, o store.Order, after *store.After, limit int) ([]Finding, error) {
	rows, err := s.st.List(ctx, sc.Scope, s.withPolicy(sc.snap, f), o, after, limit)
	if err != nil {
		return nil, err
	}
	return s.findings(sc.snap, rows), nil
}

// Advisory is an advisory with its attribution.
type Advisory struct {
	store.Advisory
	Source advisory.Source
	// FixedVersions are the `fixed` events of the affected entries, deduplicated.
	FixedVersions []string
}

func (s *Service) advisoryView(a store.Advisory) Advisory {
	out := Advisory{Advisory: a, Source: advisory.SourceOf(a.ID)}
	if a.Ecosystem == advisory.EcosystemOCI {
		out.Source = advisory.ImageSource(a.ID)
	}
	for _, af := range a.Affected {
		for _, v := range af.Fixed {
			if !slices.Contains(out.FixedVersions, v) {
				out.FixedVersions = append(out.FixedVersions, v)
			}
		}
	}
	return out
}

// fallbackAdvisory is the advisory as far as a finding carries it, for a finding whose advisory record is gone (the advisory
// tables are transient: a restored installation has findings before its first import).
func fallbackAdvisory(r store.Row) store.Advisory {
	a := advisory.Advisory{
		Ecosystem: r.Ecosystem, ID: r.Advisory, CanonicalID: r.Advisory, Kind: r.Kind, Aliases: r.Aliases, Summary: r.Summary,
		Severity: r.Severity, KnownExploited: r.KnownExploited, Modified: r.DetectedAt,
		CVSS: advisory.CVSS{Version: r.CVSSVersion, Vector: r.CVSSVector, Score: max(r.CVSSScore, 0)},
	}
	return store.Advisory{Advisory: a, CVSSScore: r.CVSSScore, KnownExploitedAddedAt: r.KnownExploitedAddedAt}
}

// Detail is one finding with its advisory, the readable virtual repositories that show it and its history.
type Detail struct {
	Finding             Finding
	Advisory            Advisory
	VirtualRepositories []qname.Repo
	History             []store.Event
}

// Detail returns one finding the caller can read; found is false for an id the caller cannot read, whatever its shape. The
// scope has to be the one built for this id ([Service.Scope] with the filter [IDFilter]).
func (s *Service) Detail(ctx context.Context, sc Scope, id string) (Detail, bool, error) {
	rows, err := s.st.List(ctx, sc.Scope, s.withPolicy(sc.snap, IDFilter(id)), store.Order{Sort: "severity", Desc: true}, nil, 1)
	if err != nil || len(rows) == 0 {
		return Detail{}, false, err
	}
	row := rows[0]
	adv, found, err := s.st.Advisory(ctx, row.Ecosystem, row.Advisory)
	if err != nil {
		return Detail{}, false, err
	}
	if !found {
		adv = fallbackAdvisory(row)
	}
	hist, err := s.st.Events(ctx, row.ID)
	if err != nil {
		return Detail{}, false, err
	}
	return Detail{
		Finding: s.findings(sc.snap, rows)[0], Advisory: s.advisoryView(adv), VirtualRepositories: sc.VirtualContaining(row.RepoID), History: hist,
	}, true, nil
}

// IDFilter selects the findings of the given ids, of any status.
func IDFilter(ids ...string) store.Filter {
	return store.Filter{IDs: ids, Statuses: AllStatuses()}
}

// AdvisoryDetail is an advisory with the findings of it the caller can read.
type AdvisoryDetail struct {
	Advisory  Advisory
	Findings  []Finding
	Truncated bool
}

// maxAdvisoryFindings is the number of findings the advisory view lists.
const maxAdvisoryFindings = 1000

// AdvisoryFilter selects the findings of one advisory group.
func AdvisoryFilter(ecosystem, canonical string) store.Filter {
	return store.Filter{Ecosystem: ecosystem, Advisory: canonical}
}

// ResolveAdvisory finds an advisory by identifier or alias; found is false for an unknown one.
func (s *Service) ResolveAdvisory(ctx context.Context, ecosystem, id string) (Advisory, bool, error) {
	a, found, err := s.st.Advisory(ctx, ecosystem, id)
	if err != nil || !found {
		return Advisory{}, false, err
	}
	return s.advisoryView(a), true, nil
}

// Findings returns the findings of an advisory group the caller can read (at most 1,000).
func (s *Service) AdvisoryFindings(ctx context.Context, sc Scope, a Advisory) (AdvisoryDetail, error) {
	f := AdvisoryFilter(a.Ecosystem, a.ID)
	rows, err := s.st.List(ctx, sc.Scope, s.withPolicy(sc.snap, f), store.Order{Sort: "severity", Desc: true}, nil, maxAdvisoryFindings+1)
	if err != nil {
		return AdvisoryDetail{}, err
	}
	out := AdvisoryDetail{Advisory: a}
	if len(rows) > maxAdvisoryFindings {
		rows, out.Truncated = rows[:maxAdvisoryFindings], true
	}
	out.Findings = s.findings(sc.snap, rows)
	return out, nil
}

// exportPage is the number of findings one query of the export fetches.
const exportPage = 500

// Export streams the findings the filter selects in the order, after it has checked that they do not exceed
// vulnerabilities.export.max_rows (422 export_too_large, before begin is called, so before anything is sent). begin runs once
// the check passed and before the first row.
func (s *Service) Export(ctx context.Context, sc Scope, f store.Filter, o store.Order, begin func() error, emit func(Finding) error) error {
	f = s.withPolicy(sc.snap, f)
	n, err := s.st.Count(ctx, sc.Scope, f, s.cfg.ExportMaxRows+1)
	if err != nil {
		return err
	}
	if n > int64(s.cfg.ExportMaxRows) {
		return errs.E(errs.Unprocessable, errs.CodeExportTooLarge,
			fmt.Sprintf("The export would exceed vulnerabilities.export.max_rows (%d); narrow the filter.", s.cfg.ExportMaxRows))
	}
	if err := begin(); err != nil {
		return err
	}
	var after *store.After
	for {
		rows, err := s.st.List(ctx, sc.Scope, f, o, after, exportPage)
		if err != nil {
			return err
		}
		for _, fd := range s.findings(sc.snap, rows) {
			if err := emit(fd); err != nil {
				return err
			}
		}
		if len(rows) < exportPage {
			return nil
		}
		last := rows[len(rows)-1]
		after = &store.After{Num: last.KeyNum, Txt: last.KeyTxt, ID: last.ID}
	}
}

// CursorKey returns the sort key of a finding in the order the statement compares it, for the cursor.
func CursorKey(f Finding) []string {
	return []string{strconv.FormatInt(f.KeyNum, 10), f.KeyTxt, f.ID}
}

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseAfter reads the cursor values of [CursorKey].
func ParseAfter(key []string) (*store.After, bool) {
	if len(key) != 3 {
		return nil, false
	}
	n, err := strconv.ParseInt(key[0], 10, 64)
	if err != nil || !uuidShape.MatchString(key[2]) {
		return nil, false
	}
	return &store.After{Num: n, Txt: key[1], ID: key[2]}, true
}
