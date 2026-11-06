# GitLab CI OIDC federation

Spec [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) (R-CI-001 to R-CI-030), beta phase 5 (M4). The page is the contract between the work packages
that build the federation in parallel, in the shape of [projects.md](projects.md). The decisions behind it are
[Q-CI-17 to Q-CI-22, Q-CI-26, from W2 Q-CI-27 to Q-CI-29, from W3 Q-CI-35 to Q-CI-37, from W5 Q-CI-38 to Q-CI-40, from W4 Q-CI-41 to Q-CI-43 and, from the fix wave A of the security audit, Q-CI-47 to Q-CI-51](../decisions.md) with [ADR-147](../decisions.md#adr-147--a-ci-token-carries-its-frozen-grants-in-its-cache-entry-and-the-management-api-refuses-ci-tokens-by-default).

Status: built. W1 (issuers and keys, [below](#w1-issuers-and-signing-keys)), W2 (the claim engine, the subject-token
validation and the shared JWS leaf, [below](#w2-the-claim-engine-and-the-validation)), W3 (the mappings, the dry run and the snippet,
[below](#w3-the-mappings)), W4 (the token exchange, [below](#w4-the-exchange)), W5 (CI tokens at use time, [below](#w5-use-time)) and W6
(the admin UI) and W7 ([below](#w7-the-end-to-end-tests-on-kind)) are built. The issuer double the tests exchange against is
test/oidcstub.

## What has to be in your head

- **Two kinds of work, two kinds of package.** The pure parts (conditions, glob, templates, grants, the scope grammar, the save-time rules, the
  validation of a subject token) are leaves with no I/O ([claims](../../internal/ci/claims), [subject](../../internal/ci/subject),
  [reason](../../internal/ci/reason)); the exchange, the dry run and a save evaluate one implementation. The services are in
  [internal/ci](../../internal/ci) behind interfaces, wired in [wire_ci.go](../../internal/app/wire_ci.go): every role builds the
  issuers, the mappings, the revoker and the issuances (step `wireCI`), a role with a client listener the exchange (step
  `wireCIExchange`, area `ci_exchange`, which takes the two limiters of the limits area).
- **PostgreSQL decides.** Issuers, mappings, key sets, the replay set and the issuance records are rows; replicas hold in-process copies
  invalidated through the areas `ci_mappings` and `ci_issuer_keys`; the exchange re-reads issuer and mappings `FOR SHARE` and compares
  `version` and `mappings_version` (Q-CI-15); Valkey holds only the two limiters' counters (R-CI-029).
- **A CI token carries its frozen grants** (Q-CI-17): `claims.Frozen` is written to `api_token.ci_context` by the exchange, read into the token
  cache entry, and travels `token.Info.CI` → `auth.Result.CI` → `httpserver.Principal.CI` → `authz.Subject.CI`. It is per contributing mapping
  (Q-CI-17 as amended 2026-11-02): `Frozen.Contributions` holds one `claims.FrozenMapping` per tier that matched, with its tier, its project
  (key and ID) for a project tier, its groups, its inline grants, its caps and, since version 2, its own `ExpiresAt`; a cap bounds only its own
  mapping's grants (R-CI-012). Policy and group grants stay live through the principal set (`Subject.Mappings`, each contribution's `Groups`). The
  JSON is versioned (`claims.FrozenVersion` 2, with `AuthzEpoch`); `claims.DecodeFrozen` checks it and still reads version 1 (Q-CI-47).
- **Each contribution is its tier's** (Q-CI-47, [below](#fix-wave-a-tier-isolation-and-issuer-hardening)). The token belongs to the system tier's
  contributing mapping, else to the longest-lived project contribution (`tokenOwner`); a project's render failure, `max_ttl_seconds` and disable or
  delete reach its own contribution only, and the evaluator drops a contribution after its `ExpiresAt` and once an index at least as fresh as the
  bundle's `AuthzEpoch` no longer holds its mapping as active (`Index.CIGrants`).
- **The management API refuses CI tokens by default** (Q-CI-18): `ciGate` in [authorize.go](../../internal/api/v1/authorize.go); only public operations,
  the action form and `x-trove-ci: admitted` (`GET` and `DELETE /tokens/current`) admit one. Package protocols and `/files/…` are outside that table.
- **Tiers.** First match inside a tier, union across tiers (R-CI-009). The tier `system` is the administrators'; a project's Owners manage the tier
  `project:<key>` (Q-CI-26), bounded like project policies: inline grants over the project's repositories (the contribution's `Project` makes the evaluator
  compile them against those only), the project's own policies, no groups, never `manage`. A mapping is named within its issuer's tier: `ci_mappings.name` is
  the bare name under `UNIQUE (issuer_id, tier, name)`, the principal is `<issuer>/<name>` for the system tier and `<issuer>/<projectKey>/<name>` for a project's
  (Q-AUTHZ-214; `Mapping.PrincipalName`, `claims.MappingRef(issuer, project, name)`), and the exchange and the issuance filters write a project's mapping as
  `<projectKey>/<name>` (`Mapping.ShortName`). Issuers are the system administrators' and the Helm values' (`ci.issuers`, Q-CI-16); a project Owner lists them
  read-only.
- **One selector grammar.** `claims` has no grammar of its own: `claims/selector.go` reads every selector with `qname.ParseSelector` in a context
  (R-CI-007, Q-AUTHZ-216). The scope items of the exchange and the grants of a system mapping are instance selectors (`<projectGlob>/<keyGlob>[:<format>]`,
  `@any…`); the grants of a project mapping are bare keys of the project. A bare key in an instance context is `scope_malformed` in the exchange and
  `invalid_pattern` at the save of a mapping, a qualified one in a project's mapping `invalid_pattern`; a template is read with a stand-in for each
  substitution (`Template.Skeleton`), so a template of the wrong shape is refused at the save. `Freeze` writes the selectors of a project mapping in their instance
  form (`instanceForm`: `npm-*` becomes `web/npm-*`, `@any` becomes `web/*`), so the token carries one grammar and the engine and `NarrowFrozen` read it alike.
- **Stored JSON is the wire JSON.** `ci_mappings.match`, `.grants`, `.options` hold the lowerCamelCase forms of the `claims` types.
- **No attribution, no vendor names** in anything the work packages write; the decider of design questions is "the development agent".

## The contracts

### Schema (migrations 00026 to 00031)

| File | What |
|---|---|
| [00026_ci_oidc.sql](../../internal/meta/postgres/migrations/00026_ci_oidc.sql) | `ci_issuers`, `ci_issuer_keys`, `ci_mappings` (with `tier`, `project_id`, the bare `name` unique per `(issuer_id, tier)`), `ci_subject_token_uses`, `ci_token_issuances`; the epoch rows of `ci_mappings` and `ci_issuer_keys`; `package_owner.ci_issuer_id → ci_issuers` `NOT VALID` |
| [00027](../../internal/meta/postgres/migrations/00027_ci_oidc_owner_index.sql), [00028](../../internal/meta/postgres/migrations/00028_ci_oidc_validate.sql) | the index `CONCURRENTLY`, the validation (R-HA-018) |
| [00029](../../internal/meta/postgres/migrations/00029_ci_issuances_issued_index.sql), [00030](../../internal/meta/postgres/migrations/00030_ci_issuances_issuer_index.sql), [00031](../../internal/meta/postgres/migrations/00031_ci_issuances_contributions_index.sql) | the three indexes of the issuance statements, each its own `CONCURRENTLY` step (Q-CI-53): `(issued_at DESC, token_id DESC)`, `(issuer_id, issued_at DESC, token_id DESC)`, a GIN on `grants -> 'contributions'`; none on `audit_event` (it is partitioned) |
| [tables_ci.go](../../internal/meta/postgres/tables_ci.go) | export classes (`ci_issuer_keys` transient, the rest data) and the revision columns `ci_issuers.version`, `.mappings_version`, `ci_mappings.version` |

Deviations from the spec's data models are Q-CI-20 and Q-CI-26: no key on `ci_token_issuances.token_id`, `ON DELETE CASCADE` on its issuer, `project_id` on mappings.
Queries (one file per work package; add yours there, `make generate`): [ci_issuers.sql](../../internal/meta/postgres/queries/ci_issuers.sql) (W1),
[ci_mappings.sql](../../internal/meta/postgres/queries/ci_mappings.sql) (W3), [ci_exchange.sql](../../internal/meta/postgres/queries/ci_exchange.sql) (W4),
[ci_issuances.sql](../../internal/meta/postgres/queries/ci_issuances.sql) (W5). Plain Go types only (no `pgtype`): nullable columns are `COALESCE`d, optional
values are `NULLIF(text, '')` or `0`. [TestCIMigration](../../test/integration/postgres/ci_test.go) runs each statement against the schema.

### Packages

| Where | What |
|---|---|
| [internal/ci/reason](../../internal/ci/reason/reason.go) | every `reason` of R-CI-008 (`Reason.OAuth()` gives status and OAuth `error`), every management `code`, the warnings |
| [internal/ci/claims](../../internal/ci/claims) | `Claims` (normalization, `CrossProject`, `Snapshot`), `Node` (parse, `Validate`, `Evaluate` with `LeafResult` trace, `Pins`), `GlobValid`, `GlobMatch`, `Template`, `Spec`, `InlineGrant`, `Rendered`, `ApplyCaps`, `Options`, `Frozen`, `FrozenMapping`, `Freeze`, `DecodeFrozen`, `ParseScope`, `Narrow`, `NarrowFrozen`, `FormatScope`, `Check` (save-time rules; `IssuerFacts.Project`) (built, W2) |
| [internal/ci/subject](../../internal/ci/subject/subject.go) | `Parse` (steps 1, 2, 3 shape, 4), `CheckIssuer` (step 5 for the caller's lookup) and `Unverified.Verify` (3 alg and typ, 6 to 9); `Settings`, `Key`, `KeySource` (the unknown-`kid` protocol is the key source's), `Failure` (built, W2) |
| [internal/ci](../../internal/ci) | types (`Issuer`, `IssuerInput`, `KeyStatus`, `Mapping`, `MappingInput`, `MappingOptions`, `MappingSet`, `Issuance`, `IssuanceFilter`, `EvaluateRequest`, `EvaluateResult`), the interfaces `Issuers` (W1), `Mappings` (W3), `Exchanger` (W4, built: `ExchangeService`), `Revoker`, `Issuances` (W5, built: `TokenRevoker`, `IssuanceStore`), `Select`, `ComputeTTL`, `Metrics`, `DeclaredFromConfig`; `NotImplemented` for a service option left nil |
| [internal/auth/jws](../../internal/auth/jws) | the compact-JWS verifier and JWK sets the login, the key store and the CI validation share (Q-CI-19, Q-CI-42): `Parse`, `Token.VerifyWith`, `Fits`, `ParseJWKS` with the report of ignored keys (the one implementation of R-CI-003's key rules), `SelectKey` (the login's), `Candidates` (the key store's), `DecodeObject` (also the exchange's JSON body) (built, W2) |

### Token and evaluation seam (the part W4 and W5 share)

| Step | Where |
|---|---|
| W4 builds the bundle | `Frozen{Version: claims.FrozenVersion, Issuer, IssuerID, ProjectID, ProjectPath, CrossProject, AuthzEpoch}` (the epoch of `CiExchangeClock`), then `Frozen.Add(claims.Freeze(ref, id, tier, project, rendered, caps))` per contributing mapping in evaluation order (`Freeze` bounds the inline grants by the mapping's caps), then `claims.NarrowFrozen` with the request's `scope` items, then `ExpiresAt` = the clock's `now()` plus the window of `ComputeLifetime` for every contribution whose window is shorter than the token's; `Frozen.Check` before the write |
| W4 issues the token in the exchange transaction | `token.Service.IssueCITx(ctx, tx, token.CITRequest{OwnerID: the owner's principal (tokenOwner), Frozen, Scope, TTL, Claims, Source})` in [cit.go](../../internal/auth/token/cit.go) (built by W5): `created_via` `ci-exchange`, the owner read `FOR SHARE` and checked active and of kind `ci_mapping`, the expiry the database clock plus `TTL` (1 s to `auth.tokens.cit.max_ttl`), `Frozen.Check` before the write, `token.created` with `ciIssuer`, `ciMappings`, `ciClaims`; no user token list, no per-principal limit; the issuance record is written beside it (`CiExchangeInsertIssuance`) with `grants` = the same bundle and `expires_us` = `CITIssued.ExpiresAt` |
| the validator reads it | `Service.load` decodes `ci_context` into `Info.CI` for type `cit` with `claims.DecodeFrozen` (built, W5): a missing bundle, another version and an inconsistent bundle are storage faults, never an acceptance |
| the stage and the chain pass it on | `auth.Result.CI`, `Principal.CI` and `Principal.Mappings` ([m1_convert.go](../../internal/app/m1_convert.go)), `authz.Subject.CI` ([edge/subject.go](../../internal/api/edge/subject.go)) (done) |
| W5: the evaluator (built) | [authz/ci.go](../../internal/authz/ci.go): `Index.CIGrants(sub)` resolves the contributions against the index and leaves out those `ciContributes` refuses (ended, or of a mapping the index no longer holds as active, Q-CI-47), `ciGrantedBy` in `allowedAction`, `ciReadShape` in `readShape`, `Subject.CIProject()`; [below](#w5-use-time) |
| W5: ownership (built) | [ownership_ci.go](../../internal/authz/ownership_ci.go): the owner operations accept `ci:<issuer>:project:<id>` (the issuer by name), `ownerOfRow` shows the issuer name, `keyFor` and `ciFacts` add the project identity, the check of a `bind_project` write and its claim, `ci.package_ownership.*` |
| W5: revocation (built) | [revoke.go](../../internal/ci/revoke.go): `Revoker` (mapping, issuer, bulk), the reasons of the token table (Q-CI-38), `ci.token.revoked` with the journal entry; the token service adds the causes `single` and `self` |

### API

[ci_services.go](../../internal/api/v1/ci_services.go) holds `CIServices`; one handler file per work package:
[ci_issuers_api.go](../../internal/api/v1/ci_issuers_api.go) (W1), [ci_mappings_api.go](../../internal/api/v1/ci_mappings_api.go) and
[ci_snippets_api.go](../../internal/api/v1/ci_snippets_api.go) (W3), [ci_exchange.go](../../internal/api/v1/ci_exchange.go) (W4),
[ci_issuances_api.go](../../internal/api/v1/ci_issuances_api.go) (W5), [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (the project routes of the
tier `project:<projectKey>`, the work behind them W1, W3, W5). Declarations: `ci:read` and `ci:write` (recent authentication on create, replace, delete),
`tokens:read` and `tokens:revoke` (Q-CI-22); the project routes add `project: path.projectKey` and `projectRole: owner`; the exchange is `public: true` with `security: []`.

| Operation | Path | Package |
|---|---|---|
| issuers | `GET/POST /ci/issuers`, `GET/PUT/DELETE /ci/issuers/{name}`, `GET …/keys`, `POST …/keys/refresh` | W1 |
| mappings, dry run, snippet | `…/ci/issuers/{issuer}/mappings[/{name}]`, `POST …/evaluate`, `GET …/snippets/{kind}` | W3 |
| issuances | `GET /ci/issuances[/{tokenId}]`, `POST /ci/issuances/revoke` | W5 |
| exchange | `POST /api/v1/auth/oidc/token` | W4 |
| a project's | `/projects/{key}/ci/issuers`, `…/ci/mappings[/{issuer}/{name}]`, `…/ci/issuers/{issuer}/evaluate`, `…/ci/issuances` | W1, W3, W5 |

The exchange needs no work in the stage (it answers the anonymous principal for the route, `isExchangeRoute`); the general API rate limit
skips the route (`ExchangePath` in `limitedPath`, [ratelimit.go](../../internal/api/v1/ratelimit.go)), which has the limiters of R-CI-021 of
its own, and every answer but the 404 of a switched-off exchange and the router's 405 is in the OAuth shape ([below](#w4-the-exchange)).

### Configuration, coordination, audit

| Where | What |
|---|---|
| [config/ci.go](../../internal/config/ci.go) | every `ci.*` key with its default; refusals: `ci.token.max_ttl` above `auth.tokens.cit.max_ttl` (both keys named), `ci.mappings.max_per_project` above `ci.mappings.max_per_issuer`, `ci.replay.retention_after_expiry` below 1 m, a declared issuer's `max_ttl_seconds` outside `ci.token.min_ttl` to `ci.token.max_ttl`, the retired `ci.exchange.rate_limit.per_issuer_per_second` (file and environment, names the replacements), `ci.job_token_fallback.enabled`; `ci.issuers` (list elements now hold pointers to scalars and string lists, [schema.go](../../internal/config/schema.go)); `trove migrate` calls `ApplyDeclared` always ([migrate_declared.go](../../internal/app/migrate_declared.go)) |
| [cluster/areas.go](../../internal/cluster/areas.go) | `ci_mappings`, `ci_issuer_keys` |
| [app/wire_ci.go](../../internal/app/wire_ci.go) | the services (`wireCI`), the exchange (`wireCIExchange`, a component whose flush writes the denial counts at shutdown), limiters `ci_exchange_address` and `ci_exchange_subject` (cluster scope, held in `limitsArea`), tasks `ci-keys-refresh` `*/5 * * * *`, `ci-replay-cleanup` `7 * * * *`, `ci-issuance-retention` `40 3 * * *` (exclusive; a not-implemented answer is a success), the metrics and the two database gauges |
| [audit/events_ci.go](../../internal/audit/events_ci.go) | the events of R-CI-020, categories and modes in Q-CI-22; journal flag on `ci.token.revoked` and the `.updated`, `.disabled`, `.deleted` events of issuers and mappings; the record schema has `project` |
| [errs/codes.go](../../internal/errs/codes.go) | `>>> M4 ci`: `ci_not_implemented` and the management codes |

## Work packages

| Package | Builds | Files it owns |
|---|---|---|
| **W1** issuers and keys (built) | R-CI-001 to R-CI-003, R-CI-005, Q-CI-16; the issuer, key and `Choices` operations; `ApplyDeclared` (migrate: call it always); `ci-keys-refresh`; the key source | `internal/ci/issuers*.go`, `keys*.go`, `ci_issuers_api.go`, `queries/ci_issuers.sql`, the issuer part of `wire_ci.go`, `migrate_declared.go` |
| **W2** claim engine and validation (built) | R-CI-004, R-CI-010 to R-CI-014, the save-time rules and warnings; `internal/auth/jws`; [what it built and the contract changes](#w2-the-claim-engine-and-the-validation) | `internal/ci/claims/**`, `internal/ci/subject/**`, `internal/auth/jws/**`, `internal/auth/oidc/jws*.go` |
| **W3** mappings (built) | store, principals, `mappings_version`, R-CI-009, the system and the project routes, the project bounds, the dry run, `Select`, the snippet; `project_not_empty` for a project with mappings; [what it built and the contract changes](#w3-the-mappings) | `internal/ci/mappings*.go`, `select.go`, `ci_mappings_api.go`, `ci_snippets_api.go`, `queries/ci_mappings.sql`, `internal/project` (the delete check) |
| **W4** exchange (built) | R-CI-007, R-CI-008, R-CI-016, R-CI-018, R-CI-021, the issuance write; calls `IssueCITx`; [what it built and the contract changes](#w4-the-exchange) | `internal/ci/exchange*.go`, `ttl.go`, `ci_exchange.go`, `queries/ci_exchange.sql`, the exchange part of `wire_ci.go`, `internal/authz/ci_scope.go` |
| **W5** use time (built) | R-AUTHZ-014 in the evaluator, R-CI-015 ownership, R-CI-017 revocation, issuances (also a project's), bulk revoke, the two retention tasks, `trove_ci_tokens_active`; `IssueCITx` (moved from W4); [what it built](#w5-use-time) | `internal/authz/ci.go`, `ownership_ci.go` and the CI parts of `ownership.go`, `internal/ci/revoke*.go`, `issuances*.go`, `internal/auth/token/cit.go`, `ci_issuances_api.go` and the issuance part of `ci_project_api.go`, `queries/ci_issuances.sql` |
| **W6** admin UI (built) | R-UI-035, R-UI-036, the issuances tab, the project's CI screens (see [web-ui.md](web-ui.md#ci-identity)); the browser journey `13-ci` is W7's | `web/**` |
| **W7** tests and e2e (built) | the acceptance criteria of R-CI-001 to R-CI-030 against the issuer double (test/oidcstub) | `test/integration/ci/**`, `test/e2e/**` |

Order: W2 first (pure), then W1 and W3, then W4, then W5 and W6; W7 throughout.

### The manual pages

The behaviour is documented for users while it is built (owner, 2026-11-02): [admin/gitlab-ci.md](../manual/admin/gitlab-ci.md) (issuers,
the Owner's how-tos, dry run, issuances, revocation, every refusal reason, rate limits, the `ci.*` keys) and
[user/gitlab-ci-pipelines.md](../manual/user/gitlab-ci-pipelines.md) (`id_tokens:`, the exchange, `scope`, npm and NuGet, the closing
revocation). A work package that changes a documented path, field, code, default or rule updates the page in the same change.
The notes "Not available in this version yet" of both pages and the "not released yet" wording of [tokens.md](../manual/admin/tokens.md),
[permissions.md](../manual/admin/permissions.md), [projects.md](../manual/admin/projects.md), [limitations.md](../manual/admin/limitations.md),
[configuration.md](../manual/admin/configuration.md), [client-setup.md](../manual/user/client-setup.md) and [getting-started.md](../manual/user/getting-started.md)
were removed by W7, which shipped the exchange; the admin page has a section for the screens (**In the administration area**). The open point on pinning a group with subgroups
(admin page, "Groups with subgroups") is updated with the result of the check against a real GitLab (Q-CI-25).

## Tests that pass now

These contract tests carry their requirement IDs in comments, not in their names: they pin the contract and are no
evidence that a requirement is implemented (the coverage generator counts a named passing test as evidence, Q-ROAD-5);
the evidence comes from the tests of each work package.

| Test | What it pins |
|---|---|
| `TestACITokenIsRefusedOnTheManagementAPIByDefault`, `TestOnlyTheCurrentTokenOperationsAdmitACIToken` ([ci_gate_test.go](../../internal/api/v1/ci_gate_test.go)) | the gate |
| `TestTheCISectionDefaults`, `TestTheLifetimeCeilingIsOne`, `TestThePerIssuerBucketIsRetired`, `TestTheJobTokenFallbackIsRefusedByName` (R-CI-022, P2, refused), `TestIssuersFromTheConfiguration` ([ci_test.go](../../internal/config/ci_test.go)) | defaults, refusals, declared issuers |
| `TestCIMigration` ([test/integration/postgres/ci_test.go](../../test/integration/postgres/ci_test.go)) | schema, classes, areas, every CI statement |
| `TestDeclaredCIIssuersReachTheMigrationJob` ([test/chart/ci_test.go](../../test/chart/ci_test.go)) | `config.ci.*` reaches the migration Job and the replicas |
| the document tests of [internal/api/v1](../../internal/api/v1) | declarations, recent authentication, reserved names, problem responses |

## W1: issuers and signing keys

Built in [internal/ci](../../internal/ci) (`issuers*.go`, `keys*.go`), [ci_issuers_api.go](../../internal/api/v1/ci_issuers_api.go),
[ci_issuers.sql](../../internal/meta/postgres/queries/ci_issuers.sql), [wire_ci.go](../../internal/app/wire_ci.go) and
[migrate_declared.go](../../internal/app/migrate_declared.go). The wiring is the step `wireCI` (area `ci`, every role): W3 to W5 replace
their member of `ciArea` there.

| What | Where |
|---|---|
| validation and defaults of an issuer (pure) | [issuers_validate.go](../../internal/ci/issuers_validate.go) |
| create, replace, delete, events, journal entries | [issuers_write.go](../../internal/ci/issuers_write.go) |
| `ApplyDeclared` (Q-CI-16): create or replace by name, disable the removed, stop with an error naming both on a clash; `trove migrate` calls it always | [issuers_declared.go](../../internal/ci/issuers_declared.go) |
| the key rules and the stored form (public members only, at most 100 keys) | [keys_jwks.go](../../internal/ci/keys_jwks.go) |
| discovery and key fetch through the outbound factory, failure classes | [keys_fetch.go](../../internal/ci/keys_fetch.go) |
| the write rule, the failure record, metrics | [keys_store.go](../../internal/ci/keys_store.go) |
| the copy, the unknown-kid protocol, the age check (`KeySource`) | [keys_source.go](../../internal/ci/keys_source.go) |
| status, forced refresh, the task `ci-keys-refresh` | [keys_task.go](../../internal/ci/keys_task.go) |

What the later work packages need to know:

- **The shape of a rule violation (Q-CI-30), for W3.** Every violation is in `errors[]` with its pointer and code; a rule the
  specification names by its code (`pin_required`, `catch_all_condition`, `priority_taken` is a 409, …) is also the problem's `code`
  (`validationError` in issuers_validate.go; `errs.E(errs.Invalid, <named code>, …, errs.WithFields(…))`). Any other violation is
  `validation_failed`.
- **`Mappings.DeleteForIssuerTx` (W3, built).** Added to the interface for the cascade of an issuer's deletion; W3's store deletes the
  mappings and their principals on the issuer's transaction, one `ci.mapping.deleted` each.
- **The revoker (W5).** `Issuers.Replace` (disable), `Delete` and `ApplyDeclared` (removal) call `Revoker.RevokeIssuerTx` in their
  transaction only when the issuer has a row in `ci_token_issuances`; the revoker is the real one (W5), so the tokens are revoked
  in the same transaction (the stub that answered 501 is gone).
- **The key source (W4).** `Issuers.KeySource(issuer).Keys(ctx, kid, alg)` returns `subject.ErrUnknownKey` and
  `subject.ErrKeysUnavailable`; a token without kid for an issuer with no stored set counts as an unknown kid. `Resolve` answers from
  the copy of all issuers; a miss or a disabled hit re-reads them at most once per replica and `cluster.epoch_poll_interval`
  (`rereadIssuers`, Q-CI-51), so an unknown `iss` costs at most one query per interval. The JWK rules are `jws.ParseJWKS`'s
  ([Q-CI-42](../spec/gitlab-ci-oidc.md#q-ci-42--one-implementation-of-the-key-rules-the-shared-leaf)): keys_jwks.go reduces a fetched set
  entry by entry with it (a usable key keeps its public members, an ignored one its `kid`, `kty`, `alg` and the reason) and reads the
  stored form with it again; the candidates of a token are `jws.Candidates`, so the key status and the validation cannot disagree.
- **The metric** `trove_ci_signing_keys_refresh_total{issuer,trigger,outcome}` is incremented by every fetch; the gauge
  `trove_ci_signing_keys_age_seconds` skips an issuer whose first fetch failed (the placeholder row, Q-CI-32).
- **Counting the reads of `ci_issuer_keys` in a test.** The test of the random-kid bound
  (`TestR_CI_003_ARotationCausesOneFetchAndRandomKidsCauseNone`) reads `pg_stat_user_tables`, which a backend fills late: it flushes
  its counts when a statement ends at least 1 s after its last flush, or after 1 s of idleness, so a replica backend that is
  polled every 500 ms lands them up to 1.5 s late. A test that measures a window waits `statsFlushWait` (2.5 s) before each
  reading, so the reads of the activity before the window are not counted inside it. A shorter wait makes the count drift by the
  reads of the previous step (exactly 10 for the rotation of that test).

## Pending criteria

Criteria that need something that is not built. The build tag `ci_pending` and its file `pending_test.go` are gone: W4 moved the last
of its tests into the run ([exchange_mappings_test.go](../../test/integration/ci/exchange_mappings_test.go), through the exchange). What
waits is listed here with what it waits for; none of it has a test behind a tag.

| Criterion | Needs |
|---|---|
| R-AUTHZ-014 "`POST /api/v1/authz/explain` lists the inline grants with `source.kind` `ci-inline`": the decision's sources already name them (`ci-inline:<issuer>/<mapping>#<n>`); the explainer is not built (Q-AUTHZ-27) | the explainer |
| R-CI-030's drained cleanup over 100,000 rows (a node drained under the worker): W7 checks where the three tasks run, not a drain under one | a test with 100,000 expired rows and a worker node drained |
| R-CI-008's `job_token_rejected`, `job_not_running`, `gitlab_unreachable` | R-CI-022 (P2, Q-CI-5); [Q-CI-43](../spec/gitlab-ci-oidc.md#q-ci-43--the-reasons-the-integration-tier-cannot-reach) |

The tests of W1 itself are [test/integration/ci](../../test/integration/ci) (`issuers_test.go`, `keys_test.go`, `declared_test.go`,
`tasks_test.go`) and the unit tests of [internal/ci](../../internal/ci).

| `TestR_CI_004_*` of [subject_test.go](../../internal/ci/subject/subject_test.go) | every step of R-CI-004 with a failing token from the issuer double and its reason, the algorithm-confusion token (Q-CI-24), the audience and skew criteria, the step order, at most 10 keys without `kid` |
| `TestR_CI_004_*` of [jws_test.go](../../internal/auth/jws/jws_test.go), `TestTheKeyRulesOfAJWKSet` | every supported algorithm, `none` and HMAC never, key fit, no key from a header, the parser's refusals, R-CI-003's key rules and their reasons |
| `TestR_CI_010_*`, `TestR_CI_011_*`, `TestR_CI_012_*`, `TestR_CI_013_*`, `TestR_CI_014_*`, the scope tests `TestR_CI_007_*` (the IDs restored with the exchange), `TestR_AUTHZ_014_AnEscapedClaimMatchesOnlyItsLiteralName` ([internal/ci/claims](../../internal/ci/claims)) | the glob table, normalization, arrays, `null` and missing claims, `(a+)+$` (median under 1 ms without the race detector, the p99 with `TROVE_PERF_GATE=1`), the limits, the save-time rules and warnings, the cross-project classification and the per-mapping cap, the grant specification, every template example and render error, the pure scope cases |
| the login's tests of [internal/auth/oidc](../../internal/auth/oidc) | unchanged and green on the shared verifier |

## W2: the claim engine and the validation

Built: the subject-token validation of R-CI-004, the claim engine of R-CI-010 to R-CI-014 with the save-time rules and the nine
warnings, the pure part of R-CI-007's scope grammar, and the shared leaf `internal/auth/jws` with the login moved onto it. Decided on the
way: [Q-CI-27](../spec/gitlab-ci-oidc.md#q-ci-27--the-reason-of-a-missing-or-malformed-time-claim) (the reason of a missing or malformed
time claim), [Q-CI-28](../spec/gitlab-ci-oidc.md#q-ci-28--what-a-condition-accepts-where-r-ci-010-sets-no-rule) (depth, numbers, glob
length, `ignoreCase`), [Q-CI-29](../spec/gitlab-ci-oidc.md#q-ci-29--what-a-template-substitution-may-render) (empty values, segments,
anchors), and the per-mapping bundle (Q-CI-17 as amended).

### Files

| File | Responsibility |
|---|---|
| [jws/parse.go](../../internal/auth/jws/parse.go) | `Parse` (three strict base64url parts, the header as a UTF-8 JSON object with unique members, `alg`, `kid` ≤ 256, no `crit`), `Token.Unsecured`, `DecodeObject` (no duplicate member names at any level, numbers as `json.Number`), the error classes with fixed details |
| [jws/verify.go](../../internal/auth/jws/verify.go) | the nine algorithms, `Supported`, `KeyType`, `Fits` (type, curve, 2048-bit RSA, the key's own `alg`), `Token.VerifyWith` |
| [jws/jwk.go](../../internal/auth/jws/jwk.go) | `ParseJWKS` with `Set.Ignored` and the stable reasons (`use_not_sig`, `key_ops_without_verify`, `unsupported_key_type`, `unsupported_curve`, `rsa_key_too_small`, `unsupported_algorithm`, `algorithm_key_mismatch`, `private_key_material`, `invalid_key_material`), `SelectKey`, `Candidates` |
| [oidc/jws.go](../../internal/auth/oidc/jws.go) | the login's names over the leaf; the login's behavior and tests are unchanged |
| [subject/subject.go](../../internal/ci/subject/subject.go) | `Parse`, `CheckIssuer`, `Unverified.Verify` and its steps |
| [claims/claims.go](../../internal/ci/claims/claims.go) | `Error`, normalization (`Normalized`, `First`), `CrossProject`, `Snapshot`, the three reasons the reason leaf does not name yet |
| [claims/condition.go](../../internal/ci/claims/condition.go), [evaluate.go](../../internal/ci/claims/evaluate.go) | the tree, `ParseCondition` (also `Node.UnmarshalJSON`), `Validate`, `Pins`, `RequiresProtectedRef`, `TestsPipelineSource`, `GuardedClaims`; `Evaluate` and the trace |
| [claims/glob.go](../../internal/ci/claims/glob.go), [regex.go](../../internal/ci/claims/regex.go) | the glob of R-CI-010 (greedy, at most the product of the lengths); RE2 anchored as `^(?:…)$` |
| [claims/template.go](../../internal/ci/claims/template.go) | the grammar, the filters, the contexts, `Anchored` |
| [claims/grants.go](../../internal/ci/claims/grants.go) | `Spec.Render`, `ApplyCaps`, `InlineGrant.ResolvedOwnership`, `Frozen`, `FrozenMapping`, `DecodeFrozen` |
| [claims/scope.go](../../internal/ci/claims/scope.go), [selector.go](../../internal/ci/claims/selector.go) | `ParseScope`, `Narrow`, `NarrowFrozen`, `FormatScope`, `Frozen.InlineScope`, `Freeze`; the selector grammar of R-AUTHZ-004 through `qname.ParseSelector` (a test holds the scope grammar equal to `authz.ValidateSelector`); `instanceForm`, `covers`, `disjoint` over instance selectors |
| [claims/check.go](../../internal/ci/claims/check.go) | `Check` and the warnings |

### What the other packages rely on

- **W1, the key source.** Build `subject.Key{KID, KTY, Alg, Public}` from `jws.ParseJWKS(body).Keys`; show `Set.Ignored` (kid, kty, alg,
  reason) in the key status. `jws.Candidates(keys, kid, alg, subject.MaxKeysWithoutKID)` is the selection of `KeySource.Keys`; a key that does
  not `Fits` the token is not a candidate, so a source that wants the refetch of R-CI-003 for such a key answers `ErrUnknownKey`. Verify
  answers `unknown_signing_key` when no returned key fits and `signature_invalid` when one fits and none verifies.
- **W3, the save.** Decode `match` with `claims.ParseCondition` (or `json.Unmarshal` into `Node`, the same rules) and prefix its pointers
  with `/match`; `Check`'s pointers are already relative to the body. Fill `SaveInput.Name` and `SaveInput.PolicyActions` (the actions the
  listed policies and groups give the mapping: `unprotected_write` and `unpinned_write` count them, `manage` among them is
  `action_not_allowed`). Store `Checked.Grants` and `Checked.Options` (resolved ownership and `crossProjectSource`). Whether a key selector
  of a project mapping is the project's (`repository_outside_project`) and whether policies and groups exist stay the service's.
- **W3, `Select`.** `Claims.CrossProject`, `Node.Evaluate` (the trace: `FailedLeaf` "" is the root leaf), `Spec.Render` (an `*Error`
  `grant_render_failed` whose pointer is relative to `grants`), and per contributing mapping `claims.Freeze`. `Selection.Rendered` of the
  contracts is the union; W3 and W4 keep the per-mapping rendering for the bundle.
- **W4, the exchange.** `subject.Parse(raw, ci.exchange.max_subject_token_bytes)`, the lookup by `Unverified.Issuer`, `subject.CheckIssuer`,
  `Verify`; `ErrKeysUnavailable` comes back as is (503). `claims.ParseScope` (`scope_malformed`), `NarrowFrozen` with `held` answering the
  policy and group grants and the selectors the text cannot decide (classes, format filters) from the index (`scope_not_granted`),
  `FormatScope` per mapping or `Frozen.InlineScope`, with the policy and group items in front. No `Detail` holds a claim value or token
  material, except the condition trace's, which quotes the claim values and belongs to the dry run and the audit event only.
- **The reason leaf.** `claims.ReasonInvalidPattern` and `ReasonInvalidValue` have the names of registered
  `errs` codes; they move to `internal/ci/reason` with the next change of that file.

### Contract changes

- `claims.SaveInput` gained `Name` (the project prefix needs it) and `PolicyActions` (the write rules count policy and group writes,
  which only the service can read).
- `claims.Frozen` is per contributing mapping (Q-CI-17 as amended): `Groups`, `Inline`, `Caps` and `Project` moved into
  `Contributions []FrozenMapping`; `Version`, `Add`, `Check`, `Groups()` and `DecodeFrozen` are new; `Frozen.HasCap` became
  `FrozenMapping.HasCap`. Nothing outside the package read the moved fields.
- Added: `subject.CheckIssuer`, `subject.DefaultMaxBytes`, `subject.JobTokenPrefix`, `claims.ApplyCaps`, `Freeze`, `NarrowFrozen`,
  `Frozen.InlineScope`, `ScopeItem.String`, `Action.Valid`, `InlineGrant.Writes`, `InlineGrant.ResolvedOwnership`, `MaxGlobLength`.
- The regular-expression memo of the first draft is not there: R-ARCH-032 allows no package-level `sync.Map`. Since Q-CI-49 the mapping set
  of a replica holds each condition compiled (`claims.CompileCondition`, object state of the set), so the exchange compiles nothing; the
  traced `Node.Evaluate` of the dry run compiles per evaluation.

### Fuzz targets

Run each locally for a minute (`-fuzztime 1m`); a failing input stays in the package's `testdata/fuzz`.

| Target | Package | What it holds |
|---|---|---|
| `FuzzR_CI_004_Parse` | `internal/auth/jws` | the parser never panics, errors are `ErrMalformed` with a fixed detail, an unsecured token never verifies |
| `FuzzR_CI_004_SubjectToken` | `internal/ci/subject` | Parse and Verify on arbitrary input: only the structural reasons from Parse, only 400 reasons from Verify, no foreign acceptance |
| `FuzzR_CI_010_Condition` | `internal/ci/claims` | parse, validate, evaluate, and the stored form parses back to itself |
| `FuzzR_CI_010_Glob` | `internal/ci/claims` | `GlobMatch` against an RE2 translation of the glob semantics |
| `FuzzR_CI_014_Template` | `internal/ci/claims` | parse and render in every context; only `template_invalid` and `grant_render_failed`; the length bound |

```sh
go test ./internal/ci/claims -run '^$' -fuzz '^FuzzR_CI_014_Template$' -fuzztime 1m
```

## W3: the mappings

Built: the mapping store with its principals and the issuer's `mappings_version`, the save-time rules of R-CI-010 to R-CI-014 on every
write, the policy binding of R-CI-013, the per-replica copy of the mapping sets, `Select`, the dry run, the snippet, the project tier
with its bounds and its routes, and the delete check of a project. Decided on the way:
[Q-CI-35](../spec/gitlab-ci-oidc.md#q-ci-35--what-a-mapping-save-writes-into-a-policys-principal-list) (what a save writes into a
policy's principal list),
[Q-CI-36](../spec/gitlab-ci-oidc.md#q-ci-36--what-the-dry-run-answers-where-it-cannot-know) (what the dry run answers where it cannot know),
[Q-CI-37](../spec/gitlab-ci-oidc.md#q-ci-37--the-edges-of-a-projects-mapping-the-policies-it-sees-the-project-lock-and-a-system-administrators-edit)
(the edges of a project's mapping).

### Files

| File | Responsibility |
|---|---|
| [mappings.go](../../internal/ci/mappings.go) | `MappingService`, `MappingsOptions`, `NewMappings`, the copy `CacheMappings` (key: issuer ID, area `ci_mappings`, `SafetyTTL` `ci.mappings.cache_safety_ttl`), the two notifies |
| [mappings_store.go](../../internal/ci/mappings_store.go) | row to `Mapping`, the stored forms (`grants` without `policies`), the record, `mappingEvent` with its journal entry, the PUT event rule |
| [mappings_validate.go](../../internal/ci/mappings_validate.go) | the shape rules, `mappingViolation` (the answer of Q-CI-30 with the named codes), the defaults |
| [mappings_read.go](../../internal/ci/mappings_read.go) | `List`, `Get`, `ListIn`, `GetIn`, `CountIn`, `Set`, `SetTx`; the policies are filled from the principal lists |
| [mappings_write.go](../../internal/ci/mappings_write.go) | `Create(In)`, `Replace(In)`, `Delete(In)`, `DeleteForIssuerTx`, the revocation, the principal's status |
| [mappings_bind.go](../../internal/ci/mappings_bind.go) | the policies and groups of a save, `applyBindings`, the project's repositories |
| [mappings_evaluate.go](../../internal/ci/mappings_evaluate.go) | `Evaluate`, `EvaluateIn` |
| [mappings_snippet.go](../../internal/ci/mappings_snippet.go) | `Snippet`, `renderSnippet` |
| [select.go](../../internal/ci/select.go) | `Select`, `Selection`, `Contribution`, `TraceEntry` (moved out of exchange.go) |
| [subject/times.go](../../internal/ci/subject/times.go) | `CheckTimes`, step 8 of R-CI-004 for a claim set, called by the dry run |
| [ci_mappings_api.go](../../internal/api/v1/ci_mappings_api.go), [ci_snippets_api.go](../../internal/api/v1/ci_snippets_api.go), [ci_project_api.go](../../internal/api/v1/ci_project_api.go) | the handlers; the condition goes through `claims.ParseCondition`, so the pointers of a violation are the body's |
| [ci_mappings.sql](../../internal/meta/postgres/queries/ci_mappings.sql) | the statements; `project.sql` counts `ci_mappings` for the project |

### The write

One READ COMMITTED transaction: the project row `FOR UPDATE` (a project's create only), the issuer row `FOR UPDATE`
(`lockIssuer`), the mapping, the policies named (`CiPoliciesLock`, in name order), then the rules (`check`: the policies and groups,
`claims.Check` with `SaveInput.PolicyActions` and `IssuerFacts.Project`, the project's repositories), the priority, the principal, the
row, the bindings, `CiIssuerBumpMappings` and `cluster.Notify` on `ci_mappings`, a create, enable, disable or delete also on `authz`
(`notifyMappingState`, Q-CI-47), and the event with the journal entry. A name is looked up in the issuer's tier only (`mappingByName(issuer, tier, name)`), so the same name may be a system mapping and a mapping of each project and a
taken name is 409 `already_exists` within its own tier; a project mapping lists the policies of its project by their name within the project (stored as
`<project>/<name>`, `storedPolicyName`). A name that exists is 409 `already_exists`, a priority held by another mapping of the tier 409
`priority_taken`, a limit 422 `mapping_limit_reached` (`ci.mappings.max_per_issuer`, or `ci.mappings.max_per_project` for a project's tier,
`checkProjectQuota`). A disable or delete revokes only when the mapping has a row in `ci_token_issuances` (`CiMappingHasIssuances`, bounded to unexpired records, Q-CI-53) and
then calls `Revoker.RevokeMappingTx` in the same transaction.

### What the other packages rely on

- **W4, the exchange.** `Mappings.Set(ctx, issuerID)` is the copy with the versions it was read at (read as: the issuer's counters first,
  then the rows, so a set is never labelled newer than it is); `SetTx` reads PostgreSQL in the exchange's transaction with every mapping
  row `FOR SHARE` and refreshes the copy. `Select(set, "id_token", claims)` returns `Selection{CrossProject, Contributions, Dropped,
  Failure, Detail}` without a trace (the exchange's, Q-CI-49) and `SelectTraced` the same with `Trace` (the dry run's); there is no flat
  union of the rendered grants, because a cap and a project bound only their own mapping's. Build the
  bundle with `Frozen.Add(c.Frozen())` per contribution. `Contribution.Mapping` has `ID`, `Ref`, `Tier`, `Project`, `ProjectID`,
  `Version` and `PrincipalID`. `Mapping.Grants.Policies` is read from the policies' principal lists for display; the evaluator reads
  the live index, not this field. The dry run computes the lifetime with `ComputeLifetime`, the exchange's formula (`dryRunTTL` builds its
  input, Q-CI-36, Q-CI-41, Q-CI-47).
- **W5, the revoker.** `RevokeMappingTx(ctx, tx, actor, mapping, CauseMapping)` runs in the write's transaction before the mapping row
  and its principal are deleted (delete) and before the principal's status is set (disable); the mapping is the one read under the issuer
  lock. W3 notifies the change area `auth` for the principal (`principal.NotifyPrincipals`) on a status change and a delete, and the
  area `authz` for each policy it touches; the tokens' own eviction is the revoker's. `wireCI` builds the store with `Revoker:
  out.revoker`: replace `out.revoker` before `NewMappings` is called. `DeleteForIssuerTx` leaves the tokens to `RevokeIssuerTx`.
- **W6, the UI.** The wire forms are `ciMappingOut`, `ciEvaluateOut` in ci_mappings_api.go: `grants` always has `policies`, `groups` and
  `inline`; `options` is resolved; `warnings` is on the answer of a save only; `failedLeaf` is present for a mapping that did not
  match (`""` for a root leaf).

### Contract changes

- `Selection.Mappings`, `Selection.Rendered` and `Selection.Caps` are gone: `Selection.Contributions` (`Contribution{Mapping,
  Rendered, Caps}`) replaces them, `Selection.Mappings()` is a method. `Select`, `Selection` and `TraceEntry` moved from exchange.go
  to select.go (the one edit of W4's file).
- `Mapping.ProjectID` is new. `TraceEntry.Contributes` is false for a mapping that did not match, too.
- `StubMappings` is gone; `migrate_declared.go` passes no mapping store to the issuer service (the migration deletes no issuer).
- `Project.CIMappings` and `ProjectGet`/`ProjectList`'s `ci_mappings` column; the problem text of `project_not_empty` names CI mappings
  (the web message in problem-messages-access.ts still names repositories and service accounts: W6).
- The route `GET /projects/{key}/ci/issuers/{issuer}/snippets/{kind}` (`getProjectCiSnippet`) is new.
- The list of mappings is ordered with the system tier first, then the projects' tiers by key (the SQL order `tier` alone put
  `project:…` before `system`).
- The reference pipeline's closing revocation is a block scalar (the plain form is a YAML mapping) and its error line prints
  `requestId`.

## W4: the exchange

Built: `POST /api/v1/auth/oidc/token` (R-CI-007, R-CI-008), the lifetime (R-CI-016), the replay set and the restore cut-off (R-CI-018),
the issuance record and the events and metrics of an exchange (R-CI-020), the two budgets and the denial aggregation (R-CI-021), the
one implementation of the key rules (Q-CI-42). Decided on the way:
[Q-CI-41](../spec/gitlab-ci-oidc.md#q-ci-41--the-reason-of-a-request-that-cannot-succeed-by-its-own-parameters) (the reason of a
request that cannot succeed by its own parameters: `ttl_below_minimum`, an empty `subject_token`),
[Q-CI-42](../spec/gitlab-ci-oidc.md#q-ci-42--one-implementation-of-the-key-rules-the-shared-leaf) and
[Q-CI-43](../spec/gitlab-ci-oidc.md#q-ci-43--the-reasons-the-integration-tier-cannot-reach) (the reasons the integration tier cannot
reach). The gaps are H-418 to H-420 of [ci-federation.md](../security/ci-federation.md#what-this-does-not-cover).

### Files

| File | Responsibility |
|---|---|
| [ci_exchange.go](../../internal/api/v1/ci_exchange.go) | the handler: the switch (404), `CheckAddress` before the body, the body limit, form or JSON (`jws.DecodeObject`), a parameter twice, the unknown ones ignored and never quoted, the OAuth answer with `Cache-Control: no-store` and `Pragma: no-cache`, `expose_reason`, `Retry-After` |
| [exchange.go](../../internal/ci/exchange.go) | `ExchangeRequest` (prints without its tokens), `ExchangeResult`, `ExchangeError`, the interface `Exchanger` |
| [exchange_service.go](../../internal/ci/exchange_service.go) | `ExchangeService`, `ExchangeOptions`, `NewExchange`; `CheckAddress` with `exempt_cidrs`, `Refused`, `Exchange` (steps 1 and 2, the subject budget), the mapping of errors to reasons, the metrics, the log line and the denial event |
| [exchange_request.go](../../internal/ci/exchange_request.go) | step 1: the parameters (Q-CI-41) |
| [exchange_tx.go](../../internal/ci/exchange_tx.go) | the transaction: `LockShareTx`, the comparison with the copies, the second validation and `SetTx` on a mismatch, `configuration_changed`, the restore cut-off and the `jti` upsert, the bundle, the scope, `ComputeTTL`, `IssueCITx`, the issuance record, `ci.exchange.succeeded`; the statements behind `exchangeQueries` |
| [exchange_scope.go](../../internal/ci/exchange_scope.go) | `GrantIndex`, `GrantIndexes`, `GrantIndexFunc`; `narrow`: the stored scope, `NarrowFrozen` with the index as `held` (an unknown key is `scope_not_granted` like any item the grants do not hold, Q-CI-51), the answer's `scope` |
| [exchange_denials.go](../../internal/ci/exchange_denials.go) | the per-replica aggregation of `ci.exchange.denied` (R-CI-021): per address, reason and minute, a timer the first entry arms, at most 10,000 addresses, the flush at shutdown |
| [ttl.go](../../internal/ci/ttl.go) | `TTLInput`, `ComputeTTL` with the reason of its cause (moved out of exchange.go); `ComputeLifetime`, `Lifetime` and `tokenOwner`: the token's lifetime from its owner's bound and the window of every other contribution (Q-CI-47) |
| [authz/ci_scope.go](../../internal/authz/ci_scope.go) | `Engine.CIScopeOf`: `Held`, `PolicyItems` on the installed index; the index records the policy entries of CI mapping principals and groups as text (`ciTexts`, [index.go](../../internal/authz/index.go)) |
| [keys_jwks.go](../../internal/ci/keys_jwks.go) | the stored form of a key set over `jws.ParseJWKS` (Q-CI-42) |
| [ci_exchange.sql](../../internal/meta/postgres/queries/ci_exchange.sql) | `CiExchangeLockMappings`, `CiExchangeClock` (the transaction's `now()` and the `authz` epoch, Q-CI-47), `CiExchangeUseJTI`, `CiExchangeRestoredAt`, `CiExchangeInsertIssuance` |

### The request path

1. The handler answers 404 when `ci.exchange.enabled` is false, then `CheckAddress` (one counter; an address of `exempt_cidrs` skips
   it), then reads at most `ci.exchange.max_body_bytes` and decodes. A refusal of its own goes through `Refused`, so it is counted and
   recorded like every denial.
2. `Exchange`: step 1 (`checkRequest`); `subject.Parse`; `Issuers.Resolve` from the copy; `subject.CheckIssuer`; `ci_issuer`;
   `Verify` with the issuer's key source behind `heldKeys`, which asks it once and keeps the answer; the `jti` rule of single use; the
   subject budget (the first pin claim, else `sub`); `Mappings.Set` from the copy, before the transaction, because a miss reads on a
   connection of its own; `Select` on the copy, without a trace and with the conditions the set holds compiled (Q-CI-49).
3. One `InTx`: the issuer row `FOR SHARE`; when its `version` and `mappings_version` equal the copies', the selection made before stands
   and only the matched rows are re-read `FOR SHARE` (`CiExchangeLockMappings`); on any difference the stale path: `Verify` again against
   the re-read row with the keys held (an issuer whose `version` moved), `SetTx`, `Select` again, and a second difference is
   `configuration_changed`. Then the restore cut-off for a single-use issuer, the `jti` upsert, the selection's failure if any,
   `CiExchangeClock`, the bundle (`Contribution.Frozen` per contribution, `AuthzEpoch`), `narrow`, `ComputeLifetime` (and each shorter
   contribution's `ExpiresAt`), `IssueCITx` (owner: `tokenOwner`), `CiExchangeInsertIssuance` (the record's expiry is the token row's) and
   `ci.exchange.succeeded`, all in that transaction.
4. After it: `Issuers.Reload` when the copy was stale (`SetTx` already refreshed the mapping copy), `trove_ci_mapping_set_stale_total`,
   `ci.exchange.replayed` or `.repeated` through the buffer, `trove_ci_exchanges_total` and `trove_ci_exchange_duration_seconds`.

### What the other packages rely on

- **The scope's index answers are the replica's.** `PolicyItems` and `Held` read the installed grant index without waiting for an
  epoch, as every request decides on the confirmed index (R-AUTHZ-028); a test that exchanges right after a mapping save waits for the
  replica's index (`xenv.settled` in the tests). The policy grants of the token are decided live at use time either way.
- **The denials are aggregated per replica.** A test that counts `ci.exchange.denied` rows uses an address of its own
  (`X-Forwarded-For` through the trusted loopback proxy) or stops the replica, whose flush writes the counts; a summary row carries
  `count` and no `iss`, `kid` or claims.
- **The issuance record exists for every token**, written beside the token row (Q-CI-32): the revoker and the bulk revoke find tokens
  through it.

### Contract changes

- `Exchanger.CheckAddress` takes the `audit.Source` (the address, the user agent and the request ID for the denial event), not an
  address string; `Exchanger.Refused` is new; `ExchangeRequest.RequestID` is `Source.TraceID`.
- `ComputeTTL` returns the reason and a detail naming the bound (`(ttl, reason, detail)`) and lives in ttl.go; the reason
  `ttl_below_minimum` is new (Q-CI-41). The reason names `invalid_pattern` and `invalid_value` moved from
  `internal/ci/claims` to `internal/ci/reason`.
- `StubExchanger` is gone, and so are the stubs: stubs.go keeps `NotImplemented`.
- `v1.CIServices.Exchange` (the `ci.exchange` settings the handler applies) is new; `ciPending` is gone.
- `CiExchangeLockIssuer` is gone (the issuer row is `Issuers.LockShareTx`'s); `CiExchangeRestoredAt` is new.
- The key status names the reasons of `jws.ParseJWKS` (`rsa_key_too_small`, `private_key_material`, …) and lists a repeated `kid` as two
  usable keys (Q-CI-42).
- The OpenAPI operation: the 501 is gone, 404 and 405 are documented, `ttl` is a string of digits (a JSON body may send a number),
  `Pragma` and `Retry-After` are declared.

### Tests

| Test | What it proves |
|---|---|
| [exchange_test.go](../../test/integration/ci/exchange_test.go) | R-CI-007's form and JSON exchange with the answer's fields and headers, the scope's policy and inline items; R-CI-017's install and publish with the token; R-CI-016's lifetime criteria and `ttl_below_minimum` |
| [exchange_errors_test.go](../../test/integration/ci/exchange_errors_test.go) | R-CI-008: every reason the MVP reaches with status, `error` and `reason` (`database_unavailable` by a lock held beyond the lock timeout, `signing_keys_unavailable` by an aged key row and an unreachable double, `internal` by a mapping row that does not decode); the denial event before and after the signature; `expose_reason: false`; the 404 and 405 |
| [replay_test.go](../../test/integration/ci/replay_test.go) | R-CI-018: single use on and off, concurrent exchanges on two replicas, a failed transaction that leaves no `jti`, the restore cut-off with its skew, `ci.exchange.replayed` and `.repeated` |
| [ratelimit_test.go](../../test/integration/ci/ratelimit_test.go) | R-CI-021: 1,000 requests from one address on three replicas, one replica and three with Valkey stopped, bounded by the address budget over the time the flood took (burst plus rate times that window, 150 for the criterion's 5 s); `exempt_cidrs` and the subject budget; 2,000 forged tokens from 20 addresses beside a legitimate project; 10,000 garbage requests without a fetch and under 100 audit rows; R-CI-029: the Valkey keys of the keyspace and an outage that changes no answer |
| [exchange_mappings_test.go](../../test/integration/ci/exchange_mappings_test.go) | R-CI-007's criteria on a replica whose change log is stopped with Valkey stopped (R-CI-019's shadowing mapping, a disabled mapping, and, in a test of its own, an audience removed on A that B refuses from the issuer row it re-reads in the transaction; `trove_ci_mapping_set_stale_total`), R-CI-009's first match and render failure, R-CI-012's fork pipelines and the recorded fork-in-parent fixture, R-CI-013's live policy binding, R-CI-017's revocation of an exchanged token on three replicas, R-CI-001's removed issuer, R-CI-020's issuance record and events |
| [exchange_service_test.go](../../internal/ci/exchange_service_test.go), [exchange_request_test.go](../../internal/ci/exchange_request_test.go), [exchange_denials_test.go](../../internal/ci/exchange_denials_test.go), [ttl_test.go](../../internal/ci/ttl_test.go) | the exchange without PostgreSQL: the stale paths and the second mismatch (Q-CI-43), step 1, the aggregation, `ComputeTTL` |
| [ci_exchange_test.go](../../internal/api/v1/ci_exchange_test.go), [authz/ci_scope_test.go](../../internal/authz/ci_scope_test.go) | the body's decoding and the rate-limit exemption; the index's scope answers |

Every `xenv` of the integration tests checks at its end that no error answer and no log line of its replicas holds a subject token it
sent or a `trv_cit_` value, and that no log line holds a CI token an answer issued (`checkNoSecrets`).

## W5: use time

Built: R-AUTHZ-014 in the evaluator, the ownership of a GitLab project (R-CI-015), the revocation of CI tokens (R-CI-017), the
issuance API with the project's view (R-CI-020, Q-CI-26), the two retention tasks (R-CI-030), the gauge `trove_ci_tokens_active`, and
`IssueCITx` (moved from W4 so that the tests have real CI tokens). Decided on the way:
[Q-CI-38](../spec/gitlab-ci-oidc.md#q-ci-38--the-revocation-reason-of-a-ci-tokens-row-and-the-events-of-a-revocation) (the row reason
and the events of a revocation), [Q-CI-39](../spec/gitlab-ci-oidc.md#q-ci-39--a-refused-install-of-a-ci-token-answers-as-every-refused-read)
(a refused install is a `404` under `hide_existence`), [Q-CI-40](../spec/gitlab-ci-oidc.md#q-ci-40--what-a-ci-publish-claims-when-several-grants-allow-it-and-what-an-added-ci-project-owner-shows)
(a `bind_project` grant decides the claim; an added project owner's display). The gaps are H-415 to H-417 of
[ci-federation.md](../security/ci-federation.md#what-this-does-not-cover).

### Files

| File | Responsibility |
|---|---|
| [authz/ci.go](../../internal/authz/ci.go) | `CIProject`, `CIGrants` (the contributions resolved against the index: the mapping principal by name, the listed groups by name through `Index.groups`, the project of a project tier), `ciGrantedBy` (per contribution: never `manage`, only `read` under the cap, the project's own policies for a project tier, the inline grants through the token scope's matcher and only on the project's repositories), `ciReadShape`, `ciWriteOf` (whether a `bind_project` grant allows the write, the claimers of rule 2) |
| [authz/ownership_ci.go](../../internal/authz/ownership_ci.go) | the owner identities of a CI token (`ciOwnFacts`: the project by issuer ID and project ID, the mappings and groups also by principal name), the claim of the project, the display update, `ci.package_ownership.claimed` and `.conflict`, the reference `ci:<issuer>:project:<id>` in the owner operations and in `GET /package-owners` |
| [authz/ownership.go](../../internal/authz/ownership.go) | the hooks: `keyFor`, `ciResolveClaimers` in `CheckTx` and `PrecheckWrite`, the CI owner in `judge`, `ownerOfRow`, `ClaimTx`, `recordClaim`, `AddOwnerTx`, `RemoveOwnerTx`'s journal entry, `ownedRows` |
| [authz/index.go](../../internal/authz/index.go), [decide.go](../../internal/authz/decide.go), [visible.go](../../internal/authz/visible.go) | `Index.groups`, `grant.project`; `evalState.isCI` and `ci`; the CI branch of `allowedAction` and `readShape` |
| [auth/token/cit.go](../../internal/auth/token/cit.go) | `IssueCITx`, `checkCIT`, `decodeCIContext` (with `claims.DecodeFrozen`), `RecordCIRevoked` (at most 1,000 IDs per event, the journal entry), `recordCIRevoked` (called by `revokeTree`: causes `single` and `self`) |
| [ci/revoke.go](../../internal/ci/revoke.go) | `TokenRevoker`: `RevokeMappingTx` (the tokens the mapping owns, for a system mapping also those it contributes to, Q-CI-47), `RevokeIssuerTx`, `RevokeMatching` (422 without `issuer` or a filter, 404 for an unknown issuer) |
| [ci/issuances.go](../../internal/ci/issuances.go) | `IssuanceStore`: `List`, `ListForProject` (found through the bundle's contributions, shown with the project's mappings only and without the scope strings, the person and the client, `onlyProject`, Q-CI-52), `Get` with the usage summary, `CleanupReplay`, `RetainIssuances` (single-statement batches of 1,000), `ActiveTokens`, `IssuanceSortKey` |
| [ci_issuances_api.go](../../internal/api/v1/ci_issuances_api.go), [ci_project_api.go](../../internal/api/v1/ci_project_api.go) | the three issuance operations and `listProjectCiIssuances`; `issuanceBody` renders `state` and `active` from the database's value |
| [queries/ci_issuances.sql](../../internal/meta/postgres/queries/ci_issuances.sql) | the statements above; `OwnershipOwners` of [ownership.sql](../../internal/meta/postgres/queries/ownership.sql) joins the issuer's name |
| [app/wire_ci.go](../../internal/app/wire_ci.go), [migrate_declared.go](../../internal/app/migrate_declared.go) | the revoker and the issuance service replace their stubs, also in `trove migrate`; `App.CIRevoker`, `App.CITokenIssuer` for the integration tests |

### What the other packages rely on

- **W3, the mapping store.** `Revoker.RevokeMappingTx(ctx, tx, actor, mapping, CauseMapping)` needs `Mapping.ID`, `PrincipalID`, `IssuerID`,
  `Issuer` and `Name`. Call it before the principal is deleted: `api_token.principal_id` cascades, so a principal deleted first takes
  its token rows with it and the revocation finds nothing to record. Disabling sets the principal's status in the same transaction; the
  token validator then refuses a token of the disabled owner with `principal_disabled`. A project mapping's revocation ends only the
  tokens it owns; its contribution to a token another tier owns ends in the evaluator once the index, rebuilt from the `authz`
  notification of the write, no longer holds the mapping as active (Q-CI-47).
- **W4, the exchange.** `IssueCITx` refuses a bundle that fails `Frozen.Check`, a `TTL` outside 1 s to `auth.tokens.cit.max_ttl` and an
  owner that is not an active `ci_mapping`; pass the allowlisted claim snapshot as `Claims`. The issuance record's `expires_us` is
  `CITIssued.ExpiresAt`, the token row's expiry, so the record and the row agree. A record must exist for every token: the revoker of an
  issuer and the bulk revoke find tokens through it (Q-CI-32).
- **W6, the UI.** The issuance list is the `CiIssuanceList` of the document with `nextCursor`; `usage.packagesDeployed` holds
  `<repository>/<package>@<version>`; the bulk revoke answers `{"revoked": n}`, `422 validation_failed` without a filter (`errors[]`
  names `/issuer` or the missing filter) and `404` for an unknown issuer. A project's list shows the project's mappings only, with an empty `grantedScope`, without `requestedScope`, the person and the client. `state` is `active`, `revoked`, `expired` or `removed` (the token row went with its mapping); `active` is `state == active` and is what `?active=` filters on (Q-CI-52).
- **The evaluator for everyone.** A CI subject is decided on its bundle; a CI subject without one holds nothing. Code that builds a
  `Subject` of kind `ci` by hand (a test) must set `CI`.

### Contract changes

- `CiIssuanceTokensOfMapping` takes the principal, the issuer and the mapping ID (the tokens a mapping contributes to as well);
  `CiReplayDeleteExpired` has a sargable prefilter on the expiry index; the revoking statements order by token ID.
- `OwnershipOwners` returns `ci_issuer_name`; an owner of kind `ci_project` reads `ci:<issuer name>:project:<id>`.
- `token.CITRequest` gained `Claims`, `token.CITIssued` `CreatedAt`; `token.RecordCIRevoked` and `token.MaxCIRevokedIDs` are new.
- `ci.StubRevoker` and `ci.StubIssuances` are gone; `ci.NewRevoker`, `ci.NewIssuances`, `ci.ActiveTokens` and `ci.IssuanceSortKey` are
  new, and `IssuanceFilter.Cursor` is the sort key joined by a comma.
- The `CIGrantSet.Allows` and `CIGrantSet.Claims` of the contracts became `ciGrantedBy` and `ciWriteOf`: a decision needs the request's
  engine (the token scope's matcher and its cache), not only the index.
- `token.created` records `ciIssuer`, `ciMappings` and `ciClaims`; `ci.package_ownership.conflict` records the current `owners`.

### Tests

| Test | What it proves |
|---|---|
| `TestR_CI_009_*`, `TestR_CI_020_*`, `TestThePrincipalOfAMappingMirrorsEnabled`, `TestR_CI_002_CascadeDeletesTheMappingsAndTheirPrincipals` ([mappings_test.go](../../test/integration/ci/mappings_test.go)) | the lifecycle, the ETags, the first match with p10 and p20, the render failure without fall-through, equal priority 409, two issuers' principals and the policy after delete and re-create, the principal mirroring `enabled`, the PUT event rule and the journal, the cascade, the 501 for a mapping with issuance records, the copy on replica B (100 trials in 1 s with Valkey, 12 trials in the poll interval plus 1 s without), `SetTx` |
| `TestR_CI_010_*`, `TestR_CI_013_*` ([mappings_rules_test.go](../../test/integration/ci/mappings_rules_test.go)) | every save-time rule over HTTP with its code and pointer, the warnings, the policy binding in both directions, the write rules counting policies and groups |
| `TestR_CI_019_*`, `TestR_CI_012_*` ([dryrun_test.go](../../test/integration/ci/dryrun_test.go)) | the dry run of the example claim set and the failing leaf, the scope, `now`, nothing issued, the fork classification and the opt-in |
| `TestQ_CI_26_*` ([project_mappings_test.go](../../test/integration/ci/project_mappings_test.go)) | the project tier's bounds and narrowing, the union with the system tier, `project_switch_off`, `project_not_empty`, create against delete |
| `TestR_API_035_*`, `TestQ_CI_26_AProjectAdministratorReadsTheSnippet…` ([snippets_test.go](../../test/integration/ci/snippets_test.go)), `TestTheSnippetIsYAML` ([mappings_snippet_test.go](../../internal/ci/mappings_snippet_test.go)) | the snippet for npm and NuGet, the project route, the YAML |
| `TestR_CI_009_Select*`, `TestR_CI_012_Select*` ([select_test.go](../../internal/ci/select_test.go)) | `Select` without a database |
| `TestR_AUTHZ_014_*`, `TestR_CI_012_TheCapBoundsOnlyItsOwnMappingsGrants`, `TestR_AUTHZ_013_TheScopeIsTheCeilingOfACIToken`, `TestR_CI_015_TheClaimOfACIPublish` ([authz/ci_test.go](../../internal/authz/ci_test.go)) | the union, the per-contribution cap and project bound, the escaped claim, `manage` never, the scope as ceiling, the listing, the claim and the owner identities, without PostgreSQL |
| [use_test.go](../../test/integration/ci/use_test.go) | R-CI-013's install, publish and refusal; R-AUTHZ-014's escaped claim and the removal from a policy on three replicas with Valkey running and stopped; R-AUTHZ-002; R-CI-017's Bearer, Basic and `X-NuGet-ApiKey` |
| [ownership_test.go](../../test/integration/ci/ownership_test.go) | R-CI-015's four criteria; R-AUTHZ-014's `bind_project` and `none` defaults; a `bind_project` grant in a repository whose ownership is off |
| [revoke_test.go](../../test/integration/ci/revoke_test.go) | R-CI-017's 2 s bound on three replicas with and without Valkey for the bulk, the mapping's and one token's revocation; `DELETE /tokens/current`; R-CI-002's disable and delete of an issuer; R-CI-001's removed configuration issuer |
| [issuances_test.go](../../test/integration/ci/issuances_test.go) | R-CI-020's list, filters, paging, usage, bulk revoke and gauge; who may read; Q-CI-26's project view and what Q-CI-52 leaves out of it; the states of a revoked, an expired and a removed record |
| [retention_test.go](../../test/integration/ci/retention_test.go) | R-CI-030's two tasks over more than one batch, run twice |

## Fix wave A: tier isolation and issuer hardening

The findings of the security audit and the code review of phase 5 (2026-11-03) on the tiers, the declared issuers, the cost of an
exchange and the edges of the exchange and the issuer writes. Decided on the way:
[Q-CI-47](../spec/gitlab-ci-oidc.md#q-ci-47--a-project-tier-changes-only-what-its-own-tier-gives-a-token) (a project tier changes only
its own contribution),
[Q-CI-48](../spec/gitlab-ci-oidc.md#q-ci-48--a-declared-issuers-issuer-url-and-kind-never-change-under-its-name) (a declared issuer's
identity), [Q-CI-49](../spec/gitlab-ci-oidc.md#q-ci-49--what-bounds-the-cost-of-an-exchange) (the cost bounds),
[Q-CI-50](../spec/gitlab-ci-oidc.md#q-ci-50--an-issuer-change-that-moves-trust-resets-the-keys-or-is-refused) (key location and pin
changes), [Q-CI-51](../spec/gitlab-ci-oidc.md#q-ci-51--what-the-exchange-tells-a-caller-and-when-a-replica-learns-an-issuer) (the scope
answer, the prefix order, the replay margin, the issuer re-read). The gaps are H-424 to H-427 of
[ci-federation.md](../security/ci-federation.md#what-this-does-not-cover); H-413 and H-418 are corrected there.

### What the other packages rely on

- **The bundle is version 2.** Write `claims.FrozenVersion`; set `Frozen.AuthzEpoch` from the transaction (`CiExchangeClock`) and a
  contribution's `ExpiresAt` only when its window is shorter than the token's. `DecodeFrozen` reads versions 2 and 1; a reader of an
  issuance record's `grants` (the issuance views) sees `expiresAt` and `authzEpoch` beside the members it knew.
- **The evaluator drops contributions.** Code that builds a CI `Subject` by hand (a test) and expects every contribution to count either
  lists the mappings in `Snapshot.CIMappings` or sets `Frozen.AuthzEpoch` above the index's epoch (`laterEpoch` in
  [authz/ci_test.go](../../internal/authz/ci_test.go)). The grant index reads `CiMappingsActive` in its snapshot.
- **A mapping write notifies `authz`** on create, enable, disable and delete (payload `{"type": "ci_mapping", "id": …}`), so a test that
  uses a token right after such a write waits for the replicas' index (`xenv.settled`) as it does after a policy change.
- **`Select` has no trace.** The dry run calls `SelectTraced`; `MappingSet` built with `newMappingSet` (`loadSet`, `SetTx`) holds the
  compiled conditions and the evaluation order, and a set built by hand is compiled and sorted per call.

### Contract changes

- `claims.FrozenVersion` is 2; `claims.FrozenVersion1`, `Frozen.AuthzEpoch`, `FrozenMapping.ExpiresAt`, `FrozenMapping.ExpiredAt` are new;
  `Frozen.Check` accepts both versions and refuses a version-1 bundle with a member of version 2.
- `claims.Condition`, `claims.CompileCondition`, `claims.PinViolation`, `claims.CheckProjectName` (was `checkProjectName`) and the size
  bounds `MaxLiteralLength`, `MaxClaimNameLength`, `MaxPathMembers`, `MaxConditionBytes` are new.
- `ci.SelectTraced`, `ci.Dropped`, `Selection.Dropped`, `ci.ComputeLifetime` and `ci.Lifetime` are new; `Select` carries no trace;
  `TTLInput.MappingMax` is the owner's bound (`ComputeLifetime` sets it).
- `GrantIndex.UnknownKey` and `authz.CIScope.UnknownKey` are gone.
- `authz.Snapshot.CIMappings` is new; `Index.CIGrants` leaves out the contributions `ciContributes` refuses.
- `CiIssuanceTokensOfMapping` takes `contributed` (the system tier's), and both revocation statements read unexpired records only;
  `CiExchangeClock`, `CiMappingCountOfTier`, `CiMappingsActive` and `CiKeysReset` are new.
- `ci.mappings.max_per_project` is new; `ci.replay.retention_after_expiry` is at least 1 m; `max_ttl_seconds` of a mapping and of an
  issuer is at least `ci.token.min_ttl`.
- The code `mappings_unpinned` (409) is new; `mapping_limit_reached` also answers the project's quota; `condition_invalid` also answers
  the size bounds.

### Tests

| Test | What it proves |
|---|---|
| [tiers_test.go](../../test/integration/ci/tiers_test.go) | Q-CI-47 over HTTP on two replicas: a project's bound of 1 s ends its contribution and not the token, a project's render failure drops its tier only, disabling and deleting a project mapping keeps the system tier's token and ends the contribution on both replicas, a token a project mapping owns is revoked |
| [bounds_test.go](../../test/integration/ci/bounds_test.go) | the floor of `maxTtlSeconds` for mappings and issuers, the project quota beside the system tier, a literal over its bound, the prefix before the lookup and the key overlap of Q-AUTHZ-176 |
| [issuer_changes_test.go](../../test/integration/ci/issuer_changes_test.go) | a changed key location fails closed until its set is fetched, a public switch and a narrowed `pinClaims` refused with the unpinned mappings, a lagging replica serving a new and a re-enabled issuer within the poll interval |
| [revoke_bound_test.go](../../test/integration/ci/revoke_bound_test.go) | the revocations of a mapping and an issuer read the unexpired issuance records only (3,000 expired records beside 20,000 live tokens of other kinds) |
| [declared_test.go](../../test/integration/ci/declared_test.go) `TestR_CI_001_ADeclaredIssuerKeepsItsIssuerURL` | `trove migrate` refuses a changed issuer URL, naming both, and applies the new instance under a new name |
| `TestQ_CI_47_*`, `TestR_CI_009_*`, `TestR_CI_016_*`, `TestQ_CI_51_*`, `TestR_CI_018_*` of [internal/ci](../../internal/ci), [internal/authz](../../internal/authz/ci_test.go), [internal/ci/claims](../../internal/ci/claims/bounds_test.go), [internal/config](../../internal/config/ci_test.go) | the selection per tier, the lifetimes, the bundle of the exchange, the evaluator's drop, version 2 and 1, the size bounds, the compiled condition against the trace, the scope answer, the stored expiry, the configuration floors |

## W7: the end-to-end tests on kind

The reference pipeline of R-CI-028 with the real npm and .NET clients against the issuer double, in the scatter profile; the page is
[e2e.md](e2e.md#the-ci-tests). What W7 built:

| What | Where |
|---|---|
| The set `ci` (every change: `TestCIPipeline`, `TestCITokens`, `TestCITasks`) and the set `ci-ha` (nightly: the drained node, Valkey stopped, the failover with `FLUSHALL`, the deleted pod) | [test/e2e/ci_*_test.go](../../test/e2e), [deploy/dev/e2e.sh](../../deploy/dev/e2e.sh), the jobs `e2e-ci` of Test and Release (port 8091) and of HA and Performance (port 8092) |
| The browser journey `13-ci` | [web/e2e/tests/13-ci.spec.ts](../../web/e2e/tests/13-ci.spec.ts), started by `TestE1_11Journeys` |
| The double on kind, once per issuer (`STUB=oidcstub`, `oidcstub-single`), pinned to the control-plane node | [deploy/dev/oidcstub.yaml](../../deploy/dev/oidcstub.yaml) |
| `curl` and `jq` in the client images, with exact apt pins | `tools/versions.env`, `test/e2e/clients`, `test/e2e/nugetclients` (Q-CI-45) |
| `make dev-up` rolls the pods when the loaded image changed | [deploy/dev/up.sh](../../deploy/dev/up.sh) |

### Contract changes

- The OpenAPI document has no `501` answer on the CI operations any more (`NotImplementedCI` is gone); `ci_not_implemented` remains in the
  error codes for a service option left nil (the issuer service of `trove migrate`) and for the task handlers.
- The `nuget.config` of the specification's Examples names `nuget-internal` as a source with credentials (Q-CI-44), found by the first run of
  the pipeline.
