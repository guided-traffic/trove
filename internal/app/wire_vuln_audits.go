package app

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/vuln/evaluate"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// The wiring of the client audits (work package D, R-VULN-017, R-VULN-018): the rule of the internal names over the claim
// index and the repository snapshot, and the ports the Host hands to the formats.

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
	// The internal-names rule is the evaluation's (R-VULN-010), so the audits withhold exactly the names the evaluation does
	// not match against public advisories.
	rule := evaluate.ClaimRule{Claims: vir.routing, Repos: repo.reader}
	adv, err := store.NewAudits(in.db, func(ctx context.Context, format, name string) (bool, error) {
		_, internal, err := rule.Claim(ctx, format, name)
		return internal, err
	})
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
