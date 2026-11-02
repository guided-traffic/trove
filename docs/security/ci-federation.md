# CI federation: issuers, signing keys and mappings

Status: the issuer record, its read-only configuration form and the signing-key store (work package W1 of
[ci-oidc.md](../developer/ci-oidc.md)) and the identity mappings with their save-time rules (work package W3) are built; the
exchange and the use-time evaluation are specified in [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) and described here when they
exist. This page answers two questions: what does trove trust when it trusts a CI issuer, and what can reach or change that trust;
and what decides what a verified job receives.

## What trust in an issuer means

A CI issuer ([R-CI-001](../spec/gitlab-ci-oidc.md)) is a name, an issuer URL and the keys that URL publishes. trove accepts a token as
that issuer's when the `iss` claim equals the stored URL byte for byte and the signature verifies with one of the stored keys. Nothing
else makes a token trusted: the audience, the algorithm list and the claims are checks on top of that
([R-CI-004](../spec/gitlab-ci-oidc.md)). Whoever controls the issuer's key set, a GitLab administrator or whoever takes over its domain
or its TLS, can sign for it; the specification keeps that case as [H-24](../spec/gitlab-ci-oidc.md).

| Who | What they can do with an issuer | Where it is enforced |
|---|---|---|
| a system administrator (`ci:write`, recent authentication) | create, replace and delete issuers; force a key refresh | the operation declarations; [issuers_write.go](../../internal/ci/issuers_write.go) |
| an auditor or security administrator (`ci:read`) | read issuers and their key status | the declarations |
| a project administrator with `managePolicies` | list the issuers read-only: name, kind, issuer URL, public flag | [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (`requireProject`) |
| `trove migrate` | create, replace and disable the issuers of `ci.issuers` (source `configuration`) | [issuers_declared.go](../../internal/ci/issuers_declared.go) |
| a CI token | nothing: the management API refuses it ([Q-CI-18](../spec/gitlab-ci-oidc.md)) | `ciGate` |

An issuer with source `configuration` cannot be changed or deleted through the API (409 `issuer_managed_by_configuration`) and is
deleted only after it was removed from the configuration. That keeps a GitOps repository the one author of such an issuer; no replica
writes it, so replicas of two releases never overwrite each other.

The rules that keep a bad issuer out are the pure ones of [issuers_validate.go](../../internal/ci/issuers_validate.go): an https issuer
URL without credentials, no `none` and no HMAC algorithm (RFC 8725), pin claims limited to the immutable ID claims, a public issuer
URL (`gitlab.com`) that cannot be declared private, the closed `kind` enum, and `ca_bundle_ref` limited to `file:<absolute path>` or a
variable named `TROVE_CIC_<NAME>`, so that no variable of trove's own can be named
([Q-CI-31](../spec/gitlab-ci-oidc.md)).

## Key handling

- **The key set is a PostgreSQL row, never a Valkey value.** The last good set of an issuer is its row in `ci_issuer_keys`; every
  replica holds a copy, invalidated through the change area `ci_issuer_keys`. A key forged into Valkey would let anyone mint CI tokens,
  so nothing that selects or holds a key is read from there ([R-ARCH-030](../spec/architecture.md)).
- **Only public members are stored.** A fetched set is reduced to `kty`, `kid`, `use`, `key_ops`, `alg` and the public key parameters
  ([keys_jwks.go](../../internal/ci/keys_jwks.go)); a private member, a certificate chain or a URL that the issuer publishes never reaches
  the table. At most 100 keys are kept.
- **A key is used only if it passes the rules of R-CI-003**: `use` absent or `sig`, `key_ops` absent or containing `verify`, a key type
  with a verification algorithm of R-CI-001, an RSA modulus of at least 2048 bits, one key per `kid`. The others are shown in the key
  status with the reason. A token's own `alg` against the key's and the key type against the algorithm are checked by the validation.
- **A removed key is gone.** A set from a successful fetch replaces the stored one; a key that is missing from it is no longer accepted,
  on every replica within the change-area bound.
- **Fail closed.** A set older than `ci.signing_keys.max_stale` is refused (`signing_keys_unavailable`), after the replica re-read the
  row, so every replica fails at the same moment within one `cluster.epoch_poll_interval`. A first fetch that fails stores a placeholder row
  with no set and `fetched_at` at the epoch, which is stale by construction ([Q-CI-32](../spec/gitlab-ci-oidc.md)).
- **The write rule** ([keys_store.go](../../internal/ci/keys_store.go)): a fetch runs outside any transaction; its result is written in one
  short transaction on the key row alone. The `ci_issuers` row, which every exchange holds `FOR SHARE`, is not locked, so a store neither
  waits for an exchange nor blocks one. A set with a later `fetched_at` is kept.

## Egress of discovery and key fetches

Every discovery and key request is made by a client of the outbound factory ([R-ARCH-022](../spec/architecture.md), see
[egress.md](egress.md)): TLS against the installation's trust (system roots and `proxy.outbound.tls.ca_files`) plus the issuer's
`ca_bundle_ref`, at most three redirects, https only (a redirect to http is refused), `ci.signing_keys.max_response_bytes`,
`ci.signing_keys.fetch_timeout`, and the address policy at connect time on every hop. A `jwks_uri` that a discovery document names, or
a redirect target, at an address the policy denies (the metadata addresses, loopback, link-local, private ranges as configured) gets no
connection; the failure class is `egress_denied`.

What a failure records is the class, the HTTP status when the issuer answered, and the host of the URL
([FetchError](../../internal/ci/keys_fetch.go)): in `last_error`, in the key status and in the event `ci.keys.refresh_failed`. No byte of a
response body and no address or rule text is in any of them, so a discovery document that points `jwks_uri` at an internal service cannot
read that service's answer back through trove. URLs come only from the issuer record and its discovery document: `jku`, `x5u`, `jwk`
and `x5c` of a token never reach the key store, whose lookup takes the `kid` and the algorithm and nothing else.

## What a forged `kid` costs

A token chooses its `kid` freely and is refused only by the signature check, so the lookup for an unknown `kid` is throttled
([keys_source.go](../../internal/ci/keys_source.go)): at most one re-read of the issuer's row per replica and
`cluster.epoch_poll_interval`, shared by concurrent requests, then at most one fetch per replica and
`ci.signing_keys.unknown_kid_refresh_interval`, shared the same way. A flood of random `kid` values therefore costs n row reads and n
fetches per interval for n `web` replicas, never one per request, and a token of an unknown issuer costs no query at all because the
issuer is resolved from the in-process copy of all issuers. The throttles are per-replica state that can change an answer for at most
one interval ([R-ARCH-032](../spec/architecture.md)); [H-409](#h-409--an-attacker-can-delay-a-rotated-key-on-one-replica) is the one
consequence.

## The mappings: the access control of an anonymous endpoint

The exchange is anonymous: its caller proves a job identity with a token and nothing else. What that identity receives is decided by
the mapping set of the issuer alone ([R-CI-009](../spec/gitlab-ci-oidc.md)), so the mappings are the access control of the
endpoint and the rules on saving them are its configuration checks. A token is evaluated per tier, the first matching mapping of a
tier decides it, and the tiers add up: the administrators' tier `system` and one tier `project:<key>` per project
([select.go](../../internal/ci/select.go)). A disabled mapping is not looked at, so disabling a narrow mapping lets its jobs fall
through to the next one of the tier, which can grant more; the event of the disable carries a journal entry for that reason
([mappings_store.go](../../internal/ci/mappings_store.go)).

| Who | What they can do with a mapping | Where it is enforced |
|---|---|---|
| a system administrator (`ci:write`, recent authentication) | create, replace and delete mappings of every tier; a project's mapping keeps the bounds of its tier | the declarations; [mappings_write.go](../../internal/ci/mappings_write.go) |
| an auditor or security administrator (`ci:read`) | read the mappings, run the dry run, read the snippet | the declarations |
| a project administrator with `managePolicies` | the same for the tier of its project, within the bounds below | [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (`requireProject`), [mappings_bind.go](../../internal/ci/mappings_bind.go) |
| a holder of `policies:write` | change what a policy gives a mapping that it lists, which no check of the mapping sees ([H-412](#h-412--a-policy-or-group-widened-after-the-save-is-not-held-to-the-write-rules)) | the policy store |
| a CI token | nothing: the management API refuses it ([Q-CI-18](../spec/gitlab-ci-oidc.md)) | `ciGate` |

**The rules on save** are checked in the transaction of the write, by the pure engine ([check.go](../../internal/ci/claims/check.go)) and
the lookups that need the database ([mappings_write.go](../../internal/ci/mappings_write.go) `check`). Each refuses a mapping that would
hand an anonymous caller more than its author meant:

- a condition with no leaf (`catch_all_condition`) matches every job of the issuer; on a public issuer (gitlab.com) a condition
  without an `equals` or `in` leaf on an immutable ID claim (`pin_required`) matches any project of the world that can reach the
  audience ([R-CI-005](../spec/gitlab-ci-oidc.md));
- a grant of `deploy`, `overwrite` or `delete` needs `ref_protected` at the top of the condition (`unprotected_write`), since on an
  unprotected ref anyone who can push a branch gets the grant, and on a private issuer a write through a policy, a group or an
  inline grant without ownership `bind_project` needs a pin (`unpinned_write`). The actions that policies and groups give the
  mapping are read from their entries and counted ([Q-CI-35](../spec/gitlab-ci-oidc.md));
- `manage` is never grantable, to a CI token or through a group that holds it (`action_not_allowed`);
- a template that a claim value alone can turn into a repository selector is refused (`template_unanchored`), and a render failure
  at the exchange fails it instead of falling through to a broader mapping;
- a cross-project (fork) job gets nothing from a mapping that has not opted in with `read_only`, and then only `read`; the first match
  of a tier decides, so a denying mapping does not fall through to a later one that would opt in
  ([select.go](../../internal/ci/select.go)).

**A project's mapping** is bounded like a project policy ([Q-CI-26](../spec/gitlab-ci-oidc.md), [Q-CI-37](../spec/gitlab-ci-oidc.md)):
its name starts with the project key, its policies are the project's, it has no groups, a literal repository of an inline grant is a
repository of the project, and every contribution of the token names the project, so the evaluator compiles `@any` and patterns
against the project's repositories only. A policy name that is not the project's, existing or not, answers the same
(`policy_outside_project`). A create in the tier locks the project row, so a project cannot be deleted under a mapping.

**The principal** of a mapping mirrors `enabled`: disabling it sets the principal `disabled` in the transaction and notifies the
change area `auth`, which is the status token validation reads ([R-CI-017](../spec/gitlab-ci-oidc.md), R-AUTH-023); no CI token
exists yet to show it, because the exchange is not built. Deleting a mapping deletes its principal and,
through the foreign keys, its entries in policies, so a mapping created under the same name inherits nothing. A mapping that has an
issuance record is disabled or deleted only through the revoker; until it exists the write answers 501 and changes nothing.

**The dry run** takes a claim set and never a token; it issues nothing, records no `jti` and writes no event. A project
administrator's trace shows its tier only. It reads no policy, which the answer says when a `scope` item depends on one
([Q-CI-36](../spec/gitlab-ci-oidc.md)). **The snippet** holds the variables of the job and no token, and the revocation as its last step.

**Every write** records an event with the record before and after (`ci.mapping.*`, category `identity`, the project in the details);
`.updated`, `.disabled` and `.deleted` also enter the revocation journal with the mapping's ID, issuer, name, tier, priority and the
record before ([R-CI-020](../spec/gitlab-ci-oidc.md)). A PUT writes exactly one event, so a change that disables is not hidden behind
an `.updated`.

## What this does not cover

### H-408 · A writer on `ci_issuer_keys` can plant a signing key

The key set of an issuer decides whether a token is accepted, and it is a row that the runtime role of the `web` and `worker`
replicas writes. Anyone who can write that row (a compromised replica, an operator with the runtime credentials) can insert a key and
mint tokens that pass the signature check; the audit event of a fetch does not exist for such a write. Live today for every holder of
the runtime database credentials; the privilege footprint in [privilege-footprint.md](privilege-footprint.md) is what limits who holds
them. The row cannot be signed, because the issuer's key is the thing being stored.

### H-409 · An attacker can delay a rotated key on one replica

A replica whose fetch throttle an earlier request has spent rejects a token signed with a newly rotated key until another replica or
the task has stored it, for at most `ci.signing_keys.unknown_kid_refresh_interval`. An attacker who sends one token with an unknown
`kid` just before a legitimate job's token arrives can spend the throttle on the replicas that answer. Live while a rotation is in
progress; the cost is a retryable 400 `unknown_signing_key` for that interval, and the scheduled refresh stores the key within
`ci.signing_keys.min_refresh_interval` at the latest.

### H-410 · An issuer administrator can probe which files exist on a replica

`ca_bundle_ref` takes an absolute path. The bundle is never shown, but a path that does not load fails the fetch with the class
`trust_store_unavailable` and one that loads does not, so a holder of `ci:write` learns whether a readable PEM file exists at a path
on the replica. Live for holders of `ci:write` only, which the role `admin` and the role `security-admin` hold; the variable form is
limited to the `TROVE_CIC_` prefix and gives no such probe.

### H-411 · An issuer administrator can probe addresses that the egress policy allows

Creating an issuer with validation, or forcing a refresh, makes trove request a URL that the administrator chose. The answer is never
returned, but the failure class and the HTTP status code are: with the default policy every internal range is denied and only public
hosts can be probed; a policy that allows an internal range through `proxy.outbound.address_policy.allow_cidrs` also lets a holder of
`ci:write` learn whether a host in that range answers on https and with which status. Dormant with the default policy; live as soon as
an `allow_cidrs` entry covers an internal range.

### H-412 · A policy or group widened after the save is not held to the write rules

`unprotected_write` and `unpinned_write` count the actions that the listed policies and groups give a mapping when the mapping is
saved. An administrator who later widens the mapping's entry in the policy, or the grants of a group the mapping lists, gives the
mapping, and every token it issues, writes that the rules never saw: policy and group grants are evaluated when a token is used. Live
today for every mapping that lists a policy or a group, for holders of `policies:write` and for project administrators with
`managePolicies` on their project's policies. What bounds it: a policy entry of a mapping never holds `manage`, a write is still
decided by the package ownership of the repository ([R-CI-015](../spec/gitlab-ci-oidc.md)), and a project policy reaches the
project's repositories only. The widening is visible in the policy's own event, not in the mapping's.

### H-413 · A project administrator can probe the mapping names of the system tier below its prefix

The name of a mapping is unique per issuer across all tiers, and a create that finds the name taken answers 409 `already_exists`
([mappings_write.go](../../internal/ci/mappings_write.go)). A project administrator names its mappings `<key>-…` and can therefore
learn whether a mapping of any tier, a system administrator's included, has a given name that starts with its own project key on an
issuer it can see. Live for project administrators with `managePolicies`; the answer reveals the existence of one name at a time and
nothing of the mapping, and the prefix limits the probe to the project's own namespace.

### H-414 · A binding removed by a mapping save is not in the policy's own journal

A save that removes a mapping from a policy, and the delete of a mapping, change the policy's principal list in the mapping's
transaction. The policy's version moves and the change area `authz` is notified, but the policy's own events (`authz.policy.updated`)
and its entry in the revocation journal ([R-AUTHZ-031](../spec/authorization.md)) are not written; the mapping's event carries the
policies in its before and after record, and its journal entry has the record before the change. Live whenever a PUT or a DELETE of
a mapping removes an entry. After a point-in-time restore the policy's removed entry is restored with its rows and only the mapping's
journal entry (a disable, never a delete, [R-CI-020](../spec/gitlab-ci-oidc.md)) is replayed; a reader of the policy audit trail does not
see the removal.
