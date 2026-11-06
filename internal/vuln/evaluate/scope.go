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

// ClaimHolder is what the claims rule needs of the repository that holds a claim.
type ClaimHolder struct {
	Key      string
	Format   string
	Class    repository.Class
	Settings vuln.Settings
}

// counts reports whether a claim of the holder makes a name internal for the format: a local repository of the format that does
// not declare, with matchPublicAdvisories always, that it holds public packages.
func (h ClaimHolder) counts(format string) bool {
	return h.Class == repository.Local && h.Format == format && h.Settings.MatchPublicAdvisories != vuln.MatchAlways
}

// HolderIndex is a [ClaimIndex] that brings the holders of its claims with it, so that the rule does not read the repository
// snapshot: the index of a writing transaction (see txClaims), whose reads must stay on the transaction's connection.
type HolderIndex interface {
	ClaimIndex
	// Holder returns the holder of a claim the index returned; false when it knows none for the key.
	Holder(key string) (ClaimHolder, bool)
}

// ClaimRule is the internal-names rule of R-VULN-010, in one place for the evaluation and the client audits: a package name
// is internal when a namespace claim of either scope covers it, held by a local repository of the format that does not set
// matchPublicAdvisories always. A repository with always declares that it holds public packages, so its claims serve routing
// only. Claims come from the claim index and the repository setting from the snapshot: the rule costs no query. A [HolderIndex]
// brings the holders itself; the commit's evaluation uses one read in its transaction.
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
	holders, _ := r.Claims.(HolderIndex)
	for _, m := range matches {
		var h ClaimHolder
		if holders != nil {
			var ok bool
			if h, ok = holders.Holder(m.Repository); !ok {
				return m.Repository, true, nil
			}
		} else {
			repo, err := r.Repos.ByKey(ctx, m.Repository)
			if err != nil {
				return m.Repository, true, nil //nolint:nilerr // see above: an unreadable holder still withholds
			}
			h = ClaimHolder{Key: repo.Name, Format: repo.Format, Class: repo.Class, Settings: repo.VulnerabilitySettings()}
		}
		if h.counts(format) {
			return h.Key, true, nil
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
	v := repoView{ID: id, Key: repo.Name, Format: repo.Format, Class: repo.Class, Ecosystem: eco, Settings: repo.VulnerabilitySettings()}
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

// scopeOf applies the table of R-VULN-010 with the rule given. For a local repository with matchPublicAdvisories auto the claims
// decide, and claimRepo names the repository whose claim made the name internal.
func (s *Service) scopeOf(ctx context.Context, rv repoView, name string, rule ClaimRule) (sc Scope, claimRepo string, err error) {
	if sc, decided := repositoryScope(s.o.PublicUpstreams, rv); decided {
		return sc, "", nil
	}
	repo, internal, err := rule.Claim(ctx, rv.Format, name)
	if err != nil {
		return ScopeMatched, "", err
	}
	if internal {
		return ScopeInternal, repo, nil
	}
	return ScopeMatched, "", nil
}

// ScopeOf is the table of R-VULN-010 for a reader that is not the evaluation, the views of the report: the one scope function
// of the installation, the claim rule included (A5, Q-VULN-70). It reads the repository's own facts and the replica's claim
// index only; claimRepo names the repository whose claim made the name internal. A repository the scanning does not apply to
// (virtual, or a format without the adapter) is [ScopeNotCovered].
func ScopeOf(ctx context.Context, repo repository.Repository, name string, rule ClaimRule, publicUpstreams map[string][]string) (sc Scope, claimRepo string, err error) {
	rv, ok := viewOf(repo)
	if !ok {
		return ScopeNotCovered, "", nil
	}
	s := &Service{rule: rule, o: Options{PublicUpstreams: publicUpstreams}}
	return s.scopeOf(ctx, rv, name, rule)
}

// publicUpstream reports whether the host is listed in vulnerabilities.matching.public_upstreams for the format.
func publicUpstream(public map[string][]string, format, host string) bool {
	if host == "" {
		return false
	}
	for _, h := range public[format] {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// repositoryScope is the part of the table of R-VULN-010 that the repository alone decides: a remote repository by its
// matchPublicAdvisories and upstream host, a local one by always (matched) and never (internal). decided is false for a local
// repository with auto, whose scope depends on the name.
func repositoryScope(public map[string][]string, rv repoView) (sc Scope, decided bool) {
	switch rv.Class {
	case repository.Remote:
		switch rv.Settings.MatchPublicAdvisories {
		case vuln.MatchAlways:
			return ScopeMatched, true
		case vuln.MatchNever:
			return ScopeNotCovered, true
		}
		if publicUpstream(public, rv.Format, rv.UpstreamHost) {
			return ScopeMatched, true
		}
		return ScopeNotCovered, true
	case repository.Local:
		switch rv.Settings.MatchPublicAdvisories {
		case vuln.MatchAlways:
			return ScopeMatched, true
		case vuln.MatchNever:
			return ScopeInternal, true
		}
		return ScopeMatched, false
	}
	return ScopeNotCovered, true
}

// RepositoryCovered reports whether versions of the repository can be in scope, as the snapshot and the configured public
// upstreams (vulnerabilities.matching.public_upstreams by format) alone say (no query): false for a remote repository whose
// upstream is not public and for a local one with matchPublicAdvisories never, whose versions are never evaluated, so never
// pending (R-VULN-015, R-VULN-001). A local repository with auto is covered here, because a claimed name is internal and the
// name is not known to the caller (Q-VULN-75); a repository the evaluation does not apply to is covered as well, so that the
// answer never withholds less.
func RepositoryCovered(public map[string][]string, repo repository.Repository) bool {
	rv, ok := viewOf(repo)
	if !ok {
		return true
	}
	sc, decided := repositoryScope(public, rv)
	return !decided || sc == ScopeMatched
}
