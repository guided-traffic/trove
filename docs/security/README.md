# Security architecture

Status: the mechanisms of M0 are implemented and described here: the listeners and their TLS, the client-address and
endpoint rules, the stored-secret key ring, the egress address policy, the PostgreSQL and Valkey connections and grants, the
coordination of replicas, and the chart's security context and network ingress. Identity, repositories, package formats
and backup do not exist yet, so they have no page.

This directory describes trove's security architecture, with one page per perspective. It is deliberately not one document that
covers everything, because nobody finishes such a document and so nobody checks it. The file names are the index.

How to report a vulnerability is in [SECURITY.md](../../SECURITY.md). This directory covers what is already known.

## Form

- **One perspective per page.** Each page explains one view of the system: who is trusted with what, where credentials go, what
  the deployment is allowed to do. It references the code that implements the view and says only what the code does.
- **Every page ends with `## What this does not cover`.** That section lists the open gaps of the page's mechanisms.
- **Open gaps carry a stable identifier.** A gap is a `### H-<n> · <title>` heading. It lives in the `What this does not cover`
  section of the page whose mechanism has the gap, and nowhere else. It is never collected in a central list. The number stays
  the same for the gap's whole life, so reviews, commits and issues can refer to it.
- **Each gap states its case.** It names the component and the subject it affects, says whether it is live today or dormant (and
  what would make it live), and says what makes it real.
- **No tickets, no checkbox lists, and no promises of future work** on these pages. They describe the system as it is.
- Security notes appear only where a real security relation exists.

## Perspectives

| Page | Perspective | Mechanisms it describes (code) |
|---|---|---|
| [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md) | Roles, the boundaries between them, and what each boundary trusts a party to do | the roles `web`, `worker`, migration; the client-address rule and `server.forwarded_header`; endpoint identification and the `421`; trace trust; the ops listener; the bootstrap read of installation facts |
| [transport-encryption.md](transport-encryption.md) | Which connections are encrypted, how they verify, where their material comes from and how a rotation reaches them | the listener TLS and its reloader; PostgreSQL, Valkey, Sentinel, object-store and outbound verification; the chart's TLS values and mounts |
| [data-and-secret-flow.md](data-and-secret-flow.md) | Where each credential lives, when a process reads it, what it holds in memory, what Valkey may hold | the file-only credentials and their re-read rules; the key ring and its file form; the sealed envelope and its additional data; redaction |
| [privilege-footprint.md](privilege-footprint.md) | What trove itself may do on PostgreSQL, Valkey, the object store, the pod and the network | the two-role and single-role grants and the preflight; the bookkeeping revoke; the Valkey user; the S3 operations trove issues; the pod security context; the ingress NetworkPolicy |
| [isolation-and-coordination.md](isolation-and-coordination.md) | Which replica may hold singleton state, how a stale writer is fenced, who can change what replicas read, and where installations can collide | the role sets; session advisory leases; the fence on `pg_locks`; the change log and its Valkey evictions; the replica registry; the handling of a lost COMMIT |
| [audit.md](audit.md) | Audit trail: who can write, read and change events; retention; the journal row; where a lost event survives | the event registry and journal flag; the transactional and asynchronous writers; the runtime role's insert-only grants; retention and partitions; the log copy |
| [egress.md](egress.md) | Which outbound connections are checked against which address rules, and where the check does not reach | the address policy at connect time; zoned IPv6 refusal; the deny and allow lists; NAT64; the objectstore and span-export exceptions |

Mechanisms of the spec that are not built yet have no page. Their rules stay in the spec pages until the code exists:
input validation of package content, the rotation and propagation of identity and upstream changes, the GitLab CI federation and the
backup data flow ([docs/spec/](../spec/README.md)).

Every page links its gaps; the spec pages keep the gaps whose mechanism they describe, and the perspective pages link them.

## Gaps

The spec pages record `H-1` to `H-61`, and `H-79` and `H-80` in [ha-and-helm.md](../spec/ha-and-helm.md). Each item's text lives in
the `What this does not cover` section of the spec page that has its mechanism, and this list only links to it. The perspective pages record `H-62` to `H-78`, each under the heading of the page
that describes its mechanism.

| Where | Items (linked to their current home) |
|---|---|
| [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md#what-this-does-not-cover) | [H-62](roles-and-trust-boundaries.md#h-62--client-address-follows-the-header-the-edge-sends), [H-63](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships) |
| [transport-encryption.md](transport-encryption.md#what-this-does-not-cover) | [H-64](transport-encryption.md#h-64--sentinel-trust-with-the-chart-defaults), [H-65](transport-encryption.md#h-65--a-rotated-out-certificate-keeps-serving-open-connections), [H-66](transport-encryption.md#h-66--a-loaded-certificate-is-served-past-its-expiry), [H-67](transport-encryption.md#h-67--no-revocation-check-for-client-certificates), [H-68](transport-encryption.md#h-68--the-operations-listener-serves-a-temporary-certificate-until-its-pair-loads), [H-69](transport-encryption.md#h-69--tracingca_file-is-read-once-at-start) |
| [data-and-secret-flow.md](data-and-secret-flow.md#what-this-does-not-cover) | [H-70](data-and-secret-flow.md#h-70--the-standard-output-log-carries-every-record-in-full) |
| [privilege-footprint.md](privilege-footprint.md#what-this-does-not-cover) | [H-71](privilege-footprint.md#h-71--single-role-mode-gives-the-runtime-login-ddl-rights), [H-72](privilege-footprint.md#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist), [H-73](privilege-footprint.md#h-73--installations-that-share-one-valkey-share-its-user), [H-74](privilege-footprint.md#h-74--the-default-object-store-credential-chain-can-reach-node-metadata), [H-75](privilege-footprint.md#h-75--the-audit-object-grants-are-not-checked-at-start) |
| [isolation-and-coordination.md](isolation-and-coordination.md#what-this-does-not-cover) | [H-76](isolation-and-coordination.md#h-76--advisory-locks-are-per-database-not-per-schema), [H-77](isolation-and-coordination.md#h-77--a-lost-commit-answers-503-and-a-clients-retry-can-duplicate-the-write) |
| [audit.md](audit.md#what-this-does-not-cover) | [H-39](audit.md#h-39--audit-records-are-not-tamper-evident), [H-121](audit.md#h-121--event-details-and-journal-entries-are-not-checked-for-secrets), [H-122](audit.md#h-122--a-crashed-replica-loses-the-buffered-asynchronous-events-and-its-open-summaries), [H-123](audit.md#h-123--the-store-has-no-permission-check-of-its-own) |
| [egress.md](egress.md#what-this-does-not-cover) | [H-78](egress.md#h-78--the-nat64-check-knows-only-the-well-known-prefix) |

Spec pages hold the gaps of their mechanisms, among them [H-41](../spec/operations-and-observability.md#h-41--egress-is-unrestricted-and-ingress-enforcement-is-verified-on-one-cni-only),
[H-53](../spec/operations-and-observability.md#h-53--span-export-bypasses-the-egress-address-policy),
[H-55](../spec/architecture.md#h-55--key-files-of-the-stored-secret-key-ring-are-read-once-at-start-up),
[H-56](../spec/architecture.md#h-56--the-object-store-endpoint-skips-the-egress-address-policy) and
[H-6](../spec/repositories-and-proxy.md#h-6--remote-repositories-reach-private-networks). The perspective pages link them where
they apply and do not repeat them.

A new gap takes the next free number across all spec and security pages. The command is in
[developer/README.md](../developer/README.md#id-schemes).

## What this does not cover

This page describes no mechanism and records no gap of its own. It does not judge whether the recorded items are complete. Each
perspective page's `What this does not cover` section and each review of a perspective page decide that.
