# TLS and certificates

This page explains where trove uses TLS, which Secrets and files carry the certificates, how client certificates work on trove's listeners, how trove verifies PostgreSQL, Valkey and the object store, and how certificates are rotated while trove keeps running. It ends with a cert-manager example and a list of common failures.

## Where trove uses TLS

Every connection trove accepts or makes can be encrypted. The listeners serve plain HTTP until a certificate is configured; the Helm chart turns TLS on for them by default. Outgoing connections verify their peer by default, and PostgreSQL, Valkey, the object store and the span export each need an explicit switch for plaintext.

| Connection | Configured by | Default | Material read again |
|---|---|---|---|
| Clients to the client listener (`:8080`) and each endpoint listener | `server.tls`, `server.endpoints.<name>.tls` | plain HTTP until certificates are set; the chart sets them | every `reload_interval` (10 s) |
| Probes and monitoring to the ops listener (`:8081`) | `server.ops_tls` | the client listener's certificate, no client certificate | with the listener |
| `api`, `packages` and `worker` to PostgreSQL | `database.ssl_mode` and the `ssl_*` files | `verify-full` | on each new connection |
| `api`, `packages` and `worker` to Valkey and the Sentinels | `valkey.tls`, `valkey.sentinel.tls` | `verify-full`, system roots without a CA file | on each handshake |
| `api`, `packages` and `worker` to the object store | `storage.s3.endpoint`, `storage.s3.ca_file` | `https`, the outbound trust store | on use, at most every 10 s |
| Remote repositories to their upstreams | `proxy.outbound.tls` | system roots, TLS 1.2 or later | on use, at most every 10 s |
| Span export (tracing) | `tracing.ca_file` | off | once, at start |

## TLS on trove's own listeners

### Client listener

`server.tls.certificates` lists one or more certificate and key pairs as PEM files. With no pair the listener speaks plain HTTP; a plain HTTP request to a TLS listener gets `400` with the text "Client sent an HTTP request to an HTTPS server".

| Setting | Values |
|---|---|
| Versions | `min_version` `1.2` (default) or `1.3`; TLS 1.3 is preferred either way |
| TLS 1.2 cipher suites | ECDHE with AES-GCM or ChaCha20-Poly1305 only; no CBC suites, no RSA key exchange |
| ALPN | `h2` and `http/1.1` |
| Certificate choice | the pair whose names hold the requested server name (SNI) exactly; else the first pair whose wildcard covers it; else the first pair |

### Further endpoints

Each further endpoint has its own listener and its own certificate set. Without certificates of its own, an endpoint serves the pairs of `server.tls`. With several pairs (for example an exact host and a wildcard), SNI picks among that endpoint's pairs only; a pair of another endpoint is never offered. `min_version` and `reload_interval` always come from `server.tls`. See [Endpoints and exposure rules](endpoints.md#configure-endpoints-with-the-helm-chart) for the endpoint values.

### Ops listener

With `server.ops_tls.inherit: true` (the default) the ops listener serves the client listener's pairs and asks for no client certificate, because the kubelet's probes cannot present one. With `inherit: false` it has its own block of the same keys. The chart sets this for you: `tls.ops.secretName` gives the ops listener a pair of its own.

If a replica starts while its certificate is missing or broken, the ops listener answers with a temporary certificate that trove generates in memory (valid for one hour, never written to disk). The probes then reach `/healthz` (200) and `/readyz` (503, check `tls` failing), so the pod is not restarted, and the gauge `trove_tls_temporary_certificate{listener="ops"}` is 1. The client and endpoint listeners refuse handshakes until their pair loads. The temporary certificate chains to no CA, so a scraper that verifies the ops listener fails until the real pair loads.

## Certificates in the Helm chart

The chart never creates a Secret. Create each Secret before the install, for example with cert-manager. The chart mounts every Secret as a read-only directory without `subPath`, so a Secret update reaches the running process. Certificate Secrets do not roll the pods when their content changes.

```yaml
tls:
  enabled: true                         # default; false = plain HTTP inside the cluster
  secretName: trove-tls                 # example; required while enabled; keys tls.crt and tls.key
  certKey: tls.crt                      # default
  keyKey: tls.key                       # default
  minVersion: "1.2"                     # default: 1.2 | 1.3
  reloadInterval: 10s                   # default
  clientCA:
    secretName: ""                      # default: no client certificates on :8080
    key: ca.crt                         # default
    auth: require                       # default: require | verify_if_given
  ops:
    secretName: trove-ops-tls           # example; default "" = :8081 serves the pair of tls.secretName
ingress:
  backendTLS:
    controller: haproxy                 # default: haproxy | traefik | ingress-nginx | manual
    verify: true                        # default: the controller verifies trove's certificate
    caSecret: trove-tls                 # example; a Secret with ca.crt, required with verify on haproxy and ingress-nginx
    clientCertSecret: ""                # default; haproxy: the pair the controller presents when tls.clientCA is set
```

Set `tls.ops.secretName` when you run worker replicas or endpoint pools: without it every pod holds the private key of the main listener, because its ops listener inherits that pair.

The ingress controller connects to trove over HTTPS. With HAProxy Ingress the chart has the controller verify trove's certificate for the name `<fullname>.<namespace>.svc`, so that name must be in the certificate; an endpoint's Ingress verifies `<fullname>-ep-<name>.<namespace>.svc` against the endpoint's certificate. A cert-manager Secret carries `ca.crt`, so `caSecret` can name the same Secret as `tls.secretName`.

<details>
<summary>Secrets and where the pods see them</summary>

| Chart value | Keys read | Mounted at | Used for |
|---|---|---|---|
| `tls.secretName` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/tls/` | client listener, and the ops listener while it inherits |
| `tls.clientCA.secretName` | `ca.crt` | `/var/run/secrets/trove/tls-client-ca/ca.crt` | client certificates on `:8080` (main `api` and `packages` pods only) |
| `tls.ops.secretName` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/tls-ops/` | ops listener |
| `endpoints.<name>.tls.existingSecret`, `extraSecrets` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/endpoints/<name>/<n>/` (`n` = position) | endpoint listener |
| `endpoints.<name>.tls.clientCaSecret` | `ca.crt` | `/var/run/secrets/trove/endpoints/<name>/client-ca/ca.crt` | client certificates on the endpoint |
| `secrets.databaseCA` | `ca.crt` | `/var/run/secrets/trove/database-ca/ca.crt` | `database.ssl_root_cert_file` |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/database-client/` | client certificate to PostgreSQL |
| `secrets.valkeyCA` | `ca.crt` | `/var/run/secrets/trove/valkey-ca/ca.crt` | `valkey.tls.ca_file` |
| `secrets.valkeyClientCert` | `tls.crt`, `tls.key` | `/var/run/secrets/trove/valkey-client/` | client certificate to Valkey |
| `secrets.s3CA` | `ca.crt` | `/var/run/secrets/trove/s3-ca/ca.crt` | `storage.s3.ca_file` |

The chart sets these file paths in the configuration itself; setting the same keys under `config` fails the render. The complete list of values is in the [full reference](https://github.com/guided-traffic/trove/blob/main/README.md#-full-reference).

</details>

## Client certificates (mutual TLS)

A listener can ask its clients for a certificate. This is meant for the connection from your ingress controller to trove, not for user sign-in: a client certificate is checked against the CA bundle and nothing else, it does not identify a user, and a user still needs a session or a token.

| Listener | Setting | Behaviour |
|---|---|---|
| Client listener | `tls.clientCA.secretName` with `auth: require` | a client without a certificate from that CA is refused in the handshake |
| Client listener | `tls.clientCA.secretName` with `auth: verify_if_given` | a certificate is checked if the client presents one |
| Further endpoint | `endpoints.<name>.tls.clientCaSecret` | always required; never inherited from the client listener |
| Ops listener | never set by the chart | the probes cannot present a certificate |

With HAProxy Ingress and `tls.clientCA`, also set `ingress.backendTLS.clientCertSecret` to a Secret with a `tls.crt` and `tls.key` that the CA signed; the render fails otherwise. trove checks no revocation lists and no OCSP: a revoked client certificate is accepted until it expires, so issue short-lived certificates.

## TLS to PostgreSQL, Valkey and the object store

### PostgreSQL

`database.ssl_mode` is `verify-full` by default: the chain and the host name are checked against `ssl_root_cert_file`, or the system roots without one. For a database with a private CA, such as a CloudNativePG cluster, put its CA into a Secret and name it in `secrets.databaseCA`.

| `ssl_mode` | Needs |
|---|---|
| `verify-full` | a root file, or a server certificate from a public CA |
| `verify-ca` | `ssl_root_cert_file`; checks the chain but not the host name |
| `require` | a root file, or `allow_insecure: true` |
| `disable`, `allow`, `prefer` | `allow_insecure: true` |

A client certificate is optional (`secrets.databaseClientCert`).

### Valkey and Sentinel

`valkey.tls.mode` is `verify-full` (default), `verify-ca` (needs `ca_file`) or `disable` (needs `valkey.allow_insecure`). The Sentinel connections use the same TLS settings while `valkey.sentinel.tls` is `true`. Name the CA of your Valkey certificates in `secrets.valkeyCA`. In Sentinel mode trove logs a warning at start when the Sentinels have no password or when the data nodes are verified against the system roots; both are warnings, not errors.

### Object store

An endpoint without a scheme is reached over `https`; an `http://` endpoint needs `storage.s3.allow_insecure_endpoint: true`. `secrets.s3CA` renders `storage.s3.ca_file`, which then becomes the whole trust store of the object-store client: the system roots and `proxy.outbound.tls.ca_files` no longer apply to it. Without it, the object store is verified like the upstreams of remote repositories (system roots plus `proxy.outbound.tls.ca_files`).

## Rotating certificates without a restart

### How it works

trove reads every certificate, key and CA file again and compares the content, not the file time, so both a Kubernetes Secret update and a file replaced on disk are noticed.

- Listener pairs and client CAs are checked every `reload_interval` (10 s). New handshakes use the new certificate; open connections keep the one they negotiated until they close.
- PostgreSQL files apply to the next new connection, Valkey files to the next handshake. Pooled connections are replaced after `database.pool.max_conn_lifetime` and `valkey.max_conn_lifetime` (30 minutes each by default).
- Object-store and upstream CA files apply to the next connection after a check, at most every 10 s.
- A file that does not load (half-written, key and certificate not matching, expired, garbage) leaves the previous material in service, adds one to `trove_tls_reload_errors_total` and logs an error that names the file. A pair whose validity starts in the future waits and is checked again on every pass.
- `tracing.ca_file` is the exception: it is read once at start, so a new collector CA needs a restart.

At start the behaviour is stricter: a listener pair that does not load keeps the replica unready until it loads, and a PostgreSQL, Valkey or `storage.s3.ca_file` file that does not load ends the process.

### Rotate a listener certificate

1. Update the Secret, or let cert-manager renew it.
2. Wait until the kubelet has updated the mounted files and one `reload_interval` has passed.
3. Check that `trove_tls_reloads_total` for the listener has increased and `trove_tls_certificate_expiry_timestamp_seconds` shows the new expiry.

For files outside Kubernetes, replace the files in place. Writing the key and the certificate one after the other is safe (a pass that sees half a rotation keeps the old pair and counts one error); replacing each file by a rename avoids that error.

### Rotate a CA

1. Put the old and the new CA into the bundle that trove trusts (for example `ca.crt` of `secrets.databaseCA`).
2. Rotate the server certificates to the new CA.
3. Remove the old CA from the bundle.

Each step applies to the next connection or handshake.

### Watch expiry and reloads

| Metric | Meaning |
|---|---|
| `trove_tls_certificate_expiry_timestamp_seconds` | expiry of each served certificate, and the earliest expiry in each CA bundle |
| `trove_tls_reloads_total` | changed files that loaded and replaced the previous content |
| `trove_tls_reload_errors_total` | changed files that did not load; the old material stays in service |

The labels are `kind` (`listener`, `database`, `valkey`, `objectstore`, `outbound`), `listener` (`client`, `client:<endpoint>`, `ops`, empty for outgoing connections) and `name` (the file). Alert on `trove_tls_certificate_expiry_timestamp_seconds - time() < 14 * 86400` and on `increase(trove_tls_reload_errors_total[15m]) > 0`. trove keeps serving a loaded certificate after it has expired, and the probes do not notice, so the expiry alert is the only warning before clients start refusing the connection.

## cert-manager example

A Certificate for the main listener of a release `trove` in the namespace `trove`. The names cover the base URL host, the Service name the ingress controller verifies, and the ops Service name for a monitoring system that verifies the ops listener.

```yaml
apiVersion: cert-manager.io/v1         # example
kind: Certificate                       # example
metadata:
  name: trove-tls                       # example
  namespace: trove                      # example
spec:
  secretName: trove-tls                 # example; the value of tls.secretName and ingress.backendTLS.caSecret
  duration: 2160h                       # example: 90 days
  renewBefore: 720h                     # example
  privateKey:
    algorithm: ECDSA                    # example
    size: 256                           # example
    rotationPolicy: Always              # example: a renewal also changes the key
  usages: [digital signature, key encipherment, server auth]   # example
  dnsNames:
    - trove.example.com                 # example: the host of server.base_url
    - trove.trove.svc                   # example: <fullname>.<namespace>.svc, verified by HAProxy Ingress
    - trove-ops.trove.svc               # example: the ops Service, if your scraper verifies it
  issuerRef:
    name: internal-ca                   # example: your Issuer or ClusterIssuer
    kind: ClusterIssuer                 # example
```

cert-manager renews the Secret before it expires; trove picks up the new pair without a restart. Further endpoints get Certificates of their own with the endpoint's hosts and `<fullname>-ep-<name>.<namespace>.svc`.

## Common failures

| Symptom | Cause | What to do |
|---|---|---|
| The chart does not render: `tls.secretName` is required | `tls.enabled` is `true` by default | Create the Secret and set `tls.secretName` |
| Pods stay unready; `/readyz?verbose=1` shows the `tls` check failing | The listener Secret is missing, uses other key names, or holds a key that does not match the certificate, or a certificate that is expired or not valid yet | Check the Secret's `tls.crt` and `tls.key` and the error log line that names the file |
| The ingress answers with a gateway error | The controller talks plain HTTP to trove (wrong `ingress.backendTLS.controller`), or cannot verify trove's certificate | Set the controller you run; make sure the certificate holds `<fullname>.<namespace>.svc` and `caSecret` holds its CA |
| A client gets `400` "Client sent an HTTP request to an HTTPS server" | Plain HTTP sent to a TLS listener | Use `https://` |
| A renewed certificate is not served | The file did not load, or the mount uses `subPath` (a hand-written manifest) | Look for `trove_tls_reload_errors_total` and the error log; mount the Secret as a directory |
| Clients refuse an expired certificate | The renewal stopped producing new content; trove keeps serving the loaded pair | Fix the issuer; alert on the expiry metric |
| The process ends at start naming a database, Valkey or object-store file | A configured CA or client certificate file is missing or unreadable | Check the Secret named in `secrets.*` |
| The configuration is refused: `verify-ca` without a root file, or plaintext without the insecure switch | The validation rules above | Add the CA file, or set the explicit switch if plaintext is intended |
| Connections to PostgreSQL or Valkey fail with a certificate error | `verify-full` against the system roots for a server with a private CA, or a host name the certificate does not hold | Set `secrets.databaseCA` or `secrets.valkeyCA`; connect by a name in the certificate |
| The object store fails after `secrets.s3CA` was set | That file replaces every other trusted root for the object store | Put the full CA chain of the object store into it |
| The render fails with `tls.clientCA` and HAProxy Ingress | The controller needs a certificate to present | Set `ingress.backendTLS.clientCertSecret` |
