# Privilege footprint

Status: implemented in M0 for the PostgreSQL grants of the runtime role, the pod security context, the service account
and the network ingress. The Valkey user and the object-store permissions are configured outside trove; this page says what
trove asks for and what the operator has to provide.

This page lists what trove itself is allowed to do on each system it depends on, and what follows when a process is
compromised. It describes the chart and the code as they are.

## PostgreSQL

Two credential modes exist (R-HA-014, [ha-and-helm.md](../spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges)):

| Mode | Chart | Who owns the schema | What a request-serving login may do |
|---|---|---|---|
| Two-role | `secrets.databaseMigration.existingSecret` set | the migration role (`trove_owner`) | DML on the tables of the schema, through the default privileges of the first migration ([00001_foundation.sql](../../internal/meta/postgres/migrations/00001_foundation.sql)) |
| Single-role | `secrets.databaseMigration.existingSecret` empty (the chart default) | the runtime login itself | everything the owner may do: DDL included |

In single-role mode a replica logs a warning at start: a SQL injection in the application would have DDL rights
([preflight.go](../../internal/meta/postgres/preflight.go), `preflight`). This is the default of the chart. The gap is
[H-71](#h-71--single-role-mode-gives-the-runtime-login-ddl-rights).

Two-role mode adds one revocation. The bookkeeping tables `schema_migration` and `schema_state` are written only by the
migration role; migration 5 revokes `INSERT`, `UPDATE`, `DELETE` and `TRUNCATE` on them from `trove_app`
([00005_bookkeeping_privileges.sql](../../internal/meta/postgres/migrations/00005_bookkeeping_privileges.sql)). `system_state` stays
writable for the runtime, because the runtime sets the deletion pause and later keys of its own.

At start, every replica runs the preflight ([preflight.go](../../internal/meta/postgres/preflight.go)):

- the server version (16 or later), and `UTF8` encoding;
- a superuser runtime role is refused unless `database.preflight.allow_superuser: true`;
- after migration 5 has applied, a runtime login that can still write `schema_migration` or `schema_state` is refused, with the
  `REVOKE` that would fix it named in the error (`checkBookkeeping`). A superuser or the owner of the tables is skipped;
- behind a transaction-mode pooler (`database.pooler.mode: transaction`), the session settings that cannot travel as startup
  parameters must come from the role's defaults (`checkRoleDefaults`).

Not implemented in M0: the check of the audit objects and of the backup role that R-HA-014 lists, because the audit tables
do not exist yet ([H-75](#h-75--the-audit-object-grants-are-not-checked-at-start)).

The migration Job ([migrate.yaml](../../deploy/helm/trove/templates/migrate.yaml)) runs `trove migrate up` with the migration
credentials when they are set, with no key ring, no object-store or Valkey Secret and no service-account token. It serves no
listener. Its exit status `3` (a refused contract guard or a half-restored database) fails the Job without a retry.

Advisory locks need no grant beyond the connection's own rights. The lock keys are described on
[isolation-and-coordination.md](isolation-and-coordination.md).

## Valkey

The client reads the user name from `valkey.username_file` and the password from `valkey.password_file`. An empty
`username_file` means the `default` user ([config/valkey.go](../../internal/config/valkey.go); the empty name in
[valkey_auth.go](../../internal/cluster/valkey_auth.go), `readSecretFile`). The Sentinel pair is separate
(`valkey.sentinel.username_file`, `password_file`).

What trove asks Valkey to allow for its own user is in [R-ARCH-031](../spec/architecture.md#r-arch-031--valkey-client-sentinel-discovery-tls-acl-and-degradation):
a key prefix, the command set the client uses, and no scripting and no administration. The valkey operator that the owner's
stack uses creates no ACL users and no separate Sentinel password yet. Until it does, the local profile of 2026-10-29 runs
trove as the operator's `default` user (the owner's decision is recorded in [ha-and-helm.md](../spec/ha-and-helm.md), Q-HA-15).
The local profile does not run that way yet: [local-profile.md](../developer/local-profile.md) still describes an ACL user
`trove`, and the switch to the default user is pending. That is [H-72](#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist).

## Object store

Trove calls these S3 operations and no others ([blob/s3/client.go](../../internal/blob/s3/client.go)): `PutObject`,
`CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `HeadObject`, `GetObject`,
`DeleteObject`, `DeleteObjects` and `ListObjectsV2`. Trove never calls `CreateBucket`, any `PutBucket*`, `DeleteObjectVersion`,
or any call that configures versioning, lifecycle or object lock ([internal/blob](../../internal/blob) has no such call). The
bucket is an operator resource.

The minimum permissions per prefix are R-STOR-031 ([storage.md](../spec/storage.md)). The stackit S3 provisioner that the owner
uses grants a workload policy that denies `PutBucketVersioning`, `PutLifecycleConfiguration`,
`PutBucketObjectLockConfiguration` and `PutBucketPolicy`, and its action list has no `DeleteObjectVersion`. This was read in
the provisioner's documentation on 2026-10-29, not in this repository. Consequences for the design: a versioned blob bucket
and an incomplete-multipart lifecycle rule cannot be set by trove, and the backup target cannot use Object Lock.

The credential source is one of `default`, `webIdentity` and `static` (`storage.s3.credentials.source`). With `static`,
trove reads the key files and nothing else. With `default` the AWS SDK's default chain is used, which is
[H-74](#h-74--the-default-object-store-credential-chain-can-reach-node-metadata).

## Pod and service account

Every pod of the chart runs with the defaults of [values.yaml](../../deploy/helm/trove/values.yaml):

| Setting | Default | Where |
|---|---|---|
| `runAsNonRoot`, `runAsUser`, `runAsGroup`, `fsGroup` | `true`, `65532` | `podSecurityContext` |
| seccomp | `RuntimeDefault` | `podSecurityContext`, `securityContext` |
| `allowPrivilegeEscalation`, `privileged` | `false`, `false` | `securityContext` |
| `readOnlyRootFilesystem` | `true`; `/tmp` is an `emptyDir` of `tmp.sizeLimit` | `securityContext`, `tmp` |
| capabilities | `drop: [ALL]` | `securityContext` |
| service account token | not mounted (`automountServiceAccountToken: false`, both in the service account and the pod) | `serviceAccount`, [migrate.yaml](../../deploy/helm/trove/templates/migrate.yaml) |

The chart renders no `Role`, `RoleBinding` or `ClusterRole`. The service account has no RBAC, and trove never calls the
Kubernetes API. The chart cannot set Pod Security Admission labels on a namespace; the local profile sets them in its scripts
([local-profile.md](../developer/local-profile.md)).

## Network

The chart renders one `NetworkPolicy` per role when `networkPolicy.enabled` is `true` (the default)
([networkpolicy.yaml](../../deploy/helm/trove/templates/networkpolicy.yaml)):

| Pod | Ingress admitted | Egress |
|---|---|---|
| `web` | `:8080` from `networkPolicy.ingress.http.from` (empty: from anywhere); `:8081` from `networkPolicy.ingress.ops.from` (default: the `monitoring` namespace) and `networkPolicy.probeCIDRs` | not rendered |
| `worker` | `:8081` only, from the same ops sources | not rendered |
| migration Job | nothing | not rendered |

No egress policy is rendered, so a compromised replica can reach any address the pod network reaches
([H-41](../spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only)).
The ingress policy is enforced on the kind profile's CNI, which is verified in the end-to-end suite; other CNIs are not verified.

Each further endpoint adds a listener on its own port and a Service, and its pods run with the credentials of a `web` pod
([H-57](../spec/endpoints.md#h-57--a-further-endpoint-runs-with-every-credential-of-a-web-replica)).

## What this does not cover

### H-71 · Single-role mode gives the runtime login DDL rights

Stage: the database's schema, changed by any SQL the runtime login sends ([preflight.go](../../internal/meta/postgres/preflight.go)).

Live with the chart default: `secrets.databaseMigration.existingSecret` is empty, so the runtime login owns the schema and holds
DDL rights. A replica only warns at start. Subject: the schema `trove` and everything in it, including the bookkeeping
tables, which in this mode are writable by the same login.

What makes it real: a SQL injection in any path that builds SQL from input (none is known; the code uses parameters and
`pgx.Identifier`), or a leaked runtime password. The two-role mode removes the DDL right; the chart does not enforce it.

### H-72 · The default Valkey user holds every right until ACL users exist

Stage: every connection from a replica to a data node and to a Sentinel ([cluster/valkey_auth.go](../../internal/cluster/valkey_auth.go)).

Live wherever `valkey.username_file` is empty or the chart's `secrets.valkey.usernameKey` is empty. The owner's valkey operator
cannot yet create ACL users (its own documentation names this gap, checked 2026-10-29), so an installation on that operator
runs trove as the `default` user. The owner decided on 2026-10-29 that the local profile follows that path as an interim; the
profile is not switched yet. The `default` user is given `~*`, `&*` and `+@all` by the operator. Subject: every key of the
Valkey instance, including the keys of other installations and of other applications on the same instance, and the commands
that flush or script.

What makes it real: a leaked Valkey password, or a replica compromised by the code path that writes to Valkey. Its effect is
still bounded by what Valkey may hold (see [data-and-secret-flow.md](data-and-secret-flow.md)): a Valkey write denies service
and never grants access, which is [H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service).

### H-73 · Installations that share one Valkey share its user

Stage: the Valkey instance, when two installations of trove connect to it with the same user.

Dormant: live only when two installations use one Valkey instance and one user. Trove's keyspace is separated per installation
by a key prefix and the keyspace ID ([README.md](../../README.md#valkey), `Keyspace`), but an ACL user with `~*` reads all of
them. The separation is a naming convention, not an access control.

What makes it real: a second installation on a shared instance, configured without its own ACL user. The mitigation is one
Valkey instance per installation, or one ACL user per installation once the operator provides them.

### H-74 · The default object-store credential chain can reach node metadata

Stage: the credentials of the object-store client when `storage.s3.credentials.source` is `default`
([blob/s3/credentials.go](../../internal/blob/s3/credentials.go), `awsConfig`).

Live on a cloud without workload identity, with the default source and no environment or shared-config credentials: the AWS
SDK's default chain then reaches the instance metadata service of the node. The object-store client applies no egress address
policy ([H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy)), so the metadata
address is not refused by trove. Whether the metadata request itself goes through that client was not verified in this run.
Subject: the node's identity, which may carry rights beyond the bucket.

What makes it real: a node role with rights beyond the bucket, and no workload identity bound to the pod. Setting
`credentials.source: static` or `webIdentity` removes the chain from the path.

### H-75 · The audit-object grants are not checked at start

Stage: the start-up preflight of a two-role installation ([preflight.go](../../internal/meta/postgres/preflight.go)).

Dormant: no audit table exists in M0, so there is nothing to check. It becomes live with the audit tables of the spec
([R-HA-014](../spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges), audit objects and the runtime role). Once
they exist, a grant that is missing or surplus on `audit_event` or `audit_policy` would stay until someone reads the grants by
hand. Subject: the append-only property of the audit trail against a runtime role.

What makes it real: the audit tables created without the check, or a later `GRANT` that re-opens a write the design revokes.
