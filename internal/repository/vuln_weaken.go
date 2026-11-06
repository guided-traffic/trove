package repository

import "github.com/guided-traffic/trove/internal/vuln"

// weakensVulnerabilities lists what the new member does less than the stored one: scanning off, fewer public advisories
// matched (always above auto above never), the malicious block off, the block on missing data off.
func weakensVulnerabilities(cur, next vuln.Settings) []string {
	var out []string
	if cur.Enabled && !next.Enabled {
		out = append(out, "enabled")
	}
	if matchRank(next.MatchPublicAdvisories) < matchRank(cur.MatchPublicAdvisories) {
		out = append(out, "matchPublicAdvisories")
	}
	if cur.BlockMalicious && !next.BlockMalicious {
		out = append(out, "blockMalicious")
	}
	if cur.OnMissingData == vuln.MissingBlock && next.OnMissingData != vuln.MissingBlock {
		out = append(out, "onMissingData")
	}
	return out
}

func matchRank(m vuln.MatchPublic) int {
	switch m {
	case vuln.MatchAlways:
		return 2
	case vuln.MatchNever:
		return 0
	}
	return 1
}

// weakenedVulnerabilities is the members the write weakens, for the audit event. A document without the member is the
// defaults (a replace that leaves it out resets it; a patch carries the stored member into the new document).
func weakenedVulnerabilities(cur, nd *Document) []string {
	before, after := vuln.DefaultSettings(), vuln.DefaultSettings()
	if cur.Vulnerabilities != nil {
		before = *cur.Vulnerabilities
	}
	if nd.Vulnerabilities != nil {
		after = *nd.Vulnerabilities
	}
	return weakensVulnerabilities(before, after)
}
