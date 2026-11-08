# Privilege footprint

Status: built for the PostgreSQL grants of the runtime role (since M0) and of the audit objects (M1), the pod security context,
the service account and the network ingress of every Deployment and Job of the chart. The Valkey user and the object-store permissions are configured outside trove; this page says what
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

Not checked by the preflight: the grants of the audit objects and of the backup role that R-HA-014 lists. The audit tables
exist, and migration 6 revokes `UPDATE`, `DELETE` and `TRUNCATE` on `audit_event` and every write on `audit_policy` from
`trove_app` ([00006_audit.sql](../../internal/meta/postgres/migrations/00006_audit.sql)), but no start checks that those
grants still hold ([H-75](#h-75--the-audit-object-grants-are-not-checked-at-start)). A backup role without its grants is found
by the backup run, which fails with `backup_grant_missing` ([backup_create.go](../../cmd/trove/backup_create.go), `mapOpenError`).

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
stack uses creates no ACL users and no separate Sentinel password yet. Until it does, the local profile runs trove as the
operator's `default` user with the cluster password, and the same Secret authenticates against the Sentinels: its values set
`secrets.valkey.usernameKey: ""` ([values-dev.yaml](../../deploy/dev/values-dev.yaml); the owner's decision is recorded in
[ha-and-helm.md](../spec/ha-and-helm.md), Q-HA-15). That is [H-72](#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist).

## Object store

Trove calls these S3 operations and no others ([internal/blob/s3](../../internal/blob/s3)):

| Operations | Who calls them | Code |
|---|---|---|
| `PutObject`, `CreateMultipartUpload`, `UploadPart`, `CompleteMultipartUpload`, `AbortMultipartUpload`, `HeadObject`, `GetObject`, `DeleteObject`, `DeleteObjects`, `ListObjectsV2` | the blob store of the replicas, the journal shipper, the backup's reads of the blob bucket, the backup target client | [client.go](../../internal/blob/s3/client.go) |
| `CopyObject`, `UploadPartCopy` | the server-side copy of a blob into the target at a backup, and into the blob store at a restore (R-BAK-006) | [target_copy.go](../../internal/blob/s3/target_copy.go), [blobsink.go](../../internal/blob/s3/blobsink.go) |
| `ListObjectVersions`, and `GetObject` with a `VersionId` | the journal reader of a restore and of the post-restore, with the credential of `restore.journalCredentials` or `postRestore.journalCredentials` | [journalstore.go](../../internal/blob/s3/journalstore.go) |
| `GetBucketVersioning`, `GetBucketLifecycleConfiguration` | the platform check of the blob bucket's protection (R-HA-023); a credential that may not read them gives "unknown", the common answer for the runtime credential | [protection.go](../../internal/blob/s3/protection.go) |

The client also implements `ListMultipartUploads` and a presigned `GetObject`; only the object-store conformance check of
R-STOR-035 ([conformance.go](../../internal/blob/conformance.go)) calls them, and it runs in the integration tier, not in a
replica or a Job. Trove never calls `CreateBucket`, any `PutBucket*`, `DeleteObjectVersion`, or any call that configures
versioning, lifecycle or object lock ([internal/blob](../../internal/blob) has no such call). The bucket is an operator resource.

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

The chart renders one ingress `NetworkPolicy` per Deployment, named as the Deployment, and one per Job, when `networkPolicy.enabled`
is `true` (the default) ([networkpolicy.yaml](../../deploy/helm/trove/templates/networkpolicy.yaml); the Deployments are those of
[_units.tpl](../../deploy/helm/trove/templates/_units.tpl), `trove.units`). Every rule names its port; no rule admits one trove pod from
another, because replicas never talk to each other:

| Pods | Ingress admitted | Egress |
|---|---|---|
| `api`, `packages`, their path pools, and the bridge's `web` | `:8080` from `networkPolicy.ingress.http.from` (empty: from anywhere); the port of each further endpoint the pods open, from that endpoint's `networkPolicy.from` (empty: from anywhere); `:8081` from `networkPolicy.ingress.ops.from` (default: the `monitoring` namespace) and `networkPolicy.ingress.probeCIDRs` | not rendered |
| the endpoint pools of further endpoints | the endpoint's port, from its `networkPolicy.from`; `:8081` from the ops sources | not rendered |
| `worker` and its pools | `:8081` only, from the ops sources | not rendered |
| migration Job, backup CronJob's pods, restore Job, post-restore Job | nothing (`ingress: []`) | not rendered |

With no ops source at all the `:8081` rule is left out rather than rendered with an empty `from`, which would admit every peer.

No egress policy is rendered, so a compromised replica can reach any address the pod network reaches
([H-516](egress.md#h-516--the-chart-restricts-no-egress-of-its-pods)).
The ingress policy is enforced on the kind profile's CNI, which is verified in the end-to-end suite; other CNIs are not verified.

Each further endpoint adds a listener on its own port and a Service, and its pods run with the credentials of their role
([H-57](../spec/endpoints.md#h-57--a-further-endpoint-runs-with-every-credential-of-a-web-replica)).

## What this does not cover

The gaps of the spec pages that bound or widen this footprint:

- [H-49](../spec/architecture.md#h-49--write-access-to-valkey-denies-service): write access to Valkey denies service.
- [H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy): the object-store endpoint skips the egress address policy.
- [H-517](../spec/architecture.md#h-517--the-cut-into-roles-separates-the-login-secrets-not-the-data-credentials): every role connects with the same database login and the same object-store and Valkey credentials.
- [H-57](../spec/endpoints.md#h-57--a-further-endpoint-runs-with-every-credential-of-a-web-replica): a further endpoint runs with every credential of its role.
- [H-41](../spec/operations-and-observability.md#h-41--ingress-enforcement-is-verified-on-one-cni-only): the enforcement of the ingress NetworkPolicy is verified on one CNI only.
- [H-516](egress.md#h-516--the-chart-restricts-no-egress-of-its-pods): the chart restricts no egress of its pods.

### H-71 · Single-role mode gives the runtime login DDL rights

Stage: the database's schema, changed by any SQL the runtime login sends ([preflight.go](../../internal/meta/postgres/preflight.go)).

Live with the chart default: `secrets.databaseMigration.existingSecret` is empty, so the runtime login owns the schema and holds
DDL rights. A replica only warns at start. Subject: the schema `trove` and everything in it, including the bookkeeping
tables, which in this mode are writable by the same login.

What makes it real: a SQL injection in any path that builds SQL from input (none is known; the code uses parameters and
`pgx.Identifier`), or a leaked runtime password. The two-role mode removes the DDL right; the chart does not enforce it.

### H-72 · The default Valkey user holds every right until ACL users exist

Stage: every connection from a replica to a data node and to a Sentinel ([cluster/valkey_auth.go](../../internal/cluster/valkey_auth.go)).

Live wherever `valkey.username_file` is empty or the chart's `secrets.valkey.usernameKey` is empty, and so in the local profile,
whose values set `usernameKey: ""` ([values-dev.yaml](../../deploy/dev/values-dev.yaml)). The owner's valkey operator cannot yet
create ACL users (its own documentation names this gap, checked 2026-10-29), so an installation on that operator runs trove as the
`default` user; the owner decided on 2026-10-29 that the local profile follows that path as an interim. The `default` user is given `~*`, `&*` and `+@all` by the operator. Subject: every key of the
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

Live on every two-role installation: the audit tables exist since migration 6, which revokes the writes the design forbids
([00006_audit.sql](../../internal/meta/postgres/migrations/00006_audit.sql)), and the preflight checks none of their grants
(`preflight` checks the server, the superuser, the bookkeeping tables and the role defaults;
[R-HA-014](../spec/ha-and-helm.md#r-ha-014--database-provisioning-roles-and-privileges) lists the audit objects as well). A grant
that is missing or surplus on `audit_event` or `audit_policy` stays until someone reads the grants by hand. In single-role mode the
runtime login owns the tables, and the revocation does not apply ([H-71](#h-71--single-role-mode-gives-the-runtime-login-ddl-rights)).
Subject: the append-only property of the audit trail against a runtime role.

What makes it real: the audit tables created without the check, or a later `GRANT` that re-opens a write the design revokes.
