# Transport encryption

Status: implemented in M0 for every connection that trove makes or accepts: the client and ops listeners, the endpoint
listeners, PostgreSQL, Valkey and the Sentinels, the object store and the outbound HTTP clients. Plain HTTP is still a
chart choice (`tls.enabled: false`, [H-38](../spec/ha-and-helm.md#h-38--plain-http-between-the-edge-proxy-and-the-replicas)).
The requirement is [INV-20](../spec/README.md#design-invariants) and [ADR-127](../decisions.md#adr-127--transport-encryption-on-every-connection-troves-listeners-included).

This page says, per connection, what is encrypted, what the default verifies, where the material comes from and how a
rotation reaches a running process. The configuration keys are listed in the reference of [README.md](../../README.md#-full-reference).

## Connections and their defaults

| Connection | Configured by | Default | Verified by | Material reloaded |
|---|---|---|---|---|
| clients to the client listener and each endpoint listener | `server.tls`, `server.endpoints.<name>.tls` | plain HTTP until `certificates` is set | the client, not trove | every `reload_interval` (`10s`) |
| kubelet and monitoring to the ops listener | `server.ops_tls` | `inherit: true`: the client listener's pairs, no client certificate | the kubelet does not verify probes | with the listener |
| web and worker to PostgreSQL | `database.ssl_mode`, `ssl_root_cert_file`, `ssl_cert_file`, `ssl_key_file` | `verify-full` | chain and host name against the roots | on each new connection |
| web and worker to Valkey data nodes | `valkey.tls.mode`, `ca_file`, `cert_file`, `key_file`, `server_name` | `verify-full`, the system roots when no `ca_file` | chain and host name | on each handshake |
| web and worker to Sentinels | `valkey.sentinel.tls` (follows `valkey.tls`) | `true` | as the data nodes | on each handshake |
| web and worker to the object store | `storage.s3.endpoint`, `storage.s3.ca_file` | an endpoint without a scheme is `https` ([Q-STOR-15](../spec/storage.md#q-stor-15--object-store-endpoint-without-a-scheme)) | the system roots, or `ca_file` only | on each check of the outbound transport |
| outbound HTTP clients | `proxy.outbound.tls` | system roots, `min_version` `1.2` | chain and host name | at most every 10 s on use |
| OTLP span export | `tracing.ca_file`, `tracing.protocol` | off until `tracing.enabled` | the system roots, or `ca_file` | **once, at start** ([H-69](#h-69--tracingca_file-is-read-once-at-start)) |

Plaintext needs an explicit switch on each of these: `database.allow_insecure` for `disable`, `allow` and `prefer`,
`valkey.allow_insecure` for `valkey.tls.mode: disable` and `valkey.sentinel.tls: false`,
`storage.s3.allow_insecure_endpoint` for an `http://` object-store endpoint. The defaults and refusals are in the
`Validate` functions of [config/database.go](../../internal/config/database.go), [config/valkey.go](../../internal/config/valkey.go)
and [config/storage.go](../../internal/config/storage.go).

## Listener TLS

The listener's TLS is built by [httpserver/tls.go](../../internal/httpserver/tls.go) from a `server.tls` block
(`ListenerTLS`). The block is served when `certificates` is not empty; with none, the listener is plain HTTP.

- Versions: `min_version` `1.2` (default) or `1.3`. TLS 1.3 is preferred either way.
- TLS 1.2 suites: ECDHE with AES-GCM or ChaCha20-Poly1305 only. No CBC suites and no RSA key exchange. The list keeps the
  AES-128-GCM suites HTTP/2 requires ([tls.go](../../internal/httpserver/tls.go), `cipherSuites`).
- ALPN: `h2` and `http/1.1`.
- Certificates by SNI: the pair whose names hold the requested name exactly; else the first pair a wildcard covers that the
  client accepts; else the first pair ([tlsfiles/reloader.go](../../internal/tlsfiles/reloader.go), `Certificate`).
- Client certificates: `client_auth` is `none` (default), `verify_if_given` or `require`; the CA bundle is `client_ca_file`
  and a client certificate is verified against it. A further endpoint never inherits its `server.tls` client rules; with
  `client_ca_file` set, it requires one ([config/endpoints.go](../../internal/config/endpoints.go), `resolveEndpoint`).
- The ops listener with `inherit: true` takes the pairs, `min_version` and `reload_interval` of `server.tls` and no client
  authentication, because the probes cannot present a certificate ([config/tls.go](../../internal/config/tls.go),
  `OpsListenerTLS`). With `inherit: false` it has its own block, and a client CA on the client listener does not extend to
  it.

## Reloading material

Every TLS file of trove is read again and compared by content, not by time, so a Kubernetes Secret volume (which swaps a
symlink) and a file rewritten in place both work. The mechanism is one package, [internal/tlsfiles](../../internal/tlsfiles/doc.go).

- A **pass** reads each piece (one certificate/key pair or one CA bundle) and hashes its bytes. Unchanged content is a
  no-op. Changed content is parsed: a pair must be complete PEM, must load as an X.509 key pair and must not be expired
  ([reloader.go](../../internal/tlsfiles/reloader.go), `parsePair`, `complete`).
- A pair that fails to parse after a half-written rotation is read once more before it is refused.
- Pieces swap independently; the loaded pieces are published as one immutable snapshot behind an atomic pointer, so a
  handshake never sees half a rotation.
- A **broken rotation** keeps the previous content in service, counts it once in `trove_tls_reload_errors_total` and logs
  an error with the file names. Readiness does not change.
- A **broken start** holds the first readiness: the `tls` check of [wire_tls.go](../../internal/app/wire_tls.go) fails until
  every listener's material has loaded, and the listener binds and refuses handshakes until then. A PostgreSQL, Valkey or
  outbound client whose configured file does not load at start fails its construction, and the process ends
  ([postgres/connect.go](../../internal/meta/postgres/connect.go), `newDBTLS`; [cluster/valkey_tls.go](../../internal/cluster/valkey_tls.go),
  `newValkeyTLS`).
- A **connection keeps what it negotiated**. A rotation reaches the next handshake or the next connection; open connections
  keep their certificate ([H-65](#h-65--a-rotated-out-certificate-keeps-serving-open-connections)).
- Per listener, the reload interval is `server.tls.reload_interval`; the chart sets it through `tls.reloadInterval`.

The developer procedure, the metric families and the tests are in [tls.md](../developer/tls.md).

### Expiry

| Signal | Source |
|---|---|
| `trove_tls_certificate_expiry_timestamp_seconds{kind,listener,name}` | notAfter of the served leaf of each pair, the earliest of each bundle |
| `trove_tls_reloads_total{kind,listener,name}` | changed content that loaded and replaced the previous one |
| `trove_tls_reload_errors_total{kind,listener,name}` | content that did not load, once per distinct content |

`kind` is `listener`, `database`, `valkey`, `objectstore` or `outbound`. `listener` is `client`, `ops`, or `client:<endpoint>`
for a further endpoint, and empty for a client ([tlsfiles/metrics.go](../../internal/tlsfiles/metrics.go)). A certificate
that expires is seen only through the gauge: see [H-66](#h-66--a-loaded-certificate-is-served-past-its-expiry).

## Client connections

**PostgreSQL** ([postgres/connect.go](../../internal/meta/postgres/connect.go), [config/database.go](../../internal/config/database.go)).
`ssl_mode` takes `verify-full` (default), `verify-ca`, `require`, `disable`, `allow` or `prefer`. `verify-full` checks the chain
and the host name against the roots (the file in `ssl_root_cert_file`, or the system roots without one). `verify-ca` and
`require` with a root file check the chain only; `verify-ca` without a root file is refused, because the system roots would
be trusted without a host name check. `require` without a root file is refused unless `allow_insecure` is set, because
the password is then sent to whoever answers. The client certificate pair is optional and read on each new connection.

**Valkey and Sentinels** ([cluster/valkey_tls.go](../../internal/cluster/valkey_tls.go), [cluster/valkey.go](../../internal/cluster/valkey.go),
[config/valkey.go](../../internal/config/valkey.go)). `valkey.tls.mode` takes `verify-full` (default, system roots without a
`ca_file`), `verify-ca` or `disable`. `verify-ca` requires `ca_file` (refused at load and at dial). `disable` requires
`allow_insecure`. At start a Sentinel-mode replica logs a warning when the Sentinels have no password, and another when the
data nodes are verified against the system roots ([valkey.go](../../internal/cluster/valkey.go), `warnWeakTrust`). These are
warnings only: no configuration switch turns them into errors yet ([spec ha-and-helm](../spec/ha-and-helm.md#r-ha-056--valkey-with-sentinel-connection-and-supported-topologies)).

**Object store** ([blob/s3/client.go](../../internal/blob/s3/client.go)). Trove's object-store client takes its trust from
`storage.s3.ca_file` when it is set, which replaces the system roots for that client; the file is reloaded like any other
bundle. A proxy credential file is refused rather than ignored (`storage.s3.http.proxy_credentials_file`).

**Outbound HTTP** ([outbound/trust.go](../../internal/outbound/trust.go), [outbound/factory.go](../../internal/outbound/factory.go)).
`proxy.outbound.tls.use_system_roots` (default `true`) and `ca_files` form the trust store; `min_version` is `1.2`
(default) or `1.3`. A client rebuilds its transport at most every 10 s after a change of the store
(`DefaultTrustCheckEvery`), so a rotated bundle applies to the next connection after that check.

## What the chart renders

The chart is [deploy/helm/trove](../../deploy/helm/trove). The defaults below are from [values.yaml](../../deploy/helm/trove/values.yaml).

| Value | Default | Effect |
|---|---|---|
| `tls.enabled` | `true` | renders the TLS wiring; with `true` and no `tls.secretName` the render fails |
| `tls.secretName` | `""` (required with `enabled`) | the Secret that holds the listener's pair; the chart never creates it |
| `tls.certKey`, `tls.keyKey` | `tls.crt`, `tls.key` | the keys projected into the pod |
| `tls.minVersion`, `tls.reloadInterval` | `"1.2"`, `10s` | `server.tls.min_version` and `reload_interval` |
| `tls.clientCA.secretName` | `""` | when set, `server.tls.client_ca_file` and `client_auth` (`require` by default) |
| `tls.ops.secretName` | `""` | a separate pair for `:8081`; the ops listener then has its own block |
| `ingress.backendTLS.controller` | `haproxy` | the controller-specific backend settings: `haproxy`, `traefik`, `ingress-nginx` or `manual` |
| `ingress.backendTLS.verify` | `true` | the controller verifies the trove certificate; with `true` and no `caSecret` (haproxy, ingress-nginx) the render fails |

What the pods get, from [_secrets.tpl](../../deploy/helm/trove/templates/_secrets.tpl): the Secret is mounted read-only at
`/var/run/secrets/trove/tls/` with `tls.crt` and `tls.key` only. `ca.crt` is not projected into the process, because the
listener never reads it; the Secret itself still carries `ca.crt` for the edge and for monitoring. The mount has no
`subPath`, so the kubelet's swap of the projected files reaches the process. The migration Job mounts no listener material.

The probes use `scheme: HTTPS` on the ops port when `tls.enabled` is true ([_pod.tpl](../../deploy/helm/trove/templates/_pod.tpl)).
The client and ops Service ports declare `appProtocol: https` ([services.yaml](../../deploy/helm/trove/templates/services.yaml)).

For the HAProxy Ingress, the chart renders `haproxy-ingress.github.io/secure-backends: "true"`, the verification secret
(`secure-verify-ca-secret`), the server name `<fullname>.<namespace>.svc` (`secure-sni`) and, with a client certificate,
`secure-crt-secret` ([ingress.yaml](../../deploy/helm/trove/templates/ingress.yaml)). The render refuses `clientCA` with
HAProxy and no `clientCertSecret` ([_validate.tpl](../../deploy/helm/trove/templates/_validate.tpl)).

Not rendered yet: a `ServiceMonitor` for Prometheus (the TLS settings of the scrape are therefore not rendered either),
and an `HTTPRoute`. The Gateway API `BackendTLSPolicy` is rendered only with `tls.backendTLSPolicy.enabled`
([backendtlspolicy.yaml](../../deploy/helm/trove/templates/backendtlspolicy.yaml)); it takes its CA from a ConfigMap.

## What this does not cover

### H-64 · Sentinel trust with the chart defaults

Stage: the connection to each Sentinel and the primary it names ([cluster/valkey.go](../../internal/cluster/valkey.go),
`warnWeakTrust`).

Live with the chart defaults: `secrets.valkeySentinel.existingSecret` is empty, so Sentinels are reached without a password,
and `secrets.valkeyCA.existingSecret` is empty, so the data nodes are verified against the system roots. Subject: which
primary a replica uses. Whoever can send a command to a Sentinel can name another primary. A primary that Sentinel names
passes verification with the system roots when its certificate chains to a public root for the name it is dialed by.

What makes the first half real: any pod that reaches a Sentinel port, which the Valkey operator's policy must restrict. The
second half needs a certificate that a public CA issued for the cluster-internal name of a data node, which is not normally
possible. Both are warned at start, not refused.

### H-65 · A rotated-out certificate keeps serving open connections

Stage: every TLS connection that is open when a certificate is rotated, on the listeners and on the clients.

Live on every installation that rotates a certificate while connections are open. The old certificate stays in use until the
connection closes; a compromised key stays usable through it until then. Subject: client connections to the listeners, and
the trove-side connections to PostgreSQL, Valkey and the object store, which are recycled by their own lifetimes
(`database.pool.max_conn_lifetime`, `valkey.max_conn_lifetime`).

What makes it real: a key compromised before the rotation and an existing session the attacker can keep. Rotation does not
terminate sessions.

### H-66 · A loaded certificate is served past its expiry

Stage: the listener's certificate after its notAfter ([tlsfiles/reloader.go](../../internal/tlsfiles/reloader.go), `passPiece`).

Live when a renewal does not happen. The reloader checks expiry only when the content changes; an unchanged pair stays in
service after its notAfter, and no pass re-checks it. Verifying clients then refuse the handshake, so the effect is an
outage. The probes do not verify the certificate and keep answering. Subject: the availability of every client of the
listener. The signals are the expiry gauge and the alert suggested in [tls.md](../developer/tls.md).

What makes it real: a renewal that stops producing new content, with the gauge not alerted.

### H-67 · No revocation check for client certificates

Stage: the client certificate on a listener with `client_auth` set ([httpserver/tls.go](../../internal/httpserver/tls.go)).

Live on an installation that sets `tls.clientCA` or `server.tls.client_ca_file` (dormant by default, since the chart's
`tls.clientCA.secretName` is empty). Trove checks a client certificate against its CA bundle only. It performs no CRL or OCSP
check and does not staple OCSP responses. A certificate that the CA revokes is accepted until it expires. Subject: the
clients of the mutual-TLS listener.

What makes it real: a revoked client certificate that is still within its validity. The mitigation is a short validity of the
issued certificates and a rotation of the CA bundle, both outside trove.

### H-68 · A missing pair at start restarts the pod

Stage: start-up of a replica whose TLS pair is absent or does not load ([wire_tls.go](../../internal/app/wire_tls.go)).

Live with `tls.enabled: true` when the Secret is missing or its pair does not load at start. The listener binds and refuses
handshakes, so the HTTPS probes fail. The startup probe allows `failureThreshold` times `periodSeconds` (60 times 2 s by the
chart default) before the kubelet restarts the container; liveness takes over afterwards. A pod whose pair arrives late can
therefore still be restarted before the material appears. Subject: availability of the replica, not its security.

What makes it real: a Secret that is created after the pod, or a broken certificate deployed in a rollout.

### H-69 · `tracing.ca_file` is read once at start

Stage: the OTLP exporter's trust store ([observability/otlp.go](../../internal/observability/otlp.go), `newOTLPExporter`).

Dormant: live only with `tracing.enabled` and a `tracing.ca_file` whose CA rotates. The file is read when the exporter is
built, and the exporter keeps that pool. A rotated collector CA is not trusted until the process restarts, and the export fails
in the meantime. The span export itself has no egress policy ([H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy)).

What makes it real: a collector certificate issued by a new CA while the process keeps the old one.
</content>
