// Package upstream builds and caches the HTTP client of each remote repository
// (R-REPO-024, R-REPO-033, R-REPO-038, R-REPO-039) and performs the upstream test.
//
// A [Client] is the outbound client of internal/outbound with the settings of one
// remote repository applied: its timeouts, the redirect limit, the source network,
// the proxy mode and a hop policy that sends the credentials only to the upstream
// origin and the extraHosts entries that take them. The credentials are resolved
// here, on the replica that uses them: a sealed value is unsealed with the key
// ring under the row key (repository id, field), a secretRef is read from a file
// directly under config_as_code.secrets_dir or from a TROVE_CAC_ variable, and a
// value that cannot be resolved fails the request with 502
// upstream_secret_unavailable while other repositories go on.
//
// [Clients] keeps the built clients per (repository id, config_version) in a
// registered cache of the area repo_config, so a changed configuration or a
// replaced secret is a new key, and the connection pool of an unchanged
// repository survives. Nothing here counts toward the circuit of R-REPO-029: the
// engine decides what a failure means; this package classifies it ([Classify]).
package upstream
