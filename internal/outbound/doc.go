// Package outbound is the only place in trove that builds an HTTP client or
// dials a socket (R-ARCH-022, R-ARCH-002). Upstream registries, OIDC discovery
// and JWKS, the object store and webhooks all use a client from the Factory.
//
// The Factory applies to every client: the trust store (system roots plus
// proxy.outbound.tls.ca_files, or a client's own store from TrustStore, read
// again without a restart through internal/tlsfiles; TLS 1.2 minimum; no switch
// to turn verification off), the egress address policy in the dialer's Control hook (so DNS answers
// that change between check and use cannot get around it), connect, TLS and
// response-header timeouts, a redirect policy, outbound proxies, the
// User-Agent trove/<version>, W3C trace context propagation (only with
// tracing.propagate_to_upstreams), the upstream request log (R-OPS-011) and
// the outbound metrics. A remote repository's client adds WithLocalPrefix (the
// source address of R-REPO-024) and WithHopPolicy (which hosts receive credentials,
// custom headers and the client certificate on each redirect hop, R-REPO-039).
package outbound
