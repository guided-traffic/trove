package app

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The wiring of the client audits (work package D, R-VULN-017, R-VULN-018): the rule of the internal names over the claim
// index and the repository snapshot, and the ports the Host hands to the formats.

// claimLookup is what the rule needs of the routing: the claims of the format that cover a name.
type claimLookup interface {
	ClaimsFor(ctx context.Context, format, name string) ([]repository.ClaimMatch, error)
}

// internalNameRule is the rule of R-VULN-010 for client audits: a name is internal when a namespace claim covers it that a
// local repository of the format holds, and that repository does not set matchPublicAdvisories: always (a repository with
// always declares that it holds public packages, so its claims serve routing only). Claims come from the replica's claim
// index and the setting from the repository snapshot: no query.
func internalNameRule(claims claimLookup, repos repository.Reader) store.InternalNameRule {
	return func(ctx context.Context, format, name string) (bool, error) {
		matches, err := claims.ClaimsFor(ctx, format, name)
		if err != nil {
			return false, err
		}
		for _, m := range matches {
			r, err := repos.ByKey(ctx, m.Repository)
			if err != nil {
				if e, ok := errs.As(err); ok && e.Kind == errs.NotFound { // deleted since the index was built
					continue
				}
				return false, err
			}
			if r.Class == repository.Local && r.Format == format && r.VulnerabilitySettings().MatchPublicAdvisories != vuln.MatchAlways {
				return true, nil
			}
		}
		return false, nil
	}
}

// wireAudits builds the advisories behind the formats' audit routes and the cache of the NuGet page. The advisories are wired
// while vulnerabilities.enabled is true; the audit modes then default to local while the OSV source is not off (Q-VULN-44).
func (a *App) wireAudits(in infra, repo repositoryArea, vir virtualArea, out *vulnArea) error {
	cfg := a.cfg.Vulnerabilities
	if !cfg.Enabled {
		return nil
	}
	if repo.reader == nil || vir.routing == nil {
		return errors.New("vuln: the client audits need the repository reader and the claim routing")
	}
	adv, err := store.NewAudits(in.db, internalNameRule(vir.routing, repo.reader))
	if err != nil {
		return err
	}
	pages, err := store.NewPages(in.caches, out.states, in.changes)
	if err != nil {
		return err
	}
	out.advisories, out.pages = adv, pages
	out.localAudits = cfg.Sources.OSV.Mode != "off"
	return nil
}

// hostPorts are the ports the Host hands to the formats' audit routes (R-VULN-017, R-VULN-018).
func (v vulnArea) hostPorts() formathost.VulnPorts {
	p := formathost.VulnPorts{States: v.states, Advisories: v.advisories, Audit: v.countAudit, LocalDefault: v.localAudits}
	if v.pages != nil {
		p.Pages = v.pages
	}
	return p
}
