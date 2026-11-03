package archtest

// bastionRoot is the tree of the trove Bastion's own code (R-BAS-001, Q-BAS-22).
const bastionRoot = "internal/bastion"

// bastionAllowed is what the Bastion's packages may import from this module besides their own tree
// (R-BAS-001, Q-BAS-51). The role runs none of the main installation's subsystems: no PostgreSQL, no object
// store of the main installation's blobs, no authorization engine, no formats, no sessions, no login, no jobs,
// no remote repositories. The list is the platform the role shares with the others:
//
//   - internal/errs, internal/config, internal/buildinfo: error kinds, the configuration trees (the Bastion's
//     is config.BastionConfig) and the version;
//   - internal/observability and its health package: logs, metrics, traces and the probes;
//   - internal/tlsfiles and internal/httpserver (with its content wrapper): the TLS reloader, the listeners,
//     the client address and the problem rendering the role shares with the others;
//   - internal/outbound: the HTTP client factory, the one place an outbound dial is made (R-ARCH-022);
//   - internal/cluster: the Valkey client and its degradation (R-ARCH-031, R-BAS-034), for the shared state of
//     work package 6 only. cluster also holds the coordination of the main installation, which the Bastion does
//     not use; only its Valkey part is the Bastion's, and the Bastion's packages that import it are
//     internal/bastion/shared and below;
//   - internal/blob/s3: the S3 client for the bucket backend of the cache (R-BAS-030), for internal/bastion/cache
//     and below.
//
// The match is the exact package, so a package that did not exist when the rule was written is refused by
// default. A new entry names the requirement that justifies it.
func bastionAllowed() []string {
	return []string{
		"internal/errs", "internal/config", "internal/buildinfo",
		"internal/observability", "internal/observability/health",
		"internal/tlsfiles", "internal/outbound",
		"internal/httpserver", "internal/httpserver/content",
	}
}

// bastionScoped are the allowances that hold for a subtree of the Bastion only.
func bastionScoped() map[string][]string {
	return map[string][]string{
		"internal/cluster": {"internal/bastion/shared"},
		"internal/blob/s3": {"internal/bastion/cache"},
	}
}

// CheckBastionImports implements the import rule of R-BAS-001 as an allowlist: a package of the Bastion's tree
// imports from this module only the packages of bastionAllowed, its own tree, and the scoped allowances. Any
// other edge, including the PostgreSQL stores, the blob store, the authorization engine, the formats, the
// sessions and the jobs, is a violation that names the edge.
func CheckBastionImports(module string, pkgs []Package) []Violation {
	var out []Violation
	for _, p := range pkgs {
		if !under(p.Rel, bastionRoot) {
			continue
		}
		for _, imp := range p.Imports {
			target := rel(module, imp)
			if target == imp { // not a package of this module
				continue
			}
			if under(target, bastionRoot) || inList(target, bastionAllowed()) {
				continue
			}
			if scopes, ok := bastionScoped()[target]; ok && underAny(p.Rel, scopes) {
				continue
			}
			out = append(out, Violation{
				Rule: "R-BAS-001", From: p.Rel, To: target,
				Detail: "the role bastion enables none of the main installation's subsystems; its packages import the platform packages of bastionAllowed (internal/archtest/bastion.go) and their own tree, and nothing else",
			})
		}
	}
	sortViolations(out)
	return out
}
