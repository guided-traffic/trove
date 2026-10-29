// Package cluster is trove's gateway to everything the replicas share. It is
// the only package that talks to Valkey (R-ARCH-002, R-ARCH-031) and one of the
// two that talk to PostgreSQL.
//
// The Valkey part is a client with Sentinel discovery, TLS and ACL, a registry
// of key and channel families in which every key has an expiry, a Pub/Sub fast
// path that follows the primary, a circuit breaker and a degraded mode.
// Valkey is never the only copy of anything (INV-3): a caller that gets
// [ErrUnavailable] runs its fallback and carries on.
//
// Files of the Valkey part are prefixed valkey_: valkey.go (client, lifecycle,
// status), valkey_ops.go (the commands trove needs), valkey_keys.go (the
// registry), valkey_pubsub.go, valkey_sentinel.go (discovery probe and startup
// classification), valkey_breaker.go, valkey_errors.go, valkey_tls.go,
// valkey_auth.go, valkey_metrics.go and valkey_version.go.
package cluster
