# TLS material: loading, reloading, rotating, testing

Every TLS certificate trove presents and every CA bundle it trusts is a PEM file that is read again without a
restart ([R-ARCH-035](../spec/architecture.md), INV-20). This page says where the files come from, how the
reloader decides that something changed, what a rotation looks like from the outside, and how the behaviour is
tested. Configuration keys: [configuration.md](configuration.md); listeners: [architecture.md](architecture.md#tls-on-the-listeners).

## One mechanism: `internal/tlsfiles`

[internal/tlsfiles](../../internal/tlsfiles) is a leaf package (R-ARCH-002): every layer uses it, it imports the
standard library and the Prometheus client, and it never dials.

| File | Responsibility |
|---|---|
| [reloader.go](../../internal/tlsfiles/reloader.go) | `Reloader`: pieces, passes, the published snapshot, the handshake callbacks |
| [metrics.go](../../internal/tlsfiles/metrics.go) | the three metric families and the `Kind…` label values |
| [tlstest/tlstest.go](../../internal/tlsfiles/tlstest/tlstest.go) | throwaway CA and leaves for tests; imported only by `_test.go` files and `test/` |

A `Reloader` holds one set of material, described by a `tlsfiles.Spec`: certificate/key pairs (`Pairs`, the first is
the default), CA bundles (`CAFiles`) and optionally the system roots. Each pair and each bundle is a **piece** with its
own state:

```text
pass (Reload / Refresh / Run tick)
  for every piece:
    read the file(s) ── read error ──────────────► keep the last good content; count + log once per distinct error
    sha256(len|bytes …)
      == content served now ──────────────────────► nothing to do (a touched mtime changes nothing)
      == content that failed last ───────────────► nothing to do (counted and logged already)
      parse: pair (X509KeyPair, complete PEM, leaf not expired) / bundle (≥ 1 certificate, complete PEM)
        fails, pair ──► read both files once more; changed? parse that   (a rotation caught half-way)
        fails ────────► keep the last good content; count + log once for this content
        loads ────────► swap the piece; trove_tls_reloads_total + info log (not for the first load)
  any swap ──► publish a new immutable snapshot (atomic pointer), Generation()++
```

The handshake callbacks never touch a file. They read the snapshot:

| Callback | Used by | Behaviour |
|---|---|---|
| `Certificate` (`GetCertificate`) | listeners | the pair whose DNS names hold the SNI name exactly; else the first pair the client supports whose names match (wildcards, via `ClientHelloInfo.SupportsCertificate`); else the first pair. No pair loaded: the handshake fails |
| `ClientCertificate` (`GetClientCertificate`) | PostgreSQL, Valkey | the configured pair; none configured: no certificate; configured but never loaded: the handshake fails (never a silent connection without it) |
| `Pool` | listeners (client CAs), all clients (roots) | system roots (if asked) plus every bundle that loaded. **Never nil when a bundle is configured**: a bundle that never loaded gives an empty pool, which trusts nothing, because a nil `RootCAs` would mean the system roots |

`Loaded()` is nil once every piece has loaded at least once; `Err()` reports the pieces whose current files did not
load (they serve their previous content). `Generation()` lets a consumer cache derived state: read it before `Pool`.

### When files are read

| Material | Reader | When | Bound |
|---|---|---|---|
| listener pairs and client CA | `httpserver.ListenerTLS` | `Run` polls every `reload_interval` (default `10s`) while the listener accepts | new handshakes within `reload_interval` (E0.15 measures `reload_interval + 1 s`) |
| `database.ssl_root_cert_file`, `ssl_cert_file`/`ssl_key_file` | `postgres` `dbTLS`, in pgx `BeforeConnect` | every new connection | the next connection |
| `valkey.tls.ca_file`, `cert_file`/`key_file` | `cluster` `valkeyTLS`, in the dial function | every new handshake (data nodes and Sentinels) | the next handshake |
| `proxy.outbound.tls.ca_files`, `storage.s3.ca_file` | `outbound` `trustedTransport` | on a request, at most every `outbound.DefaultTrustCheckEvery` (10 s) | the next connection after the check; within the 60 s of R-REPO-035 |

Why polling and not a file watcher: Q-ARCH-31. A Kubernetes Secret volume replaces files by swapping a symlink; a
content hash on a timer sees that on every volume type and cannot miss an event.

### Start versus rotation

| Situation | Listener | Client (PostgreSQL, Valkey, object store, outbound) |
|---|---|---|
| file absent or broken **at start** | the listener binds and refuses handshakes; the `tls` readiness check (first readiness only) holds the replica unready; the pair is served as soon as it is valid, no restart | configuration error: `Open`, `NewValkey`, `NewFromFactory` fail and the process ends, as before; the outbound factory's own CA files hold the `outbound` first readiness and its clients fail closed until the files load |
| file broken **after** a good load | the previous pair stays; `trove_tls_reload_errors_total` +1; ERROR log with the file names and the reason; readiness unchanged | the previous material stays; the same counter and log |

A connection keeps what it negotiated: an open TLS connection is not touched by a rotation, and the outbound
transport of the previous generation keeps its open connections and loses its idle ones. A client that resumes a TLS
session (session ticket) sees no certificate in the resumed handshake; a full handshake shows the new one.

### Metrics

Registered once per process (`tlsfiles.NewMetrics`, in `internal/app` telemetry) and passed to every reloader. Labels:
`kind` (`listener`, `database`, `valkey`, `objectstore`, `outbound`), `listener` (`client`, `client:<endpoint>` for a further endpoint, `ops`, empty for clients),
`name` (the configured certificate or bundle path). All bounded by the configuration.

| Family | Meaning |
|---|---|
| `trove_tls_certificate_expiry_timestamp_seconds` | notAfter of the served leaf of a pair; the earliest notAfter of a bundle |
| `trove_tls_reloads_total` | changed content that was loaded and swapped in (the first load is not counted) |
| `trove_tls_reload_errors_total` | content that did not load, once per distinct content or read error; exported at 0 from the start |

An expiry alert is `trove_tls_certificate_expiry_timestamp_seconds - time() < 14 * 86400`; a failed rotation is
`increase(trove_tls_reload_errors_total[15m]) > 0`.

## TLS on the listeners

`httpserver.NewListenerTLS(listener, block, opts...)` builds a `ListenerTLS` from a `config.ServerTLS` block (nil
without certificates: plain HTTP). `ServerConfig.TLS` takes it; `Serve` runs its reloader while it accepts.
Every listener has its own block and its own reloader, so a further endpoint listener (R-EP-004) takes
its own certificates the same way: `startEndpointTLS` ([wire_endpoints.go](../../internal/app/wire_endpoints.go)) builds one
`ListenerTLS` per endpoint from its effective block (the endpoint's pairs, else `server.tls`'s; `client_ca_file` of the
endpoint only; `min_version` and `reload_interval` of `server.tls`), labelled `client:<name>`. SNI picks among the
endpoint's own pairs only ([endpoints.md](endpoints.md#tls)). The handshake goes through `GetConfigForClient`, which returns one `*tls.Config`
per material generation:

| Setting | Value |
|---|---|
| versions | `min_version` `1.2` (default) or `1.3`; TLS 1.3 preferred |
| TLS 1.2 suites | ECDHE with AES-GCM or ChaCha20-Poly1305 only (`cipherSuites` in [tls.go](../../internal/httpserver/tls.go)); no CBC, no RSA key exchange |
| ALPN | `h2`, `http/1.1` |
| client certificates | `client_auth`: `none` (default), `verify_if_given`, `require`; verified against `client_ca_file`, reloaded like the pairs |

The ops listener: `server.ops_tls.inherit: true` (default) serves `server.tls`'s pairs, `min_version` and
`reload_interval` and asks for no client certificate, because the kubelet's probes cannot present one; `inherit:
false` gives it its own block. A plaintext request to a TLS listener gets net/http's `400` ("Client sent an HTTP
request to an HTTPS server") and never reaches a handler.

## How to rotate

- **Kubernetes (the chart).** cert-manager (or anything else) updates the Secret; the kubelet swaps the projected
  volume; the reloader sees the new content within `reload_interval`. Nothing else to do; watch
  `trove_tls_reloads_total` rise and the expiry gauge move. The chart must mount the Secret without `subPath`, or the
  kubelet never updates the file.
- **Files on a host.** Replace the files in place. Writing the key and then the certificate (or the other way) is
  safe: a pass that reads them half-way refuses the pair once and keeps the old one, the next pass loads the new
  pair. Writing each file by rename is cleaner and avoids the one counted error.
- **A CA change for clients** (new database CA, new Valkey CA): put old and new CA into the bundle first, rotate the
  servers' certificates, then drop the old CA. Each step applies to the next connection or handshake.

## How to test

| Tier | Where | What it proves |
|---|---|---|
| unit | [internal/tlsfiles](../../internal/tlsfiles/reloader_test.go) | swap within the interval; content not mtime; a mismatched key, a half-written file, garbage, an expired leaf keep the old pair and count once; a pair missing at start serves once it appears; SNI exact, wildcard, case, default, RSA; empty-not-nil pools; Refresh throttle; the expiry gauge |
| unit | [internal/httpserver](../../internal/httpserver/tls_test.go) | real handshakes: h2 and http/1.1 over ALPN, plaintext refused, rotation with an open connection keeping its certificate, missing pair at start, SNI, `client_auth` `require` and `verify_if_given`, client CA rotation, `min_version` 1.3, AEAD-only TLS 1.2 |
| unit | [internal/app](../../internal/app/tls_test.go) | both listeners over TLS by inheritance, the `tls` readiness check holding the first readiness and clearing without a restart, a broken rotation keeping the replica ready, the families on `/metrics` |
| unit | [internal/meta/postgres](../../internal/meta/postgres/tls_test.go), [internal/cluster](../../internal/cluster/valkey_tls_test.go), [internal/outbound](../../internal/outbound/trust_test.go), [internal/blob/s3](../../internal/blob/s3/tls_test.go) | per-connection (PostgreSQL: a fake server that answers the SSLRequest), per-handshake (Valkey) and per-check (outbound, S3) reload of CA and client certificates |
| integration | [test/integration/tls](../../test/integration/tls) | the built binary with TLS on both listeners, rotation under load (E0.15 at process level: no failed request, new serial within `reload_interval + 1 s`, same process); PostgreSQL verify-full and an in-place CA rewrite; Valkey and Sentinels likewise per handshake; SeaweedFS over https with `storage.s3.ca_file` |

Run the integration suite with the stack up:
`set -a; . .dev/integration/env; set +a; go test -tags integration -race ./test/integration/tls/...`. Each test
rewrites only its own copy of the stack's CA and restores it; nothing of the shared stack is restarted.
Certificates for trove's own listeners come from `tlstest.NewCA` in the test; no openssl is needed.

## What this does not cover

- Client certificates against the real servers: the integration stack's PostgreSQL, PgBouncer and Valkey do not
  request client certificates (`tls-auth-clients no`, no `ssl_ca_file`), so the per-connection reload of a client
  pair is proven only against the fake servers of the unit tests.
- `tracing.ca_file` is read once at start (`internal/observability`); it is the one outbound CA outside this mechanism.
- No OCSP stapling, no CRL or OCSP check of client certificates on the listeners.
