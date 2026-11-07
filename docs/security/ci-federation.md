# CI federation: issuers, signing keys and mappings

Status: the issuer record, its read-only configuration form and the signing-key store (work package W1 of
[ci-oidc.md](../developer/ci-oidc.md)), the identity mappings with their save-time rules (work package W3), the token exchange (work
package W4) and the use time of a CI token (work package W5) are built. This page answers three questions: what does trove trust when it
trusts a CI issuer, and what can reach or change that trust; what an anonymous caller of the exchange can cause; and what decides what a
verified job receives.

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
| an Owner of a project | list the issuers read-only: name, kind, issuer URL, public flag | [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (`requireProject`) |
| `trove migrate` | create, replace and disable the issuers of `ci.issuers` (source `configuration`) | [issuers_declared.go](../../internal/ci/issuers_declared.go) |
| a CI token | nothing: the management API refuses it ([Q-CI-18](../spec/gitlab-ci-oidc.md)) | `ciGate` |

An issuer with source `configuration` cannot be changed or deleted through the API (409 `issuer_managed_by_configuration`) and is
deleted only after it was removed from the configuration. That keeps a GitOps repository the one author of such an issuer; no replica
writes it, so replicas of two releases never overwrite each other. A declared name keeps its issuer URL and its kind: `trove migrate`
stops on an entry that changes either, naming both values, so the pins, the package owners and the mappings of the name never move to
another GitLab instance under the same issuer ID ([Q-CI-48](../spec/gitlab-ci-oidc.md#q-ci-48--a-declared-issuers-issuer-url-and-kind-never-change-under-its-name)).

Two settings carry trust that other state was derived from, and a write that moves them moves that state in its transaction
([Q-CI-50](../spec/gitlab-ci-oidc.md#q-ci-50--an-issuer-change-that-moves-trust-resets-the-keys-or-is-refused),
[issuers_trust.go](../../internal/ci/issuers_trust.go)): a changed `jwks_uri` or `discovery_url` replaces the stored key set with the
fail-closed placeholder, so no key of the old location validates a token after the commit, and the new location is fetched after it;
switching `public_issuer` on, or removing a claim from `pin_claims`, is refused with `409 mappings_unpinned` while a mapping of the issuer
would fail the pin rule of a save under the new settings, and the answer names those mappings.

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
  of each usable key, and to `kid`, `kty`, `alg` and the reason of each key the rules ignore
  ([keys_jwks.go](../../internal/ci/keys_jwks.go)); a private member, a certificate chain or a URL that the issuer publishes never reaches
  the table. At most 100 keys are kept.
- **A key is used only if it passes the rules of R-CI-003**, applied by one implementation, `jws.ParseJWKS` of the leaf the login shares
  ([Q-CI-42](../spec/gitlab-ci-oidc.md#q-ci-42--one-implementation-of-the-key-rules-the-shared-leaf)): `use` absent or `sig`, `key_ops`
  absent or containing `verify`, RSA, EC on P-256 or P-384 or OKP Ed25519, an RSA modulus of at least 2048 bits, a key's own `alg` that
  fits its type and curve, no private member. The others are shown in the key status with the reason (`rsa_key_too_small`,
  `private_key_material`, …). The key status and the keys the validation tries come from that one parse, so they cannot disagree; the
  candidates of a token are the keys of its `kid` that fit its `alg` (`jws.Candidates`), and two keys of one `kid` are both tried.
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
fetches per interval for n `web` replicas, never one per request. A token of an unknown issuer is answered from the in-process copy of
all issuers; a miss, and a hit of an issuer the copy holds as disabled, re-read the issuers at most once per replica and
`cluster.epoch_poll_interval` the same way ([issuers_read.go](../../internal/ci/issuers_read.go) `rereadIssuers`,
[Q-CI-51](../spec/gitlab-ci-oidc.md#q-ci-51--what-the-exchange-tells-a-caller-and-when-a-replica-learns-an-issuer)), so made-up `iss`
values cost one read per interval and replica, and a replica whose copy lags serves a just-created or re-enabled issuer within that
interval. The throttles are per-replica state that can change an answer for at most one interval
([R-ARCH-032](../spec/architecture.md)); [H-409](#h-409--an-attacker-can-delay-a-rotated-key-on-one-replica) is the one consequence.

## The mappings: the access control of an anonymous endpoint

The exchange is anonymous: its caller proves a job identity with a token and nothing else. What that identity receives is decided by
the mapping set of the issuer alone ([R-CI-009](../spec/gitlab-ci-oidc.md)), so the mappings are the access control of the
endpoint and the rules on saving them are its configuration checks. A token is evaluated per tier, the first matching mapping of a
tier decides it, and the tiers add up: the administrators' tier `system` and one tier `project:<projectKey>` per project
([select.go](../../internal/ci/select.go)). A disabled mapping is not looked at, so disabling a narrow mapping lets its jobs fall
through to the next one of the tier, which can grant more; the event of the disable carries a journal entry for that reason
([mappings_store.go](../../internal/ci/mappings_store.go)).

| Who | What they can do with a mapping | Where it is enforced |
|---|---|---|
| a system administrator (`ci:write`, recent authentication) | create, replace and delete mappings of every tier; a project's mapping keeps the bounds of its tier | the declarations; [mappings_write.go](../../internal/ci/mappings_write.go) |
| an auditor or security administrator (`ci:read`) | read the mappings, run the dry run, read the snippet | the declarations |
| an Owner of a project | the same for the tier of its project, within the bounds below | [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (`requireProject`), [mappings_bind.go](../../internal/ci/mappings_bind.go) |
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
  at the exchange never falls through to a broader mapping of the tier: a failure of the system tier fails the exchange, a failure of
  a project's tier drops that tier's contribution ([Q-CI-47](../spec/gitlab-ci-oidc.md#q-ci-47--a-project-tier-changes-only-what-its-own-tier-gives-a-token));
- a condition is bounded in size (a literal 1,024 characters, a claim name 256, a path 16 members, the whole 64 KiB) and a project's
  tier in count (`ci.mappings.max_per_project`), because every exchange of the issuer evaluates every tier
  ([Q-CI-49](../spec/gitlab-ci-oidc.md#q-ci-49--what-bounds-the-cost-of-an-exchange), [H-426](#h-426--several-projects-together-can-fill-an-issuers-mapping-limit));
- a cross-project (fork) job gets nothing from a mapping that has not opted in with `read_only`, and then only `read`; the first match
  of a tier decides, so a denying mapping does not fall through to a later one that would opt in
  ([select.go](../../internal/ci/select.go)).

**A project's mapping** is bounded like a project policy ([Q-CI-26](../spec/gitlab-ci-oidc.md), [Q-CI-37](../spec/gitlab-ci-oidc.md)):
its name is a name within the project (the principal is `<issuer>/<projectKey>/<name>`), its policies are the project's, it has no groups, its
selectors are bare keys of the project (a qualified selector is refused as `invalid_pattern`), a literal repository of an inline grant is a
repository of the project, and every contribution of the token names the project, so the evaluator compiles `@any` and patterns
against the project's repositories only; the token carries the selectors in their instance form (`web/npm-*`). A policy name that is not the project's, existing or not, answers the same
(`policy_outside_project`). A create in the tier locks the project row, so a project cannot be deleted under a mapping. A name is unique within
the issuer's tier, so `409 already_exists` answers only for a name of the caller's own tier and tells nothing of the system tier's or another project's
mappings.

A project's mapping reaches its own contribution to a token and nothing else of it
([Q-CI-47](../spec/gitlab-ci-oidc.md#q-ci-47--a-project-tier-changes-only-what-its-own-tier-gives-a-token)): a token belongs to the
system tier's mapping whenever it contributes ([ttl.go](../../internal/ci/ttl.go) `tokenOwner`), so the token's lifetime is the system
tier's and the global bounds; the project's `maxTtlSeconds` ends its own contribution (`expiresAt` in the bundle); its render failure
drops its contribution; its disable or delete revokes only the tokens it owns and ends its contribution to the others at use time
([H-424](#h-424--a-project-mapping-that-owns-a-token-ends-the-other-projects-contributions-with-it),
[H-425](#h-425--a-project-mappings-end-reaches-the-tokens-of-other-tiers-within-the-index-bound-not-the-revocation-bound)).

**The principal** of a mapping mirrors `enabled`: disabling it sets the principal `disabled` in the transaction and notifies the
change area `auth`, which is the status token validation reads ([R-CI-017](../spec/gitlab-ci-oidc.md), R-AUTH-023), and the exchange
reads it `FOR SHARE` before it issues a token, so a token of a disabled mapping is neither issued nor accepted. Deleting a mapping
deletes its principal and, through the foreign keys, its entries in policies, so a mapping created under the same name inherits
nothing. A mapping that has an issuance record is disabled or deleted through the revoker, in the transaction of the write.

**The dry run** takes a claim set and never a token; it issues nothing, records no `jti` and writes no event. A project
administrator's trace shows its tier only. It reads no policy, which the answer says when a `scope` item depends on one
([Q-CI-36](../spec/gitlab-ci-oidc.md)). **The snippet** holds the variables of the job and no token, and the revocation as its last step.

**Every write** records an event with the record before and after (`ci.mapping.*`, category `identity`, the project in the details);
`.updated`, `.disabled` and `.deleted` also enter the revocation journal with the mapping's ID, issuer, name, tier, priority and the
record before ([R-CI-020](../spec/gitlab-ci-oidc.md)). A PUT writes exactly one event, so a change that disables is not hidden behind
an `.updated`.

## The exchange: an anonymous endpoint

`POST /api/v1/auth/oidc/token` ([ci_exchange.go](../../internal/api/v1/ci_exchange.go),
[exchange_service.go](../../internal/ci/exchange_service.go)) takes no trove credential. The authentication stage answers the anonymous
principal for the route whatever the request carries, a session and a CSRF token play no part, and the general API rate limit does not
count it (`ExchangePath` in [ratelimit.go](../../internal/api/v1/ratelimit.go)). The subject token is the credential, so what an
anonymous caller can cause is bounded by the order of the checks: each step is cheaper than the next and refuses before it.

| Step | What an anonymous request costs here | The bound |
|---|---|---|
| the address budget `ci_exchange_address` | one counter in Valkey, before the body is read | 600 per minute and address, burst 100, cluster-wide; per-replica shares of it while Valkey is unavailable ([R-ARCH-034](../spec/architecture.md)); an address in `exempt_cidrs` skips it and is bounded by `server.max_in_flight` until its token is valid |
| the body and the parameters | `ci.exchange.max_body_bytes` (64 KiB), the content type, a parameter twice, the pure checks of the parameters | a `ttl` below `ci.token.min_ttl` and an empty `subject_token` are refused here ([Q-CI-41](../spec/gitlab-ci-oidc.md#q-ci-41--the-reason-of-a-request-that-cannot-succeed-by-its-own-parameters)) |
| the structure of the subject token | `ci.exchange.max_subject_token_bytes` (16 KiB), the JWS and JSON rules ([subject.go](../../internal/ci/subject/subject.go)) | no lookup |
| the issuer | the in-process copy of every issuer | an unknown `iss` costs at most one read of the issuers per replica and `cluster.epoch_poll_interval` ([above](#what-a-forged-kid-costs)) |
| the signature | the key lookup and at most 10 verifications | an unknown `kid` costs at most one row read and one fetch per interval and replica ([above](#what-a-forged-kid-costs)) |
| the subject budget `ci_exchange_subject` | one counter, per issuer and the first pin claim (`project_id`) | 300 per minute, burst 100, cluster-wide; a forged token never reaches it, so it cannot spend a project's budget |
| the selection | every tier of the issuer's mapping set, on the replica's copy, with the compiled conditions, short-circuit and no trace, before the transaction ([Q-CI-49](../spec/gitlab-ci-oidc.md#q-ci-49--what-bounds-the-cost-of-an-exchange)) | `ci.mappings.max_per_issuer` mappings, `ci.mappings.max_per_project` per project, the size bounds of a condition |
| the transaction | the reads `FOR SHARE`, the replay upsert, the token row, the issuance record | only for a validly signed token of a configured issuer within its project's budget |

**One transaction decides** ([exchange_tx.go](../../internal/ci/exchange_tx.go)). The selection is made on the replica's copy before it;
the transaction re-reads the issuer row and the matched mapping rows `FOR SHARE` and compares `version` and `mappings_version` with the
versions the copy was read at ([Q-CI-15](../spec/gitlab-ci-oidc.md)), which every mapping write moves under `FOR UPDATE` of the same
row, so equal versions mean the selection is the one an evaluation of the committed set would make. On a mismatch it validates the
token again against the re-read issuer (an audience removed a moment ago refuses the token), evaluates the mapping set it reads in the
same transaction and refreshes the copies, so a token reflects the configuration committed when it was issued whether or not a change
message reached the replica. A refused or failed transaction leaves no `jti`, no token and no record.
Single use, the restore cut-off and the `jti` are PostgreSQL's alone ([Q-CI-13](../spec/gitlab-ci-oidc.md)): concurrent exchanges of
one `jti` serialise on its row, and Valkey holds nothing but the two budgets' counters, so its loss or a forged entry can refuse
exchanges with 429 and cannot make trove accept one.

**What reaches the answer, the log and the audit trail.**

- The answer is the OAuth error response with `reason` and `requestId`. Its description names parameters, settings, claims by name and
  mappings, never a claim value, a parameter value or token material; the success answer is the one place a CI token appears. Both
  carry `Cache-Control: no-store` and `Pragma: no-cache`. With `ci.exchange.expose_reason: false` every 400 is `invalid_request` with a
  generic description ([H-420](#h-420--the-reason-of-a-refusal-tells-an-anonymous-caller-which-issuers-trove-trusts)).
- The log line of a refusal holds the reason, the issuer's name once it is resolved and the request ID; the request type prints without
  its tokens (`ExchangeRequest.LogValue`). The integration tests grep every answer and every log line of the replicas for the subject
  tokens they sent and for any `trv_cit_` value.
- `ci.exchange.succeeded` is written in the transaction with the token ID, the mappings, the allowlisted claim snapshot (no
  `user_email`), the scope and the lifetime; `token.created` beside it. `ci.exchange.denied` holds the reason and, before the signature
  is verified, only the unverified `iss` (cut to 256 bytes) and `kid` (at most 256 characters, R-CI-004), after it the snapshot. Denials
  are aggregated per replica, address, reason and minute ([exchange_denials.go](../../internal/ci/exchange_denials.go)): the first at
  once, the rest of the minute as one event with its count, the buffer bounded at 10,000 addresses
  ([H-419](#h-419--a-flood-from-many-addresses-writes-one-denial-row-per-address)). `ci.exchange.replayed` and `.repeated` carry the
  `jti` and its count.
- The metrics label an exchange with the issuer's name only once it was resolved from the configured set, and with a closed outcome
  and reason, so a forged `iss` adds no series.

**The scope** narrows what the token carries and is its ceiling at use time. What the text of the inline grants cannot decide (a policy
or group grant, a class or a format filter) the replica's grant index answers ([ci_scope.go](../../internal/authz/ci_scope.go)); the
index can lag a policy change by the bound of [R-AUTHZ-028](../spec/authorization.md), which can refuse a requested item or leave one
out of the answer's `scope` but never grants, because the policy grants of a CI token are decided live at use time. A key that names no
repository is `scope_not_granted`, with the description of every item the grants do not hold, so the answer tells nobody which
repositories exist ([H-418](#h-418--a-matched-job-learns-which-repository-keys-exist-through-its-scope)).

## What a CI token can do

A CI token (`trv_cit_`) carries the frozen bundle of its exchange in `api_token.ci_context` ([Q-CI-17](../spec/gitlab-ci-oidc.md)):
its issuer, its GitLab project, the authorization epoch of its exchange and, per contributing mapping, the tier, the project of a
project tier, the groups, the rendered inline grants, the caps and the end of its own window when its mapping's `maxTtlSeconds` is
shorter than the token's lifetime. The token validator decodes it with `claims.DecodeFrozen` into the token cache entry (version 2, and
version 1 of the tokens issued before); a bundle of another version or an inconsistent one is a storage fault and the request fails,
never an acceptance without it ([H-427](#h-427--a-replica-of-the-previous-release-refuses-the-ci-tokens-of-bundle-version-2)). The
evaluator ([internal/authz/ci.go](../../internal/authz/ci.go)) reads the bundle and the grant index and queries nothing
([R-AUTHZ-015](../spec/authorization.md)). A contribution counts until its own window ends, and only while its mapping is enabled with
an active principal in a grant index at least as fresh as the token's exchange (`ciContributes`,
[Q-CI-47](../spec/gitlab-ci-oidc.md#q-ci-47--a-project-tier-changes-only-what-its-own-tier-gives-a-token)). Per contribution, and for
that contribution only:

| Source | What it gives | The bound |
|---|---|---|
| policies naming the mapping (`ci:<issuer>/<mapping>` of the system tier, `ci:<issuer>/<projectKey>/<mapping>` of a project's tier) | their grants, live: removing the mapping from a policy ends the right on every replica within the bound of R-AUTHZ-028 | a project tier is granted by its project's own policies only |
| groups the mapping lists | the grants of those groups, resolved by name through the index, live | the system tier only; a project tier's groups are ignored ([Q-CI-26](../spec/gitlab-ci-oidc.md)) |
| inline grants | the grants rendered at the exchange, matched like a token scope's elements | a project tier's grants cover its project's repositories only, so `@any` is the project and a repository that leaves it is inert |
| the cap `read` | removes every other action of this contribution's three sources, and of no other contribution | [R-CI-012](../spec/gitlab-ci-oidc.md) |

The token's scope is the ceiling of the union, as for every token. A CI token never holds `manage` (not even through a policy or a
group that grants it), never a system role, no capability, no project role, no user identity, no `authenticated` and no
auto-join group ([R-AUTHZ-002](../spec/authorization.md)). The management API refuses it except for `GET` and
`DELETE /api/v1/tokens/current` and the repository-content operations (the gate `ciGate`, [Q-CI-18](../spec/gitlab-ci-oidc.md)), so it
can neither change an owner nor create a token nor read the issuance records.

## Package ownership of a GitLab project

An inline grant that writes carries `ownership: bind_project` unless it says `none`. A write such a grant allows is checked for
ownership in every local repository, also one whose `packageOwnership` is `off`, and its first publish claims the name for the owner
`ci:<issuer>:project:<project_id>`, stored by issuer ID and project ID ([R-CI-015](../spec/gitlab-ci-oidc.md),
[Q-CI-40](../spec/gitlab-ci-oidc.md)); the check and the claim run in the publish's transaction under the claim row's lock
([ownership_ci.go](../../internal/authz/ownership_ci.go)). The token's owner identities are its project, its mappings and the groups they
list; a mapping or a group is matched by its principal name when the index does not know its ID. Another GitLab project whose pattern
renders the same name gets `403 package_owned_by_other_owner` and an asynchronous `ci.package_ownership.conflict`; a name with versions
and no owner needs a person with `manage` to add the project as an owner. `ownership: none` makes the mapping the owner, so every
project it matches shares the name. The project path is a display value only, written by each allowed publish of the owning project, so a
project that later takes over a released path gains nothing.

## Revocation

| Cause | Who | Row reason | Events |
|---|---|---|---|
| a mapping disabled or deleted (W3 calls the revoker): the tokens it owns, for a system mapping also those whose record lists it | `ci:write`, the project's administrators | `mapping_deleted` | `ci.token.revoked` cause `mapping` |
| an issuer disabled, deleted, or removed from `ci.issuers` | `ci:write`, `trove migrate` | `admin` | `ci.token.revoked` cause `issuer` |
| `POST /api/v1/ci/issuances/revoke` | `tokens:revoke` | `admin` | `ci.token.revoked` cause `bulk`, with the filter |
| `DELETE /api/v1/tokens/{id}` | `tokens:revoke` | `admin` | `token.revoked`, `ci.token.revoked` cause `single` |
| `DELETE /api/v1/tokens/current` | the token itself | `owner` | `token.revoked`, `ci.token.revoked` cause `self` |

Every cause ends its tokens in the transaction that decides it ([revoke.go](../../internal/ci/revoke.go)), records the change of the area
`auth` with the token IDs and hashes, and writes `ci.token.revoked` with at most 1,000 IDs per event and its journal entry
([Q-CI-38](../spec/gitlab-ci-oidc.md)). The change-log poll applies it on every replica within `auth.revocation_bound` (2 s) whatever
Valkey does; the integration tests measure it on three replicas with Valkey running and stopped. A system mapping's revocation also
finds, through their issuance records, the tokens it contributed to (it owns each of them). A project mapping's revocation ends only
the tokens it owns; its contribution to a token of another tier is not revoked but ignored by the evaluator once the grant index no
longer holds the mapping as active, within the index's bound
([H-425](#h-425--a-project-mappings-end-reaches-the-tokens-of-other-tiers-within-the-index-bound-not-the-revocation-bound)). The
statements read the unexpired issuance records only, through the expiry index, so a revocation under the issuer row's `FOR UPDATE`
costs the tokens of the last `ci.token.max_ttl`, not the records of the retention. An issuance record outlives its token
(Q-CI-20); once the token row is gone it reads as `expired` after the record's expiry and as `removed` before it (the row went with the
principal of a deleted mapping), and in both cases as not active ([Q-CI-52](../spec/gitlab-ci-oidc.md)). A project Owner's view of a
record shows its own mappings and the job's identity only: the scope strings (they list every tier's items), the person who started the
pipeline, the runner and the client address are left out of the record and of its claim snapshot.

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
today for every mapping that lists a policy or a group, for holders of `policies:write` and for the Owners of a project on
its policies. What bounds it: a policy entry of a mapping never holds `manage`, a write is still
decided by the package ownership of the repository ([R-CI-015](../spec/gitlab-ci-oidc.md)), and a project policy reaches the
project's repositories only. The widening is visible in the policy's own event, not in the mapping's.

### H-414 · A binding removed by a mapping save is not in the policy's own journal

A save that removes a mapping from a policy, and the delete of a mapping, change the policy's principal list in the mapping's
transaction. The policy's version moves and the change area `authz` is notified, but the policy's own events (`authz.policy.updated`)
and its entry in the revocation journal ([R-AUTHZ-031](../spec/authorization.md)) are not written; the mapping's event carries the
policies in its before and after record, and its journal entry has the record before the change. Live whenever a PUT or a DELETE of
a mapping removes an entry. After a point-in-time restore the policy's removed entry is restored with its rows and only the mapping's
journal entry (a disable, never a delete, [R-CI-020](../spec/gitlab-ci-oidc.md)) is replayed; a reader of the policy audit trail does not
see the removal.

### H-415 · The inline grants of an issued token survive a narrowing of its mapping

Component: the frozen bundle (R-CI-013, R-CI-017). Subject: a CI token issued before an administrator narrows the mapping's inline
grants, for example after noticing that a template renders a pattern that is too wide. Live: the
token keeps the wide grant until it expires, at most `ci.token.max_ttl` (1 h by default), because editing a mapping revokes nothing.
What makes it real: a mistaken grant that a job exploits within that hour. The bulk revoke by `mapping` ends those tokens at once, and
the manual says so; nothing does it automatically.

### H-416 · The usage summary of an issuance record shows what the audit store holds, not every use

Component: `GET /api/v1/ci/issuances/{tokenId}` (R-CI-020). Subject: the request count, the last use and the packages deployed of a
token. The count is the audit events whose credential is the token within its lifetime: writes, denials and decisions, but an allowed
read only while `authz.audit.reads` records it, which the default does not. The packages are those of `npm.publish` and
`package.published`; a file deployed through `/files/` is in the audit store as `artifact.deployed` but not in the list. Live today. What
makes it real: an incident review that reads the summary as complete; the audit log filtered by `tokenId` is the complete source.

### H-417 · A group created again under a listed name grants its rights to the tokens of the mappings that listed the old one

Component: the group grants of a CI token (R-AUTHZ-014, R-CI-013). Subject: a mapping that lists a group by name, and a group that is
deleted and created again under that name. A mapping names its groups by name in its grant specification, and the evaluator resolves
the name through the grant index at use time, so the new group's policies apply to the tokens of the mapping, issued before or after
the re-creation; [R-AUTHZ-002](../spec/authorization.md) promises that a principal created later under a reused name inherits nothing,
which holds for policies, role bindings and owners but not for a mapping's group list. Dormant until the mapping store (W3) saves
mappings with groups; live then for every system-tier mapping that lists one. What makes it real: an administrator who re-creates a
deleted group for another team.

### H-418 · A matched job learns which repository keys exist through its scope

Component: the `scope` parameter of the exchange (R-CI-007). Subject: a job whose token matches a mapping and that sends
`scope=read:<key>` for keys it is not granted.

The earlier gap is closed ([Q-CI-51](../spec/gitlab-ci-oidc.md#q-ci-51--what-the-exchange-tells-a-caller-and-when-a-replica-learns-an-issuer),
[exchange_scope.go](../../internal/ci/exchange_scope.go)): a key that named no repository answered `scope_malformed` and one that exists
and is not granted `scope_not_granted`, so the job learnt whether a repository of that key exists, which `authz.hide_existence` hides
from every other caller. It was wider than written: every project Owner who controls a pipeline can save a mapping of its tier
that matches it, so the oracle reached every repository key of the installation from any project, not only from the jobs of a system
mapping. Now an item whose key names no repository is held by no grant and answers `scope_not_granted` with the same description as
every item the grants do not hold.

What stays open: an item answers whether the token's own grants hold it, which its mapping's author knows already; nothing is told of a
repository the grants do not cover.

### H-419 · A flood from many addresses writes one denial row per address

Component: the aggregation of `ci.exchange.denied` (R-CI-021). Subject: an attacker who sends refused requests from many source
addresses, an IPv6 prefix or a botnet. The aggregation is per address and reason, so each new address writes its first denial at once:
up to 10,000 rows per replica, reason and minute before the bounded buffer counts further addresses under the reason alone, and each of
the first rows may carry up to 256 bytes of a chosen `iss` and a chosen `kid`. Live today. What bounds it: the per-address budget
before the body is read, the 10,000 keys of the buffer, the audit buffer that drops and counts
(`trove_audit_events_dropped_total`) when it is full, and the retention of the audit store.

### H-420 · The reason of a refusal tells an anonymous caller which issuers trove trusts

Component: the error answer of the exchange (R-CI-008). Subject: an anonymous caller that sends tokens with chosen `iss` values, or a
validly signed token of a public issuer. With `ci.exchange.expose_reason: true`, the default, `unknown_issuer` tells an `iss` that trove
does not trust from one it does (`signature_invalid`, `unknown_signing_key`), and for a token of gitlab.com `no_matching_mapping` tells
whether a mapping matches its claims. Live with the default. What bounds it: the per-address budget; `expose_reason: false` answers every
400 with `invalid_request` and keeps the reason in the log and the audit trail, at the cost of the diagnosis a pipeline prints.

### H-424 · A project mapping that owns a token ends the other projects' contributions with it

Component: the owner of a CI token ([ttl.go](../../internal/ci/ttl.go) `tokenOwner`, R-CI-009 step 4, Q-CI-47). Subject: a job that no
system mapping matches and the mappings of two or more projects do. The token belongs to one principal: the system tier's mapping when
it contributes, otherwise the project contribution that lives longest. Disabling or deleting that project's mapping revokes the token,
and with it the contributions of the other projects, because the token row belongs to the owner's principal (its status refuses the
token, its deletion deletes the row). Live today. What makes it real: two projects whose mappings match the same pipelines, and an
administrator of the owning project who toggles its mapping; the job loses its token mid-run and exchanges again, which gives it the
other projects' grants under a new owner. Nothing a project grants reaches another project's repositories either way (Q-CI-26), so the
cost is availability, not access.

### H-425 · A project mapping's end reaches the tokens of other tiers within the index bound, not the revocation bound

Component: the contributions of a CI token at use time ([internal/authz/ci.go](../../internal/authz/ci.go) `ciContributes`,
R-CI-017, Q-CI-47). Subject: a token another tier owns, to which a project mapping contributed, after that mapping was disabled or
deleted. The token is not revoked; the evaluator ignores the contribution once the replica's grant index, rebuilt from the change area
`authz` that the mapping write notified, no longer holds the mapping as active. That takes effect within the bound of
[R-AUTHZ-028](../spec/authorization.md) (typically 100 ms plus a build, at most `cluster.epoch_poll_interval` plus a build with a working
poll, never more than `authz.cache.max_staleness` plus a build, about 10.5 s), not within `auth.revocation_bound` (2 s), and no
`ci.token.revoked` names those tokens. After a point-in-time restore the mapping's `.disabled` or `.deleted` journal entry is replayed,
which disables the principal again, and the next index drops the contribution. Live today for every token in which a project mapping
contributes beside another tier. What makes it real: a project Owner who disables a mapping to stop a leaked token's project
grants and expects the 2-second bound; the bulk revoke by `mapping` (R-CI-020) ends such tokens at once, the other tier's grants with
them.

### H-426 · Several projects together can fill an issuer's mapping limit

Component: the mapping limits (R-CI-009, Q-CI-49). Subject: an issuer on which many projects save mappings. `ci.mappings.max_per_project`
(100) keeps one project from filling `ci.mappings.max_per_issuer` (1,000), but ten projects at their quota fill it, and the next save of
any tier, a system administrator's included, answers `422 mapping_limit_reached`. Live when many projects use one issuer. What bounds
it: project Owners are named by the administrators of trove or by other Owners, the quota is a configuration key, and every mapping is listed
with its project.

### H-427 · A replica of the previous release refuses the CI tokens of bundle version 2

Component: the frozen bundle (Q-CI-17, Q-CI-47). Subject: a CI token issued by a replica of this release and used on a replica of the
release before it, during the rolling update between them. The previous release reads bundle version 1 only and treats version 2 as a
storage fault, so it refuses the token; it cannot honour a contribution's own expiry, and ignoring it would grant a project's
contribution beyond its bound. Live during that one update and dormant afterwards. What makes it real: a job whose exchange and whose
package requests reach replicas of different releases while the update rolls; it fails and runs again. The reverse direction works:
this release reads version 1.

### H-565 · A replayed disable of a configuration-declared CI issuer is undone by `trove migrate` when the restore runs with an older configuration

Stage: the apply of `ci.issuers` in `trove migrate` (R-CI-001, Q-CI-16; `ApplyDeclared` in
[issuers_declared.go](../../internal/ci/issuers_declared.go)), which the chart's migration Job runs at every `helm install` and
`helm upgrade`, so also at the final `helm upgrade` of the restore sequence that starts the replicas again
([ha-and-helm.md](../spec/ha-and-helm.md) R-HA-060), with the values file given there. It runs after the journal replay of
`trove backup restore` (R-BAK-024) or `trove admin post-restore` (R-HA-025), which disables the issuer
([journal_replay.go](../../internal/ci/journal_replay.go)).

Subject, case by case:

- **A declared issuer disabled after the restore point.** The API refuses to write an issuer with source `configuration`, so such a
  disable came from the configuration itself. The replay disables the restored row and revokes its unexpired tokens. A configuration
  that still declares the issuer enabled, or omits `enabled` (the default is `true`), makes the next `trove migrate` write the
  declaration over the row: the issuer is enabled again (`ci.issuer.enabled`, actor `system:configuration`) and its jobs obtain CI
  tokens again. The tokens the replay revoked stay revoked.
- **A declared issuer that the replay suspended** because it could not prove a `ci.issuer.updated` entry a narrowing. `trove migrate`
  writes the declared settings over the suspension, whatever they are: the after-state of the narrowing when the configuration is the
  one in force at the restore, the wider settings of an older one.
- **A declared issuer the configuration no longer lists** is disabled again by the same step (`removeOne`), so the replay's disable
  holds.

Confirmed in the code, not run as this case: `applyOne` builds the issuer from the declaration and, when the stored settings differ
(`sameSettings`), writes it with `writeUpdate`, which enables a disabled row. `TestR_CI_001_DeclaredIssuersAreReadOnlyAndFollowTheConfiguration`
shows the same path enabling a declared issuer that was disabled, through a removal and a declaration again; no test runs a restore
followed by `trove migrate`.

Live or dormant: dormant while the restored installation is deployed with the configuration in force when the issuer was disabled,
because that configuration declares it disabled or not at all. Live when the restore is deployed with the values of the backup's time
or earlier: a disaster recovery into a new cluster from values kept with the backup, or a GitOps revert of the values to the restore
point. What makes it real: the reason for the disable still holding, a compromised GitLab instance or a leaked signing key, together
with such a configuration. The migration Job's audit trail records the enable as `ci.issuer.enabled` by `system:configuration`.
