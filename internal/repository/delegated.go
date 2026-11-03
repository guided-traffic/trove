package repository

import (
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/vuln"
)

// The upstream rules of a project administrator's write (R-AUTHZ-026, Q-REPO-127,
// Q-REPO-128). A project administrator configures the upstream of its project's
// remote repositories, but three things are not its to send to a host it picks: the
// secrets of the installation (the files of config_as_code.secrets_dir and the
// TROVE_CAC_ variables a secretRef names), a credential an administrator of trove
// stored for the repository, and the outbound path an administrator chose:
//
//   - a secret it gives is a value; a secretRef is refused, except the reference the
//     repository holds, sent back unchanged;
//   - a stored credential (a sealed value or that reference) stays only while the
//     hosts it goes to stay: a write that changes the url, adds a host that receives
//     credentials or turns forwardAuthOnRedirect on gives the value again;
//   - outboundProxy.mode direct and a sourceNetwork are an administrator's: a project
//     administrator sets the defaults, or keeps the stored values while the url and
//     the extra hosts stay.
//
// The unsaved upstream test of a project administrator has the rules of a create.

// delegatedRemote applies the rules to the document of a project administrator's
// write. cur is the stored document of an update, nil on a create and for an unsaved
// test.
func delegatedRemote(v *validation, w Write, cur, nd *Document) {
	if w.Delegate == "" || nd.Class != Remote {
		return
	}
	next, err := parseStoredRemote(nd.Remote)
	if err != nil {
		return // the section did not validate, and v says why
	}
	var stored *RemoteSettings
	if cur != nil {
		// A stored section that does not load holds nothing to keep: the write is
		// checked like a create.
		stored, _ = parseStoredRemote(cur.Remote)
	}
	checkDelegatedRemote(v, stored, next, nd.pending)
}

// checkDelegatedRemote adds the violations of a project administrator's remote
// section to v. stored is the section the repository holds (nil for none), next the
// validated section and pending its secret fields.
func checkDelegatedRemote(v *validation, stored, next *RemoteSettings, pending []pendingSecret) {
	credentialsMove := stored != nil && credentialHostsWiden(*stored, *next)
	for _, p := range pending {
		switch {
		case p.op == secretReference && !holdsReference(stored, p.ref):
			v.add(p.field+"/secretRef", errs.CodeSecretValueRequired,
				"a project administrator gives the secret as a value: a secretRef names a secret of the installation, and only the reference the repository holds may be sent back unchanged")
		case p.op != secretSeal && credentialsMove:
			v.add(p.field, errs.CodeSecretValueRequired,
				"the write changes where the stored credential goes (url, an extra host with sendCredentials, forwardAuthOnRedirect): give the value again, or remove it")
		}
	}
	pathMoves := stored == nil || pathHostsWiden(*stored, *next)
	if m := next.OutboundProxy.Mode; m != ProxyDefault && (pathMoves || stored.OutboundProxy.Mode != m) {
		v.add("/remote/outboundProxy/mode", errs.CodeOutboundPathRestricted,
			"a repository of a project takes the installation's outbound proxy (mode default); a mode an administrator of trove set stays only while url and extraHosts stay")
	}
	if sn := next.SourceNetwork; sn != nil && (pathMoves || stored.SourceNetwork == nil || *stored.SourceNetwork != *sn) {
		v.add("/remote/sourceNetwork", errs.CodeOutboundPathRestricted,
			"a repository of a project takes no source network of its own (null); one an administrator of trove set stays only while url and extraHosts stay")
	}
}

// holdsReference reports whether the stored section names ref as its password.
func holdsReference(stored *RemoteSettings, ref SecretRef) bool {
	return stored != nil && stored.Password != nil && stored.Password.Ref != nil && *stored.Password.Ref == ref
}

// credentialHostsWiden reports whether next sends the credentials to a host stored
// does not: another url, a sendCredentials pattern stored has not, or redirects
// that now carry them (R-REPO-033, R-REPO-039).
func credentialHostsWiden(stored, next RemoteSettings) bool {
	if next.URL != stored.URL || (next.ForwardAuthOnRedirect && !stored.ForwardAuthOnRedirect) {
		return true
	}
	return slices.ContainsFunc(next.ExtraHosts, func(h ExtraHost) bool {
		return h.SendCredentials && !slices.Contains(stored.ExtraHosts, h)
	})
}

// pathHostsWiden reports whether next reaches a host through the outbound path that
// stored does not: another url or an extra host stored does not name.
func pathHostsWiden(stored, next RemoteSettings) bool {
	if next.URL != stored.URL {
		return true
	}
	return slices.ContainsFunc(next.ExtraHosts, func(h ExtraHost) bool {
		return !slices.ContainsFunc(stored.ExtraHosts, func(o ExtraHost) bool { return o.Pattern == h.Pattern })
	})
}

// A project administrator may not weaken the scanning (R-VULN-002, Q-VULN-69). manageRepositories lets it change a repository
// of its project, but a download block lifts by a triage decision (vulnerabilities:triage) or a configuration of the whole
// installation (repositories:write-config); a write that switched the member off or turned a block off would lift the blocks
// of the repository without either.

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

// delegatedVulnerabilities refuses (403 vulnerability_weakening_refused) the write of a project administrator that weakens the
// member `vulnerabilities`, unless the writer holds vulnerabilities:triage. cur is the stored document.
func delegatedVulnerabilities(w Write, cur, nd *Document) error {
	if w.Delegate == "" || w.MayWeakenVulnerabilities {
		return nil
	}
	if weak := weakenedVulnerabilities(cur, nd); len(weak) > 0 {
		return errs.E(errs.Forbidden, errs.CodeVulnerabilityWeakeningRefused,
			"The write weakens vulnerabilities."+strings.Join(weak, ", vulnerabilities.")+
				": that needs repositories:write-config or vulnerabilities:triage, which manageRepositories does not carry.")
	}
	return nil
}
