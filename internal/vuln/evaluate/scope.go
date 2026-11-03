// Package evaluate is the evaluation of stored versions against advisory data and the lifecycle of findings
// (docs/spec/vulnerability-scanning.md R-VULN-009 to R-VULN-012, R-VULN-014; docs/developer/vulnerabilities.md, work
// package B): the commit hook and the remote miss (the [vuln.Evaluator] port), the jobs vuln-evaluate, vuln-rescan and
// vuln-findings-retention, the claims rule of R-VULN-010 and the repository hook that enqueues a rescan.
//
// It is a domain package. The formats' adapters reach it through the composition root as [Ecosystem] values, by structural
// typing ([match.Adapter]); it never imports a format.
package evaluate

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

// Ecosystem is one OSV ecosystem the installation scans: the formats that store its packages and the adapter that
// normalizes names and versions and orders versions (R-VULN-009).
type Ecosystem struct {
	// Name is the OSV ecosystem ("npm", "NuGet").
	Name string
	// Formats are the IDs of the formats whose repositories are matched in the ecosystem.
	Formats []string
	Adapter match.Adapter
}

// Scope is how a stored version relates to public advisories (R-VULN-010).
type Scope int

// The scopes. A version in scope [ScopeMatched] is evaluated; [ScopeInternal] is the evaluation state `internal`, and
// [ScopeNotCovered] the state `not_covered` of an unrecognised upstream.
const (
	ScopeMatched Scope = iota
	ScopeInternal
	ScopeNotCovered
)

// ClaimIndex is the replica's claim index (repository.Routing implements it): the claims that cover a name, from the
// per-replica copy and without a query.
type ClaimIndex interface {
	ClaimsFor(ctx context.Context, format, name string) ([]repository.ClaimMatch, error)
}

// ClaimRule is the internal-names rule of R-VULN-010, in one place for the evaluation and the client audits: a package name
// is internal when a namespace claim of either scope covers it, held by a local repository of the format that does not set
// matchPublicAdvisories always. A repository with always declares that it holds public packages, so its claims serve routing
// only. Claims come from the claim index and the repository setting from the snapshot: the rule costs no query.
type ClaimRule struct {
	Claims ClaimIndex
	Repos  repository.Reader
}

// Claim reports whether the name is internal for the format and, when it is, the key of the repository whose claim makes it
// so (the most specific claim's first holder that counts). A claim whose repository the snapshot does not hold counts: a rule
// that cannot read the setting does not withhold less.
func (r ClaimRule) Claim(ctx context.Context, format, name string) (claimRepo string, internal bool, err error) {
	if r.Claims == nil {
		return "", false, nil
	}
	matches, err := r.Claims.ClaimsFor(ctx, format, name)
	if err != nil {
		return "", false, err
	}
	for _, m := range matches {
		repo, err := r.Repos.ByKey(ctx, m.Repository)
		if err != nil {
			return m.Repository, true, nil //nolint:nilerr // see above: an unreadable holder still withholds
		}
		if repo.Class == repository.Local && repo.Format == format && repo.VulnerabilitySettings().MatchPublicAdvisories != vuln.MatchAlways {
			return repo.Key, true, nil
		}
	}
	return "", false, nil
}

// InternalNames reports which of the names are internal names of the format: the client audits withhold them (R-VULN-017,
// R-VULN-018). It is the implementation of vuln.Advisories.InternalNames; the map holds the internal names only.
func (r ClaimRule) InternalNames(ctx context.Context, format string, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, n := range names {
		_, internal, err := r.Claim(ctx, format, n)
		if err != nil {
			return nil, err
		}
		if internal {
			out[n] = true
		}
	}
	return out, nil
}

// repoView is what the evaluation needs of a repository: its facts and the scanning inputs. The hot path builds it from the
// replica's snapshot, a rescan from the repository row, so that it sees a setting committed just before it ran.
type repoView struct {
	ID        int64
	Key       string
	Format    string
	Class     repository.Class
	Ecosystem string
	Settings  vuln.Settings
	// UpstreamHost is the lowercased host of a remote repository's upstream URL.
	UpstreamHost string
}

// viewOf builds the view of a repository of the snapshot; false for a repository the scanning does not apply to (virtual, or
// a format without the adapter).
func viewOf(repo repository.Repository) (repoView, bool) {
	eco := repo.VulnerabilityEcosystem()
	if eco == "" || repo.Class == repository.Virtual {
		return repoView{}, false
	}
	id, err := strconv.ParseInt(repo.ID, 10, 64)
	if err != nil {
		return repoView{}, false
	}
	v := repoView{ID: id, Key: repo.Key, Format: repo.Format, Class: repo.Class, Ecosystem: eco, Settings: repo.VulnerabilitySettings()}
	if rs, ok := repo.Remote(); ok {
		v.UpstreamHost = hostOf(rs.URL)
	}
	return v, true
}

// hostOf is the lowercased host name of a URL, "" for one that does not parse.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// scopeOf applies the table of R-VULN-010. For a local repository with matchPublicAdvisories auto the claims decide, and
// claimRepo names the repository whose claim made the name internal. inTx, when not nil, adds the claims of the repository
// itself read in the writing transaction: a first publish creates its auto claim in the same transaction, which the
// per-replica index does not hold yet.
func (s *Service) scopeOf(ctx context.Context, rv repoView, name string, inTx claimReader) (sc Scope, claimRepo string, err error) {
	switch rv.Class {
	case repository.Remote:
		switch rv.Settings.MatchPublicAdvisories {
		case vuln.MatchAlways:
			return ScopeMatched, "", nil
		case vuln.MatchNever:
			return ScopeNotCovered, "", nil
		}
		if s.publicUpstream(rv.Format, rv.UpstreamHost) {
			return ScopeMatched, "", nil
		}
		return ScopeNotCovered, "", nil
	case repository.Local:
		switch rv.Settings.MatchPublicAdvisories {
		case vuln.MatchAlways:
			return ScopeMatched, "", nil
		case vuln.MatchNever:
			return ScopeInternal, "", nil
		}
		repo, internal, err := s.rule.Claim(ctx, rv.Format, name)
		if err != nil {
			return ScopeMatched, "", err
		}
		if internal {
			return ScopeInternal, repo, nil
		}
		if inTx != nil {
			own, err := inTx.RepoClaims(ctx, rv.ID)
			if err != nil {
				return ScopeMatched, "", err
			}
			for _, p := range own {
				if cp, perr := repository.ParseClaimPattern(p); perr == nil && cp.Match(name) {
					return ScopeInternal, rv.Key, nil
				}
			}
		}
		return ScopeMatched, "", nil
	}
	return ScopeNotCovered, "", nil
}

// claimReader is the read of a repository's own claims in the writing transaction.
type claimReader interface {
	RepoClaims(ctx context.Context, repoID int64) ([]string, error)
}

// publicUpstream reports whether the host is listed in vulnerabilities.matching.public_upstreams for the format.
func (s *Service) publicUpstream(format, host string) bool {
	if host == "" {
		return false
	}
	for _, h := range s.o.PublicUpstreams[format] {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}
