package repository

import (
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// The upstream rules of an Owner's write (R-AUTHZ-026, Q-REPO-127,
// Q-REPO-128). An Owner configures the upstream of its project's
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
//     the extra hosts stay;
//   - the mirror mode is an administrator's: an Owner keeps remote.mode
//     and remote.mirror as stored, changes paused, or turns the mode back to
//     pullThrough (Q-REPO-142, validate_mirror.go); while the stored mode is mirror and
//     the write keeps it, the include and exclude patterns, the url and the extra hosts
//     stay as stored as well, because they decide what the mirror stores and from where
//     (Q-REPO-193).
//
// The unsaved upstream test of an Owner has the rules of a create.

// restrictedRemote applies the rules to the document of an Owner's
// write. cur is the stored document of an update, nil on a create and for an unsaved
// test.
func restrictedRemote(v *validation, c Caller, cur, nd *Document) {
	if !c.Restricted || nd.Class != Remote {
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
	checkDelegatedMirror(v, stored, next)
	if cur != nil {
		checkDelegatedMirrorScope(v, stored, next, cur, nd)
	}
}

// checkDelegatedRemote adds the violations of an Owner's remote
// section to v. stored is the section the repository holds (nil for none), next the
// validated section and pending its secret fields.
func checkDelegatedRemote(v *validation, stored, next *RemoteSettings, pending []pendingSecret) {
	credentialsMove := stored != nil && credentialHostsWiden(*stored, *next)
	for _, p := range pending {
		switch {
		case p.op == secretReference && !holdsReference(stored, p.ref):
			v.add(p.field+"/secretRef", errs.CodeSecretValueRequired,
				"an Owner gives the secret as a value: a secretRef names a secret of the installation, and only the reference the repository holds may be sent back unchanged")
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

// An Owner may not weaken the scanning (R-VULN-002, Q-VULN-69). The role owner lets it change a repository of its project, but a
// download block lifts by a triage decision (vulnerabilities:triage) or a configuration of the whole installation
// (repositories:write-config); a write that switched the member off or turned a block off would lift the blocks of the
// repository without either. vuln_weaken.go says what weakens.

// restrictedVulnerabilities refuses (403 vulnerability_weakening_refused) the write of a restricted caller that weakens the
// member `vulnerabilities`, unless the writer holds vulnerabilities:triage. cur is the stored document.
func restrictedVulnerabilities(c Caller, cur, nd *Document) error {
	if !c.Restricted || c.MayWeakenVulnerabilities {
		return nil
	}
	if weak := weakenedVulnerabilities(cur, nd); len(weak) > 0 {
		return errs.E(errs.Forbidden, errs.CodeVulnerabilityWeakeningRefused,
			"The write weakens vulnerabilities."+strings.Join(weak, ", vulnerabilities.")+
				": that needs repositories:write-config or vulnerabilities:triage, which the role owner does not carry.")
	}
	return nil
}
