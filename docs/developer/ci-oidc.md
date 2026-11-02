# GitLab CI OIDC federation

Spec [gitlab-ci-oidc.md](../spec/gitlab-ci-oidc.md) (R-CI-001 to R-CI-030), beta phase 5 (M4). The page is the contract between the work packages
that build the federation in parallel, in the shape of [projects.md](projects.md). The decisions behind it are
[Q-CI-17 to Q-CI-22, Q-CI-26 and, from W2, Q-CI-27 to Q-CI-29](../decisions.md) with [ADR-147](../decisions.md#adr-147--a-ci-token-carries-its-frozen-grants-in-its-cache-entry-and-the-management-api-refuses-ci-tokens-by-default).

Status: contracts laid; W1 (issuers and keys, [below](#w1-issuers-and-signing-keys)) and W2 (the claim engine, the subject-token
validation and the shared JWS leaf, [below](#w2-the-claim-engine-and-the-validation)) built; W3 to W7 pending. Every other operation of the
document answers `501 ci_not_implemented` (the exchange too, as problem details until W4). The issuer double the end-to-end tests exchange
against is built separately (test/oidcstub).

## What has to be in your head

- **Two kinds of work, two kinds of package.** The pure parts (conditions, glob, templates, grants, the scope grammar, the save-time rules, the
  validation of a subject token) are leaves with no I/O ([claims](../../internal/ci/claims), [subject](../../internal/ci/subject),
  [reason](../../internal/ci/reason)); the exchange, the dry run and a save evaluate one implementation. The services are in
  [internal/ci](../../internal/ci) behind interfaces; stubs answer until a work package replaces its member in
  [wire_ci.go](../../internal/app/wire_ci.go).
- **PostgreSQL decides.** Issuers, mappings, key sets, the replay set and the issuance records are rows; replicas hold in-process copies
  invalidated through the areas `ci_mappings` and `ci_issuer_keys`; the exchange re-reads issuer and mappings `FOR SHARE` and compares
  `version` and `mappings_version` (Q-CI-15); Valkey holds only the two limiters' counters (R-CI-029).
- **A CI token carries its frozen grants** (Q-CI-17): `claims.Frozen` is written to `api_token.ci_context` by the exchange, read into the token
  cache entry, and travels `token.Info.CI` → `auth.Result.CI` → `httpserver.Principal.CI` → `authz.Subject.CI`. It is per contributing mapping
  (Q-CI-17 as amended 2026-11-02): `Frozen.Contributions` holds one `claims.FrozenMapping` per tier that matched, with its tier, its project
  (key and ID) for a project tier, its groups, its inline grants and its caps; a cap bounds only its own mapping's grants (R-CI-012). Policy and
  group grants stay live through the principal set (`Subject.Mappings`, each contribution's `Groups`). The JSON is versioned
  (`claims.FrozenVersion`); `claims.DecodeFrozen` checks it.
- **The management API refuses CI tokens by default** (Q-CI-18): `ciGate` in [authorize.go](../../internal/api/v1/authorize.go); only public operations,
  the action form and `x-trove-ci: admitted` (`GET` and `DELETE /tokens/current`) admit one. Package protocols and `/files/…` are outside that table.
- **Tiers.** First match inside a tier, union across tiers (R-CI-009). The tier `system` is the administrators'; a project's administrators manage the tier
  `project:<key>` (Q-CI-26), bounded like project policies: inline grants over the project's repositories (the contribution's `Project` makes the evaluator
  compile them against those only), the project's own policies, no groups, never `manage`, a name starting `<key>-`. Issuers are the system administrators' and
  the Helm values' (`ci.issuers`, Q-CI-16); a project administrator lists them read-only.
- **Stored JSON is the wire JSON.** `ci_mappings.match`, `.grants`, `.options` hold the lowerCamelCase forms of the `claims` types.
- **No attribution, no vendor names** in anything the work packages write; the decider of design questions is "the development agent".

## The contracts

### Schema (migrations 00026 to 00028)

| File | What |
|---|---|
| [00026_ci_oidc.sql](../../internal/meta/postgres/migrations/00026_ci_oidc.sql) | `ci_issuers`, `ci_issuer_keys`, `ci_mappings` (with `tier`, `project_id`), `ci_subject_token_uses`, `ci_token_issuances`; the epoch rows of `ci_mappings` and `ci_issuer_keys`; `package_owner.ci_issuer_id → ci_issuers` `NOT VALID` |
| [00027](../../internal/meta/postgres/migrations/00027_ci_oidc_owner_index.sql), [00028](../../internal/meta/postgres/migrations/00028_ci_oidc_validate.sql) | the index `CONCURRENTLY`, the validation (R-HA-018) |
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
| [internal/ci](../../internal/ci) | types (`Issuer`, `IssuerInput`, `KeyStatus`, `Mapping`, `MappingInput`, `MappingOptions`, `MappingSet`, `Issuance`, `IssuanceFilter`, `EvaluateRequest`, `EvaluateResult`), the interfaces `Issuers` (W1), `Mappings` (W3), `Exchanger` (W4), `Revoker`, `Issuances` (W5), `Select`, `ComputeTTL`, `Metrics`, `DeclaredFromConfig`, the stubs |
| [internal/auth/jws](../../internal/auth/jws) | the compact-JWS verifier and JWK sets the login and the CI validation share (Q-CI-19): `Parse`, `Token.VerifyWith`, `Fits`, `ParseJWKS` with the report of ignored keys, `SelectKey` (the login's), `Candidates` (the exchange's), `DecodeObject` (built, W2) |

### Token and evaluation seam (the part W4 and W5 share)

| Step | Where |
|---|---|
| W4 builds the bundle | `Frozen{Version: claims.FrozenVersion, Issuer, IssuerID, ProjectID, ProjectPath, CrossProject}`, then `Frozen.Add(claims.Freeze(ref, id, tier, project, rendered, caps))` per contributing mapping in evaluation order (`Freeze` bounds the inline grants by the mapping's caps), then `claims.NarrowFrozen` with the request's `scope` items; `Frozen.Check` before the write |
| W4 issues the token in the exchange transaction | `token.Service.IssueCITx(ctx, tx, token.CITRequest{OwnerID: mapping principal, Frozen, Scope, TTL})` in [cit.go](../../internal/auth/token/cit.go); `created_via` `ci-exchange`; no user token list, no per-principal limit; the issuance record is written beside it (`CiExchangeInsertIssuance`) with `grants` = the same bundle |
| the validator reads it | `Service.load` decodes `ci_context` into `Info.CI` for type `cit` (a missing bundle is a storage fault, never an acceptance). `decodeCIContext` still uses `json.Unmarshal`; W4 switches it to `claims.DecodeFrozen`, which also refuses another version and an inconsistent bundle |
| the stage and the chain pass it on | `auth.Result.CI`, `Principal.CI` and `Principal.Mappings` ([m1_convert.go](../../internal/app/m1_convert.go)), `authz.Subject.CI` ([edge/subject.go](../../internal/api/edge/subject.go)) (done) |
| W5: the evaluator | [authz/ci.go](../../internal/authz/ci.go): `Index.CIGrants(sub)`, `CIGrantSet.Allows`, `CIGrantSet.Claims`, `Subject.CIProject()`; hook it into `allowedAction` and `factsOf`; the stub allows nothing (fail closed). Per `Frozen.Contributions` entry: its `Inline` compiled against its `Project`'s repositories when the tier is a project's, its `Caps` applied to its inline grants, to the policies naming its `Ref` and to the groups in its `Groups` (resolved by name through the index), and to nothing else. The comments of `authz/ci.go` still name the bundle-wide `Frozen.Groups` of the contracts |
| W5: ownership | `ownerRef` accepts `ci:<issuer>:project:<id>` (resolve the issuer by name), `ownerOfRow` shows the issuer name, `factsOf` adds the project identity and the claim of a `bind_project` write, `ci.package_ownership.*` events |
| W5: revocation | `Revoker` (mapping, issuer, bulk), reasons of the token table, `ci.token.revoked` with the journal entry |

### API

[ci_services.go](../../internal/api/v1/ci_services.go) holds `CIServices` and `ciPending`; one handler file per work package:
[ci_issuers_api.go](../../internal/api/v1/ci_issuers_api.go) (W1), [ci_mappings_api.go](../../internal/api/v1/ci_mappings_api.go) and
[ci_snippets_api.go](../../internal/api/v1/ci_snippets_api.go) (W3), [ci_exchange.go](../../internal/api/v1/ci_exchange.go) (W4),
[ci_issuances_api.go](../../internal/api/v1/ci_issuances_api.go) (W5), [ci_project_api.go](../../internal/api/v1/ci_project_api.go) (the project routes: the
narrowing `requireProject(…, managePolicies)` is real, the work behind it W1, W3, W5). Declarations: `ci:read` and `ci:write` (recent authentication on create, replace, delete),
`tokens:read` and `tokens:revoke` (Q-CI-22); the project routes add `projectAdmin: managePolicies`; the exchange is `public: true` with `security: []`.

| Operation | Path | Package |
|---|---|---|
| issuers | `GET/POST /ci/issuers`, `GET/PUT/DELETE /ci/issuers/{name}`, `GET …/keys`, `POST …/keys/refresh` | W1 |
| mappings, dry run, snippet | `…/ci/issuers/{issuer}/mappings[/{name}]`, `POST …/evaluate`, `GET …/snippets/{kind}` | W3 |
| issuances | `GET /ci/issuances[/{tokenId}]`, `POST /ci/issuances/revoke` | W5 |
| exchange | `POST /api/v1/auth/oidc/token` | W4 |
| a project's | `/projects/{key}/ci/issuers`, `…/ci/mappings[/{issuer}/{name}]`, `…/ci/issuers/{issuer}/evaluate`, `…/ci/issuances` | W1, W3, W5 |

The exchange needs no work in the stage (it answers the anonymous principal for the route, `isExchangeRoute`), but W4 must exempt the route from the
general API rate limit (`limitedPath` in [ratelimit.go](../../internal/api/v1/ratelimit.go)) and answer in the OAuth error shape.

### Configuration, coordination, audit

| Where | What |
|---|---|
| [config/ci.go](../../internal/config/ci.go) | every `ci.*` key with its default; refusals: `ci.token.max_ttl` above `auth.tokens.cit.max_ttl` (both keys named), the retired `ci.exchange.rate_limit.per_issuer_per_second` (file and environment, names the replacements), `ci.job_token_fallback.enabled`; `ci.issuers` (list elements now hold pointers to scalars and string lists, [schema.go](../../internal/config/schema.go)); `trove migrate` calls `ApplyDeclared` always ([migrate_declared.go](../../internal/app/migrate_declared.go)) |
| [cluster/areas.go](../../internal/cluster/areas.go) | `ci_mappings`, `ci_issuer_keys` |
| [app/wire_ci.go](../../internal/app/wire_ci.go) | stubs, limiters `ci_exchange_address` and `ci_exchange_subject` (cluster scope, held in `limitsArea`), tasks `ci-keys-refresh` `*/5 * * * *`, `ci-replay-cleanup` `7 * * * *`, `ci-issuance-retention` `40 3 * * *` (exclusive; a stub's not-implemented answer is a success), the metrics and the two database gauges |
| [audit/events_ci.go](../../internal/audit/events_ci.go) | the events of R-CI-020, categories and modes in Q-CI-22; journal flag on `ci.token.revoked` and the `.updated`, `.disabled`, `.deleted` events of issuers and mappings; the record schema has `project` |
| [errs/codes.go](../../internal/errs/codes.go) | `>>> M4 ci`: `ci_not_implemented` and the management codes |

## Work packages

| Package | Builds | Files it owns |
|---|---|---|
| **W1** issuers and keys (built) | R-CI-001 to R-CI-003, R-CI-005, Q-CI-16; the issuer, key and `Choices` operations; `ApplyDeclared` (migrate: call it always); `ci-keys-refresh`; the key source | `internal/ci/issuers*.go`, `keys*.go`, `ci_issuers_api.go`, `queries/ci_issuers.sql`, the issuer part of `wire_ci.go`, `migrate_declared.go` |
| **W2** claim engine and validation (built) | R-CI-004, R-CI-010 to R-CI-014, the save-time rules and warnings; `internal/auth/jws`; [what it built and the contract changes](#w2-the-claim-engine-and-the-validation) | `internal/ci/claims/**`, `internal/ci/subject/**`, `internal/auth/jws/**`, `internal/auth/oidc/jws*.go` |
| **W3** mappings | store, principals, `mappings_version`, R-CI-009, the system and the project routes, the project bounds, the dry run, `Select`, the snippet; `project_not_empty` for a project with mappings | `internal/ci/mappings*.go`, `select.go`, `ci_mappings_api.go`, `ci_snippets_api.go`, `queries/ci_mappings.sql`, `internal/project` (the delete check) |
| **W4** exchange | R-CI-007, R-CI-008, R-CI-016, R-CI-018, R-CI-021, the issuance write; `IssueCITx` | `internal/ci/exchange*.go`, `ttl.go`, `internal/auth/token/cit.go`, `ci_exchange.go`, `queries/ci_exchange.sql` |
| **W5** use time | R-AUTHZ-014 in the evaluator, R-CI-015 ownership, R-CI-017 revocation, issuances (also a project's), bulk revoke, the two retention tasks, the metrics' increments | `internal/authz/ci.go`, `ownership.go` (CI parts), `internal/ci/revoke*.go`, `issuances*.go`, `ci_issuances_api.go`, `queries/ci_issuances.sql` |
| **W6** admin UI | R-UI-035, R-UI-036, the issuances tab, the project's CI screens. **Built** against the fakes (see [web-ui.md](web-ui.md#ci-identity)); the browser journey is W7's | `web/**` |
| **W7** tests and e2e | the acceptance criteria of R-CI-001 to R-CI-030 against the issuer double (test/oidcstub) | `test/integration/ci/**`, `test/e2e/**` |

Order: W2 first (pure), then W1 and W3, then W4, then W5 and W6; W7 throughout.

### The manual pages

The behaviour is documented for users while it is built (owner, 2026-11-02): [admin/gitlab-ci.md](../manual/admin/gitlab-ci.md) (issuers,
the project administrator's how-tos, dry run, issuances, revocation, every refusal reason, rate limits, the `ci.*` keys) and
[user/gitlab-ci-pipelines.md](../manual/user/gitlab-ci-pipelines.md) (`id_tokens:`, the exchange, `scope`, npm and NuGet, the closing
revocation). A work package that changes a documented path, field, code, default or rule updates the page in the same change.
Both pages open with a **Not available in this version yet** note, and [tokens.md](../manual/admin/tokens.md),
[permissions.md](../manual/admin/permissions.md), [projects.md](../manual/admin/projects.md), [limitations.md](../manual/admin/limitations.md),
[configuration.md](../manual/admin/configuration.md), [access-tokens.md](../manual/user/access-tokens.md),
[client-setup.md](../manual/user/client-setup.md) and [getting-started.md](../manual/user/getting-started.md) say "not released yet"
where they link them; the change that ships the exchange removes those notes. The open point on pinning a group with subgroups
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
- **`Mappings.DeleteForIssuerTx` (W3).** Added to the interface for the cascade of an issuer's deletion; the stub answers 501, so
  `DELETE …?cascade=true` of an issuer that has mappings answers 501 until W3.
- **The revoker (W5).** `Issuers.Replace` (disable), `Delete` and `ApplyDeclared` (removal) call `Revoker.RevokeIssuerTx` in their
  transaction only when the issuer has a row in `ci_token_issuances`. With the stub revoker such a write answers 501 and changes
  nothing (and a `trove migrate` that removes such an issuer stops).
- **The key source (W4).** `Issuers.KeySource(issuer).Keys(ctx, kid, alg)` returns `subject.ErrUnknownKey` and
  `subject.ErrKeysUnavailable`; a token without kid for an issuer with no stored set counts as an unknown kid. `Resolve` answers from
  the copy of all issuers, so an unknown `iss` costs no query. The JWK rules are in keys_jwks.go, not in `internal/auth/jws` (W2
  builds that package in parallel): W2 may replace `parseStored` with its `ParseJWKS` if the two agree.
- **The metric** `trove_ci_signing_keys_refresh_total{issuer,trigger,outcome}` is incremented by every fetch; the gauge
  `trove_ci_signing_keys_age_seconds` skips an issuer whose first fetch failed (the placeholder row, Q-CI-32).

## Pending criteria

Criteria of R-CI-001 and R-CI-002 that need a work package that is not built. The tests exist in
[pending_test.go](../../test/integration/ci/pending_test.go) behind the build tag `ci_pending` (run with
`-tags 'integration ci_pending'`); the work package named removes the tag from its tests when it builds its part.

| Test | Needs |
|---|---|
| `TestR_CI_002_DisablingAnIssuerRevokesItsTokens`, `TestR_CI_002_DeletingAnIssuerRevokesItsTokens` | W3 (a mapping), W4 (the exchange issues a token), W5 (the revoker) |
| `TestR_CI_002_CascadeDeletesTheMappingsAndTheirPrincipals` | W3 (`DeleteForIssuerTx`) |
| `TestR_CI_001_ARemovedConfigurationIssuerKeepsItsRecords` | W5 (the revoker, for an issuer with issuance records) |
| `TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer` | W4 |

The tests of W1 itself are [test/integration/ci](../../test/integration/ci) (`issuers_test.go`, `keys_test.go`, `declared_test.go`,
`tasks_test.go`) and the unit tests of [internal/ci](../../internal/ci).

| `TestR_CI_004_*` of [subject_test.go](../../internal/ci/subject/subject_test.go) | every step of R-CI-004 with a failing token from the issuer double and its reason, the algorithm-confusion token (Q-CI-24), the audience and skew criteria, the step order, at most 10 keys without `kid` |
| `TestR_CI_004_*` of [jws_test.go](../../internal/auth/jws/jws_test.go), `TestTheKeyRulesOfAJWKSet` | every supported algorithm, `none` and HMAC never, key fit, no key from a header, the parser's refusals, R-CI-003's key rules and their reasons |
| `TestR_CI_010_*`, `TestR_CI_011_*`, `TestR_CI_012_*`, `TestR_CI_013_*`, `TestR_CI_014_*`, `TestR_CI_007_*`, `TestR_AUTHZ_014_AnEscapedClaimMatchesOnlyItsLiteralName` ([internal/ci/claims](../../internal/ci/claims)) | the glob table, normalization, arrays, `null` and missing claims, `(a+)+$` (median under 1 ms without the race detector, the p99 with `TROVE_PERF_GATE=1`), the limits, the save-time rules and warnings, the cross-project classification and the per-mapping cap, the grant specification, every template example and render error, the pure scope cases |
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
| [claims/scope.go](../../internal/ci/claims/scope.go), [selector.go](../../internal/ci/claims/selector.go) | `ParseScope`, `Narrow`, `NarrowFrozen`, `FormatScope`, `Frozen.InlineScope`, `Freeze`; the selector syntax of R-AUTHZ-004 (a test holds it equal to `authz.ValidateSelector`) |
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
- **The reason leaf.** `claims.ReasonProjectPrefixRequired`, `ReasonInvalidPattern` and `ReasonInvalidValue` have the names of registered
  `errs` codes; they move to `internal/ci/reason` with the next change of that file.

### Contract changes

- `claims.SaveInput` gained `Name` (the project prefix needs it) and `PolicyActions` (the write rules count policy and group writes,
  which only the service can read).
- `claims.Frozen` is per contributing mapping (Q-CI-17 as amended): `Groups`, `Inline`, `Caps` and `Project` moved into
  `Contributions []FrozenMapping`; `Version`, `Add`, `Check`, `Groups()` and `DecodeFrozen` are new; `Frozen.HasCap` became
  `FrozenMapping.HasCap`. Nothing outside the package read the moved fields.
- Added: `subject.CheckIssuer`, `subject.DefaultMaxBytes`, `subject.JobTokenPrefix`, `claims.ApplyCaps`, `Freeze`, `NarrowFrozen`,
  `Frozen.InlineScope`, `ScopeItem.String`, `Action.Valid`, `InlineGrant.Writes`, `InlineGrant.ResolvedOwnership`, `MaxGlobLength`.
- The regular-expression memo of the first draft is not there: R-ARCH-032 allows no package-level `sync.Map`, so an expression compiles at
  save and again per evaluation (a few microseconds).

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
