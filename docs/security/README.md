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
| [data-and-secret-flow.md](data-and-secret-flow.md) | Where each credential lives, when a process reads it, what it holds in memory, what Valkey may hold | the file-only credentials and their re-read rules; the key ring and its file form; the sealed envelope and its additional data; the upstream credentials of remote repositories (sealed rows, references, the clients); redaction |
| [privilege-footprint.md](privilege-footprint.md) | What trove itself may do on PostgreSQL, Valkey, the object store, the pod and the network | the two-role and single-role grants and the preflight; the bookkeeping revoke; the Valkey user; the S3 operations trove issues; the pod security context; the ingress NetworkPolicy |
| [isolation-and-coordination.md](isolation-and-coordination.md) | Which replica may hold singleton state, how a stale writer is fenced, who can change what replicas read, and where installations can collide | the role sets; session advisory leases; the fence on `pg_locks`; the change log and its Valkey evictions; the replica registry; the handling of a lost COMMIT |
| [audit.md](audit.md) | Audit trail: who can write, read and change events; retention; the journal row; where a lost event survives | the event registry and journal flag; the transactional and asynchronous writers; the runtime role's insert-only grants; retention and partitions; the log copy |
| [repository-model.md](repository-model.md) | What a repository configuration can admit or refuse, who can change it, and how a change reaches every replica | keys and reserved names; include and exclude patterns; the immutable identity; the snapshot and its invalidation; the deletion job; the routing of virtual repositories (claims, pin rules, the global-claim permission, the auto claim); what the virtual engine decides per caller (readable members, the merged-metadata key, the explanation, the shadowing check) |
| [tokens-and-credentials.md](tokens-and-credentials.md) | Which credentials a request may carry, in which order they are read, how a token is stored, checked, scoped and revoked | the credential precedence and the 401 rule; the token format and its SHA-256 storage; scopes as a ceiling; lifetime limits; revocation across replicas with and without Valkey; the failed-credential throttle |
| [login-and-sessions.md](login-and-sessions.md) | Who signs in how: the OIDC login and what it trusts the IdP and the browser with, where the login secrets live, the checks a login passes, sessions, CSRF and the break-glass account | the flow row and its single use; the ID-token checks; the claim gates; the session cookies, the session cache and the end of a session; the CSRF rules; the `auth.flow_starts` limit; the break-glass lockout, slots and rotation hold |
| [permissions.md](permissions.md) | Who may do what: deny by default, policies, roles and capabilities, the token scope as a ceiling, hidden existence, how a change reaches every replica | the evaluation of R-AUTHZ-015; the grant index and its epochs; `Trove-Authz-Min-Epoch`; the bootstrap read bridge; decision audit |
| [api-edge.md](api-edge.md) | What the management API does at its edge before a handler runs | strict body decoding; CORS; the cluster-wide rate-limit classes and their fallback; idempotency keys; the security headers |
| [endpoints-and-exposure.md](endpoints-and-exposure.md) | What an outside audience on a further endpoint can present and see | the accepted credentials and bound tokens per endpoint; exposure rules as a ceiling for every caller; hidden content as a missing one; the endpoint rate-limit class; the endpoint API |
| [file-transfer.md](file-transfer.md) | What a client can upload, download and delete under `/files/`, and what the served bytes can do in a browser | path cleaning; the decision of every request and listing; digests; Range; the response headers of served files; deletes and their job |
| [egress.md](egress.md) | Which outbound connections are checked against which address rules, and where the check does not reach | the address policy at connect time; zoned IPv6 refusal; the deny and allow lists; NAT64; the objectstore and span-export exceptions |

Mechanisms of the spec that are not built yet have no page. Their rules stay in the spec pages until the code exists:
input validation of package content, the rotation and propagation of identity and upstream changes, the GitLab CI federation and the
backup data flow ([docs/spec/](../spec/README.md)).

Every page links its gaps; the spec pages keep the gaps whose mechanism they describe, and the perspective pages link them.

## Gaps

The spec pages record `H-1` to `H-61`, and `H-79` and `H-80` in [ha-and-helm.md](../spec/ha-and-helm.md). Each item's text lives in
the `What this does not cover` section of the spec page that has its mechanism, and this list only links to it. The perspective pages record `H-62` to `H-78`, `H-81` to `H-85`, `H-91` and `H-93`, and more, each under the heading of the page
that describes its mechanism.

| Where | Items (linked to their current home) |
|---|---|
| [roles-and-trust-boundaries.md](roles-and-trust-boundaries.md#what-this-does-not-cover) | [H-62](roles-and-trust-boundaries.md#h-62--client-address-follows-the-header-the-edge-sends), [H-63](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships) |
| [transport-encryption.md](transport-encryption.md#what-this-does-not-cover) | [H-64](transport-encryption.md#h-64--sentinel-trust-with-the-chart-defaults), [H-65](transport-encryption.md#h-65--a-rotated-out-certificate-keeps-serving-open-connections), [H-66](transport-encryption.md#h-66--a-loaded-certificate-is-served-past-its-expiry), [H-67](transport-encryption.md#h-67--no-revocation-check-for-client-certificates), [H-68](transport-encryption.md#h-68--the-operations-listener-serves-a-temporary-certificate-until-its-pair-loads), [H-69](transport-encryption.md#h-69--tracingca_file-is-read-once-at-start) |
| [data-and-secret-flow.md](data-and-secret-flow.md#what-this-does-not-cover) | [H-70](data-and-secret-flow.md#h-70--the-standard-output-log-carries-every-record-in-full), [H-280](data-and-secret-flow.md#h-280--a-secret-that-cannot-be-resolved-is-found-at-its-first-use), [H-281](data-and-secret-flow.md#h-281--a-holder-of-repositorieswrite-config-can-send-any-file-of-secrets_dir-to-an-upstream-of-their-choice) |
| [privilege-footprint.md](privilege-footprint.md#what-this-does-not-cover) | [H-71](privilege-footprint.md#h-71--single-role-mode-gives-the-runtime-login-ddl-rights), [H-72](privilege-footprint.md#h-72--the-default-valkey-user-holds-every-right-until-acl-users-exist), [H-73](privilege-footprint.md#h-73--installations-that-share-one-valkey-share-its-user), [H-74](privilege-footprint.md#h-74--the-default-object-store-credential-chain-can-reach-node-metadata), [H-75](privilege-footprint.md#h-75--the-audit-object-grants-are-not-checked-at-start) |
| [isolation-and-coordination.md](isolation-and-coordination.md#what-this-does-not-cover) | [H-76](isolation-and-coordination.md#h-76--advisory-locks-are-per-database-not-per-schema), [H-77](isolation-and-coordination.md#h-77--a-lost-commit-answers-503-and-a-clients-retry-can-duplicate-the-write) |
| [audit.md](audit.md#what-this-does-not-cover) | [H-39](audit.md#h-39--audit-records-are-not-tamper-evident), [H-121](audit.md#h-121--event-details-and-journal-entries-are-not-checked-for-secrets), [H-122](audit.md#h-122--a-crashed-replica-loses-the-buffered-asynchronous-events-and-its-open-summaries), [H-144](audit.md#h-144--reading-the-audit-trail-leaves-no-event-except-for-the-export) |
| [repository-model.md](repository-model.md#what-this-does-not-cover) | [H-132](repository-model.md#h-132--a-replica-enforces-the-previous-repository-settings-until-its-snapshot-is-rebuilt), [H-133](repository-model.md#h-133--paths-are-compared-byte-for-byte-without-unicode-normalisation), [H-274](repository-model.md#h-274--a-stored-claim-that-does-not-parse-protects-nothing), [H-283](repository-model.md#h-283--honouring-restricted-is-up-to-the-formats-merge) |
| [tokens-and-credentials.md](tokens-and-credentials.md#what-this-does-not-cover) | [H-82](tokens-and-credentials.md#h-82--rejected-credentials-of-one-address-are-counted-together), [H-83](tokens-and-credentials.md#h-83--anonymous-requests-reach-the-management-api-handlers), [H-84](tokens-and-credentials.md#h-84--last-use-of-a-token-is-not-recorded), [H-85](tokens-and-credentials.md#h-85--group-names-from-the-login-flow-are-applied-as-given), [H-134](tokens-and-credentials.md#h-134--revocation-by-value-and-the-login-routes-share-one-budget-per-address) |
| [login-and-sessions.md](login-and-sessions.md#what-this-does-not-cover) | [H-91](login-and-sessions.md#h-91--provisioning-and-the-session-commit-in-two-transactions), [H-93](login-and-sessions.md#h-93--an-enabled-break-glass-account-without-a-password-file-starts-silently), [H-210](login-and-sessions.md#h-210--anyone-who-reaches-the-local-login-can-keep-the-break-glass-account-locked), [H-211](login-and-sessions.md#h-211--many-distinct-addresses-can-still-fill-the-pending-flow-cap) |
| [permissions.md](permissions.md#what-this-does-not-cover) | [H-102](permissions.md#h-102--a-caller-that-decides-several-coordinates-without-begin-can-straddle-an-index-swap), [H-103](permissions.md#h-103--package-ownership-and-the-populate-check-have-no-callers-yet-the-shadowing-check-does-not-exist), [H-104](permissions.md#h-104--the-commit-time-re-check-decides-on-the-grants-not-on-the-credential), [H-105](permissions.md#h-105--the-revocation-journal-is-written-and-not-replayed), [H-212](permissions.md#h-212--the-identity-providers-group-sync-can-remove-the-last-administrator), [H-290](permissions.md#h-290--a-raw-delete-of-an-npm-tarball-through-files-asks-no-ownership-question) |
| [api-edge.md](api-edge.md#what-this-does-not-cover) | [H-111](api-edge.md#h-111--the-read-and-write-budgets-are-per-class-not-per-route), [H-112](api-edge.md#h-112--a-counter-lost-in-a-valkey-failover-restarts-and-valkey-write-access-moves-the-limits), [H-113](api-edge.md#h-113--the-fallback-share-is-approximate), [H-114](api-edge.md#h-114--anonymous-callers-behind-one-address-share-one-bucket), [H-115](api-edge.md#h-115--nothing-detects-an-operation-that-returns-a-secret-without-declaring-it), [H-273](api-edge.md#h-273--a-preflight-is-answered-without-authentication-and-is-not-counted-by-the-rate-limits) |
| [endpoints-and-exposure.md](endpoints-and-exposure.md#what-this-does-not-cover) | [H-166](endpoints-and-exposure.md#h-166--a-job-that-acts-on-behalf-of-a-request-decides-without-the-requests-endpoint), [H-167](endpoints-and-exposure.md#h-167--the-fallback-share-counts-the-replicas-of-one-pool), [H-294](endpoints-and-exposure.md#h-294--the-code-of-a-members-failure-tells-that-the-addressed-repository-reaches-an-upstream), [H-295](endpoints-and-exposure.md#h-295--a-merged-or-rewritten-document-is-filtered-only-where-its-handler-calls-the-filter) |
| [file-transfer.md](file-transfer.md#what-this-does-not-cover) | [H-154](file-transfer.md#h-154--an-insert-that-was-in-flight-when-the-delete-was-checked-is-removed-without-a-decision), [H-155](file-transfer.md#h-155--the-first-request-for-an-unknown-repository-key-costs-a-query-that-a-known-key-does-not), [H-156](file-transfer.md#h-156--a-repository-has-no-quota-so-a-holder-of-deploy-is-bounded-only-by-the-size-limits), [H-208](file-transfer.md#h-208--a-forged-listing-cursor-tells-where-a-hidden-item-sorts), [H-209](file-transfer.md#h-209--a-holder-of-deploy-without-read-learns-whether-a-file-exists) |
| [egress.md](egress.md#what-this-does-not-cover) | [H-78](egress.md#h-78--the-nat64-check-knows-only-the-well-known-prefix), [H-282](egress.md#h-282--the-upstream-test-tells-a-holder-of-repositorieswrite-config-what-it-sees-of-an-address) |

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
