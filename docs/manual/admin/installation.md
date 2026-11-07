# Installation

This page takes you from an empty Kubernetes namespace to a running trove installation: what must exist beforehand, where the chart and the image come from, which Secrets to create, a minimal values file, the first sign-in with the break-glass administrator, the command-line bootstrap and recovery commands, and how to check that the installation is healthy.

## Prerequisites

trove installs with its Helm chart and consumes the services below; the chart deploys none of them.

| Need | Requirement |
|---|---|
| Kubernetes | 1.30 or later. The security settings of the pods meet the restricted Pod Security Standard. |
| Helm | The chart is tested with Helm 4. |
| PostgreSQL | Version 16 or later, `UTF8`, a database and a login that is not a superuser. A [CloudNativePG](https://cloudnative-pg.io/documentation/) cluster provides what the chart reads: the read-write Service, the application Secret with `username` and `password`, and a Secret with the server's `ca.crt`. |
| Valkey | Version 9.0 or later with [Sentinel](https://valkey.io/topics/sentinel/), over TLS. With the default `highAvailability.enforce: true` the chart requires at least three Sentinel addresses. Standalone Valkey is for development only and is refused under `enforce`. |
| Object storage | An S3-compatible bucket and credentials for it. SeaweedFS, MinIO and Ceph RGW need path-style addressing (`force_path_style: true`). |
| Certificates | A `kubernetes.io/tls` Secret for trove's own listeners with `tls.crt`, `tls.key` and `ca.crt`, for example from a [cert-manager](https://cert-manager.io/docs/) `Certificate`. It must name the client Service, `<fullname>.<namespace>.svc`, because the ingress controller verifies that name by default. |
| Ingress controller | HAProxy Ingress is the default and tested target. The chart also renders the backend-TLS settings for Traefik and ingress-nginx, which are tested for their rendering only. With `controller: manual` you set the backend protocol yourself. |
| OpenID Connect provider | A client for trove using the authorization-code flow, with the redirect URI `<base URL>/auth/oidc/<provider ID>/callback`. |

trove's pods need no persistent volume: the spool and `/tmp` are ephemeral. The default resources are 2 `api` pods with 0.5 CPU and 512 MiB requested and a 1 GiB memory limit, 3 `packages` pods with 1 CPU and 1 GiB requested and a 2 GiB memory limit, and 2 `worker` pods with 0.5 CPU and 512 MiB requested and a 1 GiB limit.

## Get the chart and the image

The chart is published in its own Helm repository, and the image `docker.io/guidedtraffic/trove` on Docker Hub for `linux/amd64` and `linux/arm64`. The chart's version equals trove's version, and the chart uses the image of its own version by default.

```shell
helm repo add trove https://guided-traffic.github.io/trove/
helm repo update trove
helm search repo trove/trove --versions        # lists the published versions
```

A release `v0.5.0` tags the image `0.5.0`, `0.5` and `0`. To pin the exact image, set `image.digest`; a digest wins over the tag.

## Create the namespace and the Secrets

The chart never creates a Secret. Every Secret it names must exist in the release namespace before `helm install`, or the pods cannot start. The examples use the namespace `trove` and the release name `trove`, so the objects are called `trove-api`, `trove-packages`, `trove-worker` and so on.

```shell
kubectl create namespace trove
```

The encryption key protects the secrets trove stores in PostgreSQL, such as the upstream passwords of remote repositories. The file name without `.key` is the key ID. Keep a copy of the key outside the cluster: without it, those stored values cannot be decrypted, and the chart never generates a replacement.

```shell
kubectl -n trove create secret generic trove-encryption-keys \
  --from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)                 # example key ID 2026-10
```

The break-glass Secret holds the password of the one local administrator account: a plaintext password of at least 20 characters, or an Argon2id hash in PHC string format with at least `m=65536,t=3,p=4`, which keeps the plaintext out of the cluster.

```shell
kubectl -n trove create secret generic trove-break-glass \
  --from-literal=password="$(head -c 24 /dev/urandom | base64)"
kubectl -n trove create secret generic trove-oidc-corp \
  --from-file=client-secret=./client-secret.txt                                # example: the secret your provider issued
```

Whoever can read the break-glass Secret is an administrator of trove. Restrict access to it with Kubernetes RBAC as you would for the database credentials.

<details>
<summary>Every Secret the chart reads</summary>

| Value | Keys (defaults) | Needed | Mounted into |
|---|---|---|---|
| `secrets.database` | `username`, `password` | always | every pod and the migration Job |
| `secrets.databaseMigration` | `username`, `password` | optional: a separate schema owner for the migration Job; empty means the runtime login owns the schema | the migration Job |
| `secrets.databaseCA` | `ca.crt` | when PostgreSQL's certificate is not signed by a public CA | every pod and the Job |
| `secrets.databaseClientCert` | `tls.crt`, `tls.key` | optional client certificate | every pod and the Job |
| `secrets.valkey` | `username`, `password` (`usernameKey: ""` for Valkey's default user) | always | `api`, `packages`, `worker` |
| `secrets.valkeySentinel` | `password` (and a user if you name `usernameKey`) | optional; without it trove warns that the Sentinels are unauthenticated | `api`, `packages`, `worker` |
| `secrets.valkeyCA` | `ca.crt` | when Valkey's certificate is not signed by a public CA | `api`, `packages`, `worker` |
| `secrets.valkeyClientCert` | `tls.crt`, `tls.key` | when Valkey requires client certificates | `api`, `packages`, `worker` |
| `secrets.s3` | `access-key-id`, `secret-access-key`; optionally the keys named by `bucketKey`, `regionKey`, `endpointKey` | with `config.storage.s3.credentials.source: static` | `api`, `packages`, `worker` |
| `secrets.s3CA` | `ca.crt` | when the object store's certificate is not signed by a public CA | `api`, `packages`, `worker` |
| `secrets.encryption` | the files listed in `secrets.encryption.keys`, first one active | always | `api`, `packages`, `worker`, never the Job |
| `secrets.breakGlass` | `password` | optional; without it the break-glass account is switched off | `api` pods only, `api` pools included; never `packages` |
| `secrets.oidc[]` | `client-secret`, one entry per provider | one per OIDC provider that uses a client secret | `api` pods only, `api` pools included; never `packages` |
| `secrets.metricsToken` | `token` | with `config.metrics.auth: static` | `api`, `packages`, `worker` |
| `tls.secretName` | `tls.crt`, `tls.key` | with `tls.enabled` (the default) | the pods that serve with it |
| `tls.clientCA.secretName`, `tls.ops.secretName` | `ca.crt`; `tls.crt`, `tls.key` | optional: mutual TLS from the edge; a separate certificate for the ops listener | `api` and `packages`; every pod |
| `ingress.backendTLS.caSecret` | `ca.crt` | with HAProxy Ingress or ingress-nginx and `verify: true` | read by the ingress controller |

Secrets are mounted as read-only files, never as environment variables, so a rotated Secret reaches the running pods. The settings of an S3 provisioner's Secret (bucket, region, endpoint) are the one exception: they are passed as environment variables because they are not credentials.

</details>

## A minimal values file

Save this as `values.yaml` and replace every value marked `# example`. The chart refuses unknown keys and fails with a message that names a missing required value.

```yaml
config:
  server:
    base_url: https://trove.example.com          # example: the HTTPS origin users and clients reach
  database:
    host: trove-db-rw.trove.svc                  # example: the CloudNativePG read-write Service
    name: trove                                  # example
  valkey:
    sentinel:
      addresses:                                 # example: at least three Sentinels
        - trove-valkey-sentinel-0.trove-valkey-sentinel.trove.svc:26379   # example
        - trove-valkey-sentinel-1.trove-valkey-sentinel.trove.svc:26379   # example
        - trove-valkey-sentinel-2.trove-valkey-sentinel.trove.svc:26379   # example
      primary_name: trove                        # example: the name in the Sentinels' monitor line
  storage:
    s3:
      bucket: trove-blobs                        # example
      region: eu-central-1                       # example
      endpoint: https://s3.example.com           # example; empty uses AWS endpoint resolution
      force_path_style: false                    # default; true for SeaweedFS, MinIO and Ceph RGW
      credentials:
        source: static                           # example: the key files of secrets.s3
  auth:
    oidc:
      providers:
        corp:                                    # example: the provider ID
          display_name: Corporate sign-in        # example
          issuer: https://id.example.com         # example
          client_id: trove                       # example
tls:
  secretName: trove-tls                          # example: tls.crt, tls.key, ca.crt for <fullname>.<namespace>.svc
ingress:
  enabled: true                                  # example; default false
  className: haproxy                             # example
  tls:
    - secretName: trove-public-tls               # example: the certificate of trove.example.com
      hosts: [trove.example.com]                 # example
  backendTLS:
    controller: haproxy                          # default
    caSecret: trove-tls                          # example: a Secret with the ca.crt that signed tls.secretName
secrets:
  database:
    existingSecret: trove-db-app                 # example: the CloudNativePG application Secret
  databaseCA:
    existingSecret: trove-db-ca                  # example: the CloudNativePG CA Secret
  valkey:
    existingSecret: trove-valkey                 # example
  valkeyCA:
    existingSecret: trove-valkey-ca              # example
  s3:
    existingSecret: trove-s3                     # example
  encryption:
    existingSecret: trove-encryption-keys        # example
    keys: [2026-10.key]                          # example; ordered, the first is the active key
  breakGlass:
    existingSecret: trove-break-glass            # example
  oidc:
    - providerId: corp                           # example: a key of config.auth.oidc.providers
      existingSecret: trove-oidc-corp            # example
```

The `config` subtree becomes trove's configuration file; [Configuration](configuration.md) lists its keys. Do not set the paths of mounted Secrets, `storage.spool.dir`, `server.tls` or `server.endpoints` inside `config`: the chart manages them and fails the render when you do. To make the client address of each request visible to trove (for rate limits and the audit log), list the addresses of your ingress controller pods in `config.server.trusted_proxies`; never list a whole pod network, because every pod in it could then claim any client address.

trove reaches your OIDC provider over HTTPS and trusts the system roots. If the provider's certificate comes from a private CA, the chart has no value for it yet: mount the CA file with `extraVolumes` and `extraVolumeMounts` and list its path in `config.proxy.outbound.tls.ca_files`.

## Install

```shell
helm install trove trove/trove -n trove -f values.yaml --version <version>   # example: a version from helm search
kubectl -n trove get jobs,pods                                               # the migration Job runs first
kubectl -n trove rollout status deployment/trove-api
kubectl -n trove rollout status deployment/trove-packages
kubectl -n trove rollout status deployment/trove-worker
```

`helm install` first runs the Job `trove-migrate`, which creates the schema, and creates the Deployments only after it has succeeded. If the Job fails, read its log with `kubectl -n trove logs job/trove-migrate`; the Job is kept for a day after it finished. The notes Helm prints after the install list the endpoints and warn about risky endpoint settings.

## First sign-in

1. Read the break-glass password: `kubectl -n trove get secret trove-break-glass -o jsonpath='{.data.password}' | base64 -d`.
2. Open `https://trove.example.com/ui/login?auto_sign_in=false`. The parameter keeps the sign-in page from redirecting to your provider and shows the username and password form.
3. Sign in with the username `admin` (`auth.break_glass.username`) and the password, then select **Sign in**.
4. Open **Admin area › Access › System roles** and use **Bind** to give the administrator role to your own user or to a group from your identity provider. [Permissions](permissions.md) describes the roles. To keep this in your GitOps repository instead, declare the group in `config.authz.role_bindings` ([Administrators from the Helm values](sign-in-and-identity.md#administrators-from-the-helm-values)); the binding then exists after the install.
5. Sign out and sign in again through your provider. [Sign-in and identities](sign-in-and-identity.md) covers provider options and group mapping.

The break-glass account has no role binding; it holds system administration because it is the local administrator. With the default settings it locks for 15 minutes after 5 failed attempts within 15 minutes, its sessions end after 15 minutes idle or one hour in total, and it cannot own tokens. When a rotated Secret reaches the pods, each `api` replica notices the new file within 10 seconds (`auth.break_glass.check_interval`), refuses the local sign-in for 2 minutes (`auth.break_glass.rotation_hold`) and ends the break-glass sessions. To switch the account off, remove `secrets.breakGlass` from your values; the chart then renders `auth.break_glass.enabled: false`.

## Bootstrap and recovery commands

The `trove admin` commands work directly on the database with the pod's own configuration, so they also work while no administrator can sign in. They need no Valkey. Run them in a running pod; the binary is `/usr/local/bin/trove`:

```shell
kubectl -n trove exec deploy/trove-api -- /usr/local/bin/trove admin break-glass status
```

| Command | What it does |
|---|---|
| `trove admin config validate [--format table\|json]` | Validates the configuration file and environment and prints every key with its value and source; secrets are redacted |
| `trove admin break-glass status` | Shows whether the account is enabled and whether it is locked |
| `trove admin break-glass unlock` | Lifts the lockout after failed sign-ins |
| `trove admin break-glass end-sessions` | Ends every break-glass session |
| `trove admin sessions end --user <username>` or `--all` | Ends the browser sessions of one user or of everyone |
| `trove admin service-account create <name> [--description <text>] [--admin]` | Creates a service account; `--admin` binds the administrator role to it |
| `trove admin token create --service-account <name\|project/name> [--ttl 15m] [--scope-file <file>]` | Prints a new token of the service account (`<project>/<name>` for the account of a project) once, and nothing else, so a script can capture it. The lifetime defaults to 15 minutes and may not exceed the service-account token maximum. |
| `trove admin token revoke --id <uuid>` or `--value-stdin` | Revokes a token by its ID, or by its value read from standard input |
| `trove admin user disable <username> [--keep-tokens]` | Disables a user and ends the user's sessions; with `--keep-tokens` the tokens remain but fail while the user is disabled |

Every command that changes something writes an audit event with the actor `admin-cli`, and the change is in force on every replica within `auth.revocation_bound`. For automation without a browser, create a service account with `--admin` and a short-lived token, then use the management API with that token.

## Verify the installation

1. The release and the pods: `helm status trove -n trove` shows `deployed`, and every `trove-api`, `trove-packages` and `trove-worker` pod is `Running` and ready.
2. The edge answers: `curl -s -o /dev/null -w '%{http_code}\n' https://trove.example.com/api/v1/system/ping` prints `200`. This request needs no credential.
3. The readiness of one replica, through a port forward to its ops listener:

   ```shell
   kubectl -n trove port-forward deploy/trove-api 8081:8081
   curl -sk 'https://localhost:8081/readyz?verbose=1'      # -k: the certificate does not name localhost
   ```

   A ready replica answers `200` with `"status":"ready"` and lists each check. `degraded` names checks that fail without making the replica unready, `valkey` for example; `failing` names the checks that keep it unready.
4. In the UI, **Admin area › System › System status** shows **All ready** when every replica is ready and none is degraded, and lists the **Installation** version, the **Replicas by role**, the **Database** schema version, the **Valkey** connection and the **Stored-secret keys** each replica holds.
5. The effective configuration of a pod: `kubectl -n trove exec deploy/trove-api -- /usr/local/bin/trove admin config validate`.

[Monitoring and operations](operations.md) describes the metrics and the day-to-day checks.

## Remove an installation

`helm uninstall trove -n trove` removes the Deployments, Services, ConfigMaps and the other objects of the chart. It leaves the database, Valkey, the bucket and every Secret untouched; delete those yourself if you no longer need the data.
