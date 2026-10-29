// Package tlsfiles holds the TLS material that trove presents or trusts, read
// from PEM files and replaced without a restart (R-ARCH-035, INV-20).
//
// A [Reloader] owns one set of material: certificate/key pairs, CA bundles and,
// optionally, the system roots. It reads every file at construction, and again
// on every pass: [Reloader.Run] polls on an interval (the listeners),
// [Reloader.Refresh] re-reads on use (PostgreSQL per new connection, Valkey per
// handshake, outbound clients at most every few seconds). A pass compares the
// content (a SHA-256 over the bytes), never the file times, so a Kubernetes
// Secret volume, which swaps a symlink, and a file rewritten in place both work.
//
// Each pair and each bundle is one piece. A piece whose new content does not
// load keeps its previous content in service, logs an error and counts
// trove_tls_reload_errors_total once per distinct content; the other pieces swap
// independently. The pieces that loaded are published as one immutable snapshot
// behind an atomic pointer, so a handshake never sees half a rotation.
//
// The package is a leaf (R-ARCH-002): every layer may use it, it imports only the
// standard library and the Prometheus client, and it never dials.
package tlsfiles
