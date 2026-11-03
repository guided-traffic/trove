package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// Audits reads the advisory data of the client audits (R-VULN-017, R-VULN-018) and implements [vuln.Advisories]. Its
// queries are queries/vuln_audits.sql; the rule of the internal names is an [InternalNameRule] over the claim index.
type Audits struct {
	run   meta.Runner
	names InternalNameRule
}

var _ vuln.Advisories = (*Audits)(nil)

// NewAudits builds the advisory reads over a runner and the rule of the internal names.
func NewAudits(run meta.Runner, names InternalNameRule) (*Audits, error) {
	if run == nil {
		return nil, errors.New("vuln store: no runner")
	}
	if names == nil {
		return nil, errors.New("vuln store: no internal-name rule")
	}
	return &Audits{run: run, names: names}, nil
}

// InternalNameRule reports whether a normalized name of a format is an internal name (R-VULN-010): covered by a namespace
// claim of a local repository of the format that does not set matchPublicAdvisories always. It reads the replica's claim
// index and the repository snapshot, so a call costs no query.
type InternalNameRule func(ctx context.Context, format, name string) (bool, error)

func queriesOf(tx meta.Tx) (*q.Queries, error) {
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		return nil, errors.New("vuln store: the transaction does not come from the PostgreSQL store")
	}
	return q.New(ptx), nil
}

// auditRow is one row of either audit query: an advisory with one of its affected entries.
type auditRow = q.AuditAllAdvisoriesRow

// ForNames implements [vuln.Advisories]: the advisories that name any of the names, keyed by name. An advisory that names
// two requested names is listed under both, and under each it carries the affected entries of that name only. The
// advisories of one name are in the order of their canonical identifier. A name no advisory names has no key.
func (a *Audits) ForNames(ctx context.Context, ecosystem string, names []string) (map[string][]advisory.Advisory, error) {
	out := map[string][]advisory.Advisory{}
	if len(names) == 0 {
		return out, nil
	}
	var rows []q.AuditAdvisoriesForNamesRow
	err := a.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err = qs.AuditAdvisoriesForNames(ctx, q.AuditAdvisoriesForNamesParams{Ecosystem: ecosystem, Names: names})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("vuln store: advisories of %d names: %w", len(names), err)
	}
	for _, r := range rows {
		ad, err := groupRow(ecosystem, auditRow(r), out[r.Name])
		if err != nil {
			return nil, err
		}
		out[r.Name] = ad
	}
	return out, nil
}

// All implements [vuln.Advisories]: every non-withdrawn advisory with all of its affected entries, ordered by canonical
// identifier then identifier.
func (a *Audits) All(ctx context.Context, ecosystem string) ([]advisory.Advisory, error) {
	var rows []q.AuditAllAdvisoriesRow
	err := a.run.InTx(ctx, func(tx meta.Tx) error {
		qs, err := queriesOf(tx)
		if err != nil {
			return err
		}
		rows, err = qs.AuditAllAdvisories(ctx, ecosystem)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("vuln store: all advisories of %s: %w", ecosystem, err)
	}
	byID := map[string]int{}
	var out []advisory.Advisory
	for _, r := range rows {
		i, seen := byID[r.ID]
		if !seen {
			ad, err := advisoryOf(ecosystem, r)
			if err != nil {
				return nil, err
			}
			i = len(out)
			byID[r.ID] = i
			out = append(out, ad)
		}
		af, err := affectedOf(r)
		if err != nil {
			return nil, err
		}
		out[i].Affected = append(out[i].Affected, af)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CanonicalID != out[j].CanonicalID {
			return out[i].CanonicalID < out[j].CanonicalID
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// InternalNames implements [vuln.Advisories]: the rule of R-VULN-010 per name; only the internal ones are keys.
func (a *Audits) InternalNames(ctx context.Context, format string, names []string) (map[string]bool, error) {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		internal, err := a.names(ctx, format, n)
		if err != nil {
			return nil, fmt.Errorf("vuln store: internal name of %q: %w", n, err)
		}
		if internal {
			out[n] = true
		}
	}
	return out, nil
}

// groupRow adds the row's affected entry to the advisory of its identifier; the rows of one name list an advisory in a run.
func groupRow(ecosystem string, r auditRow, so []advisory.Advisory) ([]advisory.Advisory, error) {
	af, err := affectedOf(r)
	if err != nil {
		return nil, err
	}
	if n := len(so); n > 0 && so[n-1].ID == r.ID {
		so[n-1].Affected = append(so[n-1].Affected, af)
		return so, nil
	}
	ad, err := advisoryOf(ecosystem, r)
	if err != nil {
		return nil, err
	}
	ad.Affected = []advisory.Affected{af}
	return append(so, ad), nil
}

func advisoryOf(ecosystem string, r auditRow) (advisory.Advisory, error) {
	kind := advisory.Kind(r.Kind)
	if !kind.Valid() {
		return advisory.Advisory{}, fmt.Errorf("vuln store: advisory %s has kind %q", r.ID, r.Kind)
	}
	return advisory.Advisory{
		Ecosystem: ecosystem, ID: r.ID, CanonicalID: r.CanonicalID,
		Modified: time.UnixMicro(r.ModifiedUs).UTC(), Published: micro(r.PublishedUs),
		Kind: kind, Aliases: r.Aliases, Summary: r.Summary,
		Severity: advisory.Severity(r.Severity), SeveritySource: advisory.SeveritySource(r.SeveritySource),
		CVSS:           advisory.CVSS{Version: r.CvssVersion, Vector: r.CvssVector, Score: r.CvssScore},
		KnownExploited: r.KnownExploited, CWEIDs: r.CweIds,
	}, nil
}

func affectedOf(r auditRow) (advisory.Affected, error) {
	var ranges []advisory.Range
	if len(r.Ranges) > 0 {
		if err := json.Unmarshal(r.Ranges, &ranges); err != nil {
			return advisory.Affected{}, fmt.Errorf("vuln store: advisory %s affected entry %d: ranges: %w", r.ID, r.Ord, err)
		}
	}
	return advisory.Affected{Name: r.Name, Ranges: ranges, Versions: r.Versions, EveryVersion: r.EveryVersion, Fixed: r.Fixed}, nil
}

func micro(us int64) time.Time {
	if us == 0 {
		return time.Time{}
	}
	return time.UnixMicro(us).UTC()
}
