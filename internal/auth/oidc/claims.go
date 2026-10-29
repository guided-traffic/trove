package oidc

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/config"
)

// maxGroupValues is how many values of the group claim are read (R-AUTH-008).
const maxGroupValues = 1000

// maxDisplayName bounds the display name (R-AUTH-007).
const maxDisplayName = 256

// Reasons a group claim is unusable (R-AUTH-008).
const (
	missingAbsent    = "absent"
	missingWrongType = "wrong_type"
	missingOverage   = "overage"
)

// lookupClaim reads a claim by a dotted path into nested objects
// (realm_access.roles).
func lookupClaim(c claims, path string) (any, bool) {
	var cur any = map[string]any(c)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// merged reads a claim from the ID token first and from userinfo where the ID
// token lacks it (R-AUTH-006 step 8).
func merged(id, userinfo claims, path string) (any, bool) {
	if v, ok := lookupClaim(id, path); ok {
		return v, true
	}
	if userinfo != nil {
		return lookupClaim(userinfo, path)
	}
	return nil, false
}

// mapping is the result of the claim mapping of one login: the claims for the
// provisioner and the facts the gates and the events need.
type mapping struct {
	claims principal.LoginClaims
	// rawGroups are the claim values before the rules, for login_requires_groups.
	rawGroups []string
	// skipped are rule results that are no valid group name.
	skipped []string
}

// mapClaims applies the claim mapping, the gates and the group rules of a
// provider (R-AUTH-007, R-AUTH-008). breakGlass is the reserved break-glass
// username.
func (p *Provider) mapClaims(v validated, userinfo claims, breakGlass string, log *slog.Logger) (mapping, *loginError) {
	cfg := p.cfg
	m := mapping{claims: principal.LoginClaims{
		Provider: p.ID, Issuer: v.issuer, Subject: v.subject,
		JIT: *cfg.JIT, SyncUsername: *cfg.SyncUsername,
	}}
	raw, _ := merged(v.claims, userinfo, cfg.Claims.Username)
	name, ok := normalizeUsername(raw, cfg.UsernamePrefix, breakGlass)
	if !ok {
		return mapping{}, fail(ReasonUsernameInvalid, fmt.Errorf("claim %s does not map to a valid username", cfg.Claims.Username))
	}
	m.claims.Username = name
	if e, ok := merged(v.claims, userinfo, cfg.Claims.Email); ok {
		m.claims.Email, _ = e.(string)
	}
	m.claims.EmailVerified = emailVerified(v.claims, userinfo)
	if d, ok := merged(v.claims, userinfo, cfg.Claims.DisplayName); ok {
		s, _ := d.(string)
		m.claims.DisplayName = truncateRunes(s, maxDisplayName)
	}

	groups, missing := readGroups(v.claims, userinfo, cfg.Claims.Groups)
	m.rawGroups = groups
	if cfg.RequireVerifiedEmail && !m.claims.EmailVerified {
		return mapping{}, fail(ReasonEmailUnverified, fmt.Errorf("email_verified is not true"))
	}
	if len(cfg.LoginRequiresGroups) > 0 && !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(cfg.LoginRequiresGroups, g) }) {
		return mapping{}, fail(ReasonNotInAllowedGroup, fmt.Errorf("none of login_requires_groups is in the group claim"))
	}

	m.claims.GroupSync = *cfg.GroupSync.Enabled
	m.claims.CreateGroups = cfg.GroupSync.CreateGroups
	m.claims.ClaimState = principal.GroupClaimState(missing)
	if m.claims.GroupSync && missing != "" {
		switch cfg.GroupSync.OnMissingClaim {
		case config.OnMissingDeny:
			return mapping{}, fail(ReasonGroupsClaimMissing, fmt.Errorf("the group claim is %s", missing))
		case config.OnMissingKeep:
			m.claims.KeepOnMissing = true
		}
	}
	if m.claims.GroupSync && missing == "" {
		m.claims.Groups, m.skipped = applyRules(cfg.GroupSync.Rules, groups)
		for _, s := range m.skipped {
			log.Warn("a group rule produced a name that is not a valid group name; skipped", "provider", p.ID, "group", s)
		}
	}
	return m, nil
}

// normalizeUsername is the username rule of R-AUTH-007: Unicode NFC, lower
// case, the prefix, then the pattern and the reserved names, the break-glass
// username among them.
func normalizeUsername(raw any, prefix, breakGlass string) (string, bool) {
	s, ok := raw.(string)
	if !ok || s == "" {
		return "", false
	}
	name := prefix + strings.ToLower(norm.NFC.String(s))
	if !config.ValidUsername(name) || name == breakGlass {
		return "", false
	}
	return name, true
}

// emailVerified reads email_verified as a boolean; some providers send the
// string "true".
func emailVerified(id, userinfo claims) bool {
	v, ok := merged(id, userinfo, "email_verified")
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true"
	}
	return false
}

// readGroups reads the group claim (R-AUTH-008): a string or an array of
// strings, the first 1000 values. It reports why the claim is unusable: absent
// from both the ID token and userinfo, of another type, or replaced by the
// distributed-claims indicator _claim_names.
func readGroups(id, userinfo claims, path string) ([]string, string) {
	v, ok := merged(id, userinfo, path)
	if !ok {
		top, _, _ := strings.Cut(path, ".")
		if overage(id, top) || overage(userinfo, top) {
			return nil, missingOverage
		}
		return nil, missingAbsent
	}
	switch x := v.(type) {
	case string:
		return []string{x}, ""
	case []any:
		out := make([]string, 0, min(len(x), maxGroupValues))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, missingWrongType
			}
			if len(out) < maxGroupValues {
				out = append(out, s)
			}
		}
		return out, ""
	}
	return nil, missingWrongType
}

func overage(c claims, name string) bool {
	if c == nil {
		return false
	}
	names, ok := c["_claim_names"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = names[name]
	return ok
}

// compiledRule is a group rule ready to apply.
type compiledRule struct {
	match string
	re    *regexp.Regexp
	group string
}

func compileRules(rules []config.AuthOIDCGroupRule) []compiledRule {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		c := compiledRule{match: r.Match, group: r.Group}
		if r.MatchRegex != "" {
			// Validated at load; implicitly anchored to the whole value (R-AUTH-008).
			c.re = regexp.MustCompile(`^(?:` + r.MatchRegex + `)$`)
		}
		out = append(out, c)
	}
	return out
}

// applyRules maps claim values to trove group names: the union of every rule's
// results, sorted; results that violate the group-name rule of R-AUTH-018 are
// returned as skipped.
func applyRules(rules []config.AuthOIDCGroupRule, values []string) (groups, skipped []string) {
	compiled := compileRules(rules)
	seen := map[string]bool{}
	for _, v := range values {
		for _, r := range compiled {
			var target string
			switch {
			case r.re == nil && v == r.match:
				target = r.group
			case r.re != nil:
				sub := r.re.FindStringSubmatchIndex(v)
				if sub == nil {
					continue
				}
				target = string(r.re.ExpandString(nil, r.group, v, sub))
			default:
				continue
			}
			if seen[target] {
				continue
			}
			seen[target] = true
			if config.ValidGroupName(target) {
				groups = append(groups, target)
			} else {
				skipped = append(skipped, target)
			}
		}
	}
	slices.Sort(groups)
	return groups, skipped
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func sha256Of(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
