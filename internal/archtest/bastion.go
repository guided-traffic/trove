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
//   - internal/qname: the address of a repository (project and key) and the one parser of the protocol paths, standard
//     library only (Q-REPO-212, R-BAS-018);
//   - internal/observability and its health package: logs, metrics, traces and the probes;
//   - internal/tlsfiles and internal/httpserver (with its content wrapper): the TLS reloader, the listeners,
//     the client address and the problem rendering the role shares with the others;
//   - internal/outbound: the HTTP client factory, the one place an outbound dial is made (R-ARCH-022);
//   - internal/cluster: the Valkey client and its degradation (R-ARCH-031, R-BAS-034), for the shared state
//     only. cluster also holds the coordination of the main installation, which the Bastion does not use; only
//     its Valkey part is the Bastion's, and the Bastion's packages that import it are internal/bastion/shared
//     and below (a scoped allowance, bastionScoped);
//   - internal/blob/s3: the S3 client for the bucket backend of the cache (R-BAS-030), for internal/bastion/cache
//     and below (scoped).
//
// The match is the exact package, so a package that did not exist when the rule was written is refused by
// default. A new entry names the requirement that justifies it.
//
// The rule is transitive (Q-BAS-129): what the allowed packages import in turn is held to the same list plus
// bastionTransitive, so the Bastion's dependency closure is these lists and nothing else.
func bastionAllowed() []string {
	return []string{
		"internal/errs", "internal/config", "internal/buildinfo", "internal/qname",
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

// bastionTransitive are the packages that enter the Bastion's dependency closure only because an allowed
// platform package imports them (Q-BAS-129). They are compiled into the role's binary; the role does not use or
// enable them: it opens no PostgreSQL pool, no object store of the main installation and builds no
// authorization engine (R-BAS-001; proven at runtime by TestR_BAS_001_TheRoleOpensNoConnectionToPostgreSQL in
// internal/bastion/server). A package that is neither here nor allowed above and enters the closure is a
// violation that names the chain. Narrowing them means splitting the importing platform package (H-473).
func bastionTransitive() map[string]transitiveEntry {
	cluster := []string{"internal/cluster"}
	httpserver := []string{"internal/httpserver"}
	const viaCluster = "internal/cluster holds the Valkey client the Bastion's shared state uses and, in the same package, the change log, the advisory locks and the replica registry of the main installation, which import "
	const viaHTTP = "internal/httpserver (the listeners and the request context the role shares) carries "
	return map[string]transitiveEntry{
		"internal/meta":            {cluster, viaCluster + "the metadata interfaces (changelog_notify.go, locks.go, signal_coalescer.go)"},
		"internal/meta/postgres":   {cluster, viaCluster + "the PostgreSQL store of the change log and the locks (changelog.go, locks.go); the role opens no pool"},
		"internal/meta/postgres/q": {cluster, viaCluster + "the generated queries of the change log and the replica registry"},
		"internal/backup/tables":   {cluster, viaCluster + "the table registry of the change log (changelog_tables.go, replica_tables.go)"},
		"internal/formats/catalog": {cluster, viaCluster + "the list of format areas of the change log (areas.go)"},
		"internal/audit":           {httpserver, viaHTTP + "SourceFrom, which builds the source of an audit event (audit.go); the role records no audit event of its own, its download reports go to the main installation (R-BAS-039)"},
		"internal/auth/scope":      {httpserver, viaHTTP + "the scope type of the request's Principal (principal.go)"},
		"internal/ci/claims":       {httpserver, viaHTTP + "the frozen CI claims of the request's Principal (principal.go)"},
		"internal/ci/reason":       {httpserver, viaHTTP + "the reasons of the claims package, which the Principal's type reaches"},
		"internal/backup/target":   {[]string{"internal/blob/s3"}, "internal/blob/s3 implements the backup target port (target.go, target_writer.go, target_copy.go) in the package that owns the AWS SDK; the Bastion's cache uses the S3 client only, never the target"},
		"internal/blob":            {[]string{"internal/blob/s3"}, "internal/blob/s3 implements the blob store interface of the main installation; the Bastion's cache uses the S3 client only, never the interface"},
	}
}

// transitiveEntry is an exception of bastionTransitive: the allowed platform packages that may import it (an
// exception is not a free pass for any platform package; the packages below an exception import one another
// freely, they are the exception's own tree) and why it is there.
type transitiveEntry struct {
	importers []string
	reason    string
}

// CheckBastionImports implements the import rule of R-BAS-001 as an allowlist, transitively. A package of the
// Bastion's tree imports from this module only the packages of bastionAllowed, its own tree and the scoped
// allowances; any other edge, including the PostgreSQL stores, the blob store, the authorization engine, the
// formats, the sessions and the jobs, is a violation that names the edge. What the allowed platform packages
// import in turn must be allowed or listed in bastionTransitive with its reason; a package that enters the
// closure otherwise is a violation that names the chain from the Bastion's package.
func CheckBastionImports(module string, pkgs []Package) []Violation {
	var out []Violation
	byRel := make(map[string]Package, len(pkgs))
	for _, p := range pkgs {
		byRel[p.Rel] = p
	}
	type step struct{ pkg, via string }
	var queue []step
	seen := map[string]bool{}
	for _, p := range pkgs {
		if !under(p.Rel, bastionRoot) {
			continue
		}
		for _, imp := range p.Imports {
			target := rel(module, imp)
			if target == imp || under(target, bastionRoot) { // not a package of this module, or the Bastion's own
				continue
			}
			if inList(target, bastionAllowed()) || underAny(p.Rel, bastionScoped()[target]) {
				if !seen[target] {
					seen[target] = true
					queue = append(queue, step{target, p.Rel + " -> " + target})
				}
				continue
			}
			out = append(out, Violation{
				Rule: "R-BAS-001", From: p.Rel, To: target,
				Detail: "the role bastion enables none of the main installation's subsystems; its packages import the platform packages of bastionAllowed (internal/archtest/bastion.go) and their own tree, and nothing else",
			})
		}
	}
	exceptions := bastionTransitive()
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range byRel[cur.pkg].Imports {
			target := rel(module, imp)
			if target == imp || under(target, bastionRoot) {
				continue
			}
			e, listed := exceptions[target]
			_, fromException := exceptions[cur.pkg]
			ok := inList(target, bastionAllowed()) || (listed && (fromException || inList(cur.pkg, e.importers)))
			if !ok {
				if !seen[cur.pkg+" -> "+target] {
					seen[cur.pkg+" -> "+target] = true
					out = append(out, Violation{
						Rule: "R-BAS-001", From: cur.pkg, To: target,
						Detail: "enters the dependency closure of the role bastion through " + cur.via + " -> " + target + "; list the edge in bastionTransitive with its reason (internal/archtest/bastion.go) or stop the platform package importing it",
					})
				}
				continue
			}
			if !seen[target] {
				seen[target] = true
				queue = append(queue, step{target, cur.via + " -> " + target})
			}
		}
	}
	sortViolations(out)
	return out
}
