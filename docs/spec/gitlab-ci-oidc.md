# GitLab CI OIDC federation

Status: specification — not implemented

This page specifies how a CI job gets trove permissions without a stored secret. A GitLab job
asks GitLab for an OIDC ID token addressed to trove. It exchanges that token at
`POST /api/v1/auth/oidc/token` for a short-lived `trv_cit_` token (INV-8, INV-10). With that
token it installs dependencies and publishes the packages it builds. The page covers the trusted
issuer configuration, ID-token validation, identity mappings (the ordered rules that turn
verified claims into grants), grant templating and package-ownership binding, token lifetime,
replay protection, audit, the optional `CI_JOB_TOKEN` fallback, and GitHub Actions and generic
OIDC issuers as later uses of the same mechanism. The page specifies only the CI side of these
features. Other spec pages own the token store and token presentation
([identity-and-authentication.md](identity-and-authentication.md)), the permission model,
permission policies and the package-ownership store ([authorization.md](authorization.md)), the
audit pipeline ([operations-and-observability.md](operations-and-observability.md)), API
conventions and the UI ([api-and-ui.md](api-and-ui.md)), and client-IP and proxy handling
([architecture.md](architecture.md), [ha-and-helm.md](ha-and-helm.md)).

Covers: [oidc-ci-integration.md](../features/oidc-ci-integration.md) (CIOIDC-001 to CIOIDC-040),
the CI-related entries of [access-tokens.md](../features/access-tokens.md) and
[identity-and-access.md](../features/identity-and-access.md), the OIDC operations in
[api-admin-operations.md](../features/api-admin-operations.md) (ADMAPI-028 to ADMAPI-031), and
the CI identity screen gap in [ui-inventory.md](../features/ui-inventory.md) (UI-049).

## Two GitLab credentials, only one of them is OIDC

A GitLab job can hold two different credentials. They are often mixed up, and only the first is
the subject of the OIDC path:

| | ID token (`id_tokens:` keyword) | `CI_JOB_TOKEN` |
|---|---|---|
| What it is | An OIDC ID token: a JWT that GitLab signs with RS256 using the instance's dedicated signing key | A GitLab credential for calling GitLab itself (API, registries, Git) with the triggering user's access level, restricted to a fixed set of GitLab endpoints |
| Audience | Set by the job (`aud`); trove accepts only its own audience | None. Nothing ties it to a relying party. |
| How a third party verifies it | Offline: signature against the issuer's published JWKS, plus `iss`, `aud`, `exp`, `nbf`, `iat` ([RFC 7519](https://www.rfc-editor.org/rfc/rfc7519)) | Only by calling GitLab (`GET /api/v4/job` with the token), while the job is running |
| Format | JWT (compact JWS) | GitLab-internal. Since GitLab 19.0 job tokens use a JWT format by default (a group can switch back until 20.0), but GitLab documents neither claims nor verification keys for third parties. trove treats it as an opaque string. |
| Lifetime | Expires at the job's timeout, or after 5 minutes if no timeout is set | Valid while the job runs; revoked when the job finishes |
| trove use | The normal path, MVP (R-CI-001 to R-CI-021) | Optional fallback, off by default (R-CI-022) |

Sources: [ID token authentication](https://docs.gitlab.com/ci/secrets/id_token_authentication/),
[CI/CD job token](https://docs.gitlab.com/ci/jobs/ci_job_token/),
[Jobs API, retrieve a job by job token](https://docs.gitlab.com/api/jobs/#retrieve-a-job-by-job-token)
(all read on 2026-10-28).

## Flow

```mermaid
sequenceDiagram
    autonumber
    participant GL as GitLab instance
    participant J as GitLab job (runner)
    participant A as trove (any replica)
    participant PG as PostgreSQL
    participant C as npm / dotnet in the job
    GL->>J: start job, inject TROVE_ID_TOKEN (JWT, aud = trove base URL)
    J->>A: POST /api/v1/auth/oidc/token (RFC 8693, subject_token = ID token)
    A->>A: parse JWT without trusting it, select CI issuer by iss
    A->>PG: load CI issuer and cached signing keys
    opt kid unknown or key set due for refresh
        A->>GL: GET /.well-known/openid-configuration, GET jwks_uri
        A->>PG: store key set, NOTIFY other replicas
    end
    A->>A: verify alg, signature, iss, aud, exp/nbf/iat, required claims
    A->>A: cross-project (fork) guard, evaluate mappings in order, render grants, compute TTL
    A->>PG: one transaction: record jti, insert token hash, issuance record, audit event
    A-->>J: 200 access_token = trv_cit_..., expires_in, scope
    J->>C: export TROVE_TOKEN (.npmrc / nuget.config read it from the environment)
    C->>A: npm publish / dotnet nuget push (Bearer or X-NuGet-ApiKey)
    A->>PG: resolve token hash, authorize (grants, ownership), audit with token_id
```

Terms used on this page:

- **CI issuer**: a trusted external token issuer (one GitLab instance, GitHub Actions, or
  another OIDC issuer) and its validation settings. This is not the same as the OIDC provider
  that humans log in through (INV-7).
- **Identity mapping**: an ordered rule of one CI issuer. It has a claim condition and a grant
  specification.
- **Subject token**: the token the job presents (RFC 8693 term): normally the GitLab ID token.
- **CI token**: the `trv_cit_` token that trove issues.
- **Issuance record**: the stored link between a CI token and the verified job identity it was
  issued to.

## Requirements

### R-CI-001 · CI issuer configuration
Priority: **MVP** · Covers: CIOIDC-001, CIOIDC-002

trove MUST let a system administrator register any number of CI issuers. A CI issuer has a
unique name (`^[a-z0-9][a-z0-9._-]{1,63}$`), a kind (`gitlab` in the MVP; `github` and
`generic` in P1, see R-CI-025 and R-CI-026) and an issuer URL. The issuer URL MUST be an
`https` URL and MUST be unique across CI issuers, so that the `iss` claim of a token selects at
most one CI issuer (R-CI-005). For GitLab, the issuer URL is the instance's base URL, for example
`https://gitlab.com` or `https://gitlab.example.com`.

Settings of a CI issuer (API field names; durations over the API are integer seconds, so units
cannot be mixed up):

<details>
<summary>CI issuer settings (22)</summary>

| Field | Meaning | Value |
|---|---|---|
| `name` | Handle in the API, audit and metrics | — |
| `kind` | `gitlab`, `github`, `generic` | — |
| `issuer` | Expected `iss`, compared exactly (no trailing-slash or case normalization) | — |
| `discovery_url` | OIDC discovery document | `null` = `<issuer>/.well-known/openid-configuration` # default (proposed) |
| `jwks_uri` | Overrides the key-set URL from discovery | `null` # default (proposed) |
| `allowed_audiences` | Accepted `aud` values, exact string match | `[<server.base_url>]` # default (proposed) |
| `reject_multiple_audiences` | Reject tokens whose `aud` array has more than one element | `true` # default (proposed) |
| `allowed_algorithms` | JWS algorithms accepted | `["RS256"]` # default (proposed) for `gitlab` and `github` |
| `allowed_typ` | Accepted `typ` header values, compared case-insensitively | `["JWT"]` # default (proposed) |
| `allow_missing_typ` | Accept tokens without a `typ` header | `true` # default (proposed) |
| `clock_skew_seconds` | Leeway for `exp`, `nbf`, `iat`; at most 300 | `60` # default (proposed) |
| `required_claims` | Claims that must be present and non-null | kind preset, see R-CI-006 |
| `public_issuer` | Anyone can obtain tokens from this issuer (e.g. GitLab.com, or any instance with open sign-up). Turns on the ID-pinning rule of R-CI-010. | `true` # default (proposed) |
| `pin_claims` | Claims that count as immutable identifiers for the pinning rule | `gitlab`: `project_id`, `namespace_id`, `job_project_id`, `job_namespace_id` # default (proposed) |
| `single_use_subject_tokens` | Reject a second exchange of the same `jti` (R-CI-018) | `false` # default (proposed) |
| `cross_project_source` | Default handling of cross-project (fork) pipelines (R-CI-012) | `deny` # default (proposed) |
| `max_ttl_seconds` | Upper bound for CI tokens of this issuer, at most `ci.token.max_ttl` | `null` (global bound only) # default (proposed) |
| `ca_bundle_ref` | Reference to a PEM bundle (file or environment variable, INV-15) for instances behind a private CA | `null` # default (proposed) |
| `use_outbound_proxy` | Route discovery, key and GitLab API calls through the system outbound proxy ([repositories-and-proxy.md](repositories-and-proxy.md)) | `false` # default (proposed) |
| `job_token_fallback` | R-CI-022 settings | `{"enabled": false}` # default (proposed) |
| `enabled` | Disabled issuers reject every exchange | `true` # default (proposed) |
| `description` | Free text | `""` |

</details>

Several GitLab instances are several CI issuers (R-CI-005). trove MUST NOT accept an
`alg` of `none` or any HMAC algorithm (`HS256`, `HS384`, `HS512`) in `allowed_algorithms`
([RFC 8725 §3.1, §3.2](https://www.rfc-editor.org/rfc/rfc8725#section-3.1)). Only
`RS256/384/512`, `PS256/384/512`, `ES256/384` and `EdDSA` are valid entries.

`pin_claims` of a `gitlab` or `github` CI issuer MUST be a subset of that kind's default
`pin_claims` (the ID claims in the table above and in R-CI-025). Any other claim answers 422
`pin_claim_not_immutable`. A path or name claim in `pin_claims` would satisfy the pinning rule of
R-CI-005 with exactly the claims that rule exists to exclude. For an issuer URL that is public by
construction (`https://gitlab.com`, `https://token.actions.githubusercontent.com`),
`public_issuer: false` answers 422 `public_issuer_required`.

**Data model**

```sql
CREATE TABLE ci_issuers (
  id                         uuid PRIMARY KEY,
  name                       text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
  kind                       text NOT NULL CHECK (kind IN ('gitlab', 'github', 'generic')),
  issuer                     text NOT NULL UNIQUE CHECK (issuer ~ '^https://'),
  discovery_url              text,
  jwks_uri                   text,
  allowed_audiences          text[] NOT NULL CHECK (cardinality(allowed_audiences) >= 1),
  reject_multiple_audiences  boolean NOT NULL DEFAULT true,
  allowed_algorithms         text[] NOT NULL DEFAULT '{RS256}',
  allowed_typ                text[] NOT NULL DEFAULT '{JWT}',
  allow_missing_typ          boolean NOT NULL DEFAULT true,
  clock_skew                 interval NOT NULL DEFAULT '60 seconds'
                               CHECK (clock_skew <= interval '300 seconds'),
  required_claims            text[] NOT NULL,
  public_issuer              boolean NOT NULL DEFAULT true,
  pin_claims                 text[] NOT NULL,
  single_use_subject_tokens  boolean NOT NULL DEFAULT false,
  cross_project_source       text NOT NULL DEFAULT 'deny'
                               CHECK (cross_project_source IN ('deny', 'read_only')),
  max_ttl                    interval,
  ca_bundle_ref              text,
  use_outbound_proxy         boolean NOT NULL DEFAULT false,
  job_token_fallback         jsonb NOT NULL DEFAULT '{"enabled": false}',
  enabled                    boolean NOT NULL DEFAULT true,
  description                text NOT NULL DEFAULT '',
  version                    bigint NOT NULL DEFAULT 1,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  updated_at                 timestamptz NOT NULL DEFAULT now()
);
```

**Acceptance criteria**

- Creating a second CI issuer with `issuer = https://gitlab.com` fails with 409.
- Creating a CI issuer with `allowed_algorithms: ["HS256"]` or `["none"]` fails with 422.
- Creating a CI issuer with `issuer = http://gitlab.example.com` fails with 422.
- Creating a `gitlab` CI issuer with `pin_claims: ["project_path"]` fails with 422
  `pin_claim_not_immutable`. Creating one with `issuer = https://gitlab.com` and
  `public_issuer: false` fails with 422 `public_issuer_required`.

### R-CI-002 · CI issuer management API
Priority: **MVP** · Covers: CIOIDC-005, ADMAPI-028

All operations require the system administrator role (INV-9). Pagination, error envelope and
optimistic concurrency follow [api-and-ui.md](api-and-ui.md). An update carries the `version`
it read, and a stale `version` answers 409.

| Method and path | Purpose | Success | Errors |
|---|---|---|---|
| `GET /api/v1/ci/issuers` | List CI issuers with mapping count and key status | 200 | 401, 403 |
| `POST /api/v1/ci/issuers` | Create; validates discovery and keys unless `"validate": false` | 201 + record | 409 duplicate name or issuer, 422 invalid or unreachable |
| `GET /api/v1/ci/issuers/{name}` | Read | 200 | 404 |
| `PUT /api/v1/ci/issuers/{name}` | Replace; `name` and `issuer` are immutable | 200 | 404, 409, 422 |
| `DELETE /api/v1/ci/issuers/{name}` | Delete; refuses while mappings exist unless `?cascade=true` | 204 | 404, 409 |
| `GET /api/v1/ci/issuers/{name}/keys` | Cached key set: `kid`, `kty`, `alg`, fetch time, next refresh, last error | 200 | 404 |
| `POST /api/v1/ci/issuers/{name}/keys/refresh` | Force a discovery and key refresh now | 200 + key status | 404, 502 issuer unreachable |

Request sketch for `POST /api/v1/ci/issuers`:

```json
{
  "name": "gitlab-com",
  "kind": "gitlab",
  "issuer": "https://gitlab.com",
  "allowed_audiences": ["https://packages.example.com"],
  "description": "GitLab.com, groups acme and acme-labs",
  "validate": true
}
```

The response is the full record with all defaults filled in, plus `version`, `created_at` and
`updated_at`. A create with `"validate": true` (the default) fetches the discovery document and
the key set synchronously. It fails with 422 and a `reason` (`discovery_unreachable`,
`issuer_mismatch`, `no_usable_keys`, `algorithms_not_offered`) when the issuer would not be
usable.

Disabling (`enabled: false`) or deleting a CI issuer revokes all unexpired CI tokens issued
through it, in the same transaction (R-CI-017).

**Acceptance criteria**

- `curl -sS -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' --data @issuer.json https://packages.example.com/api/v1/ci/issuers`
  with the request above returns 201, and `GET .../keys` lists at least one RSA key.
- A non-administrator PAT receives 403 on every operation in the table.
- After `PUT` with `"enabled": false`, a CI token issued earlier through the issuer gets 401 on
  `npm view @acme-billing/invoice-client` against trove.

### R-CI-003 · Discovery, signing keys, caching and rotation
Priority: **MVP** · Covers: CIOIDC-019, CIOIDC-031

Discovery: trove MUST fetch the discovery document from `discovery_url`
([OpenID Connect Discovery 1.0 §4](https://openid.net/specs/openid-connect-discovery-1_0.html#ProviderConfig)).
It MUST reject a document whose `issuer` is not exactly the configured issuer (§4.3) and MUST
take `jwks_uri` from it unless the CI issuer overrides `jwks_uri`. The `jwks_uri` MUST be an
`https` URL. When `id_token_signing_alg_values_supported` does not intersect
`allowed_algorithms`, validation fails with `algorithms_not_offered`. GitLab.com publishes
`https://gitlab.com/.well-known/openid-configuration` with `jwks_uri`
`https://gitlab.com/oauth/discovery/keys` and RS256 (fetched 2026-10-28). Self-managed
instances serve the same paths under their own base URL.

Key set ([RFC 7517](https://www.rfc-editor.org/rfc/rfc7517)): trove MUST use only keys that
meet all of these conditions:

- `use` is absent or `sig`.
- `key_ops` is absent or contains `verify`.
- `kty` matches the algorithm family.
- RSA keys have at least 2048 bits.
- A key's own `alg`, when present, equals the token's `alg`.

Other keys are ignored and reported in the key status.

Caching and refresh:

- The last good key set of each CI issuer is stored in PostgreSQL and held in memory on every
  replica. A refresh on any replica sends `NOTIFY ci_issuer_keys` and the other replicas reload
  from the database (INV-12). A restarted replica validates tokens without contacting GitLab.
- Scheduled refresh: a background job refreshes keys whose `next_refresh_at` has passed. It
  claims rows with `FOR UPDATE SKIP LOCKED`, so exactly one replica fetches. The next refresh
  time follows the response's `Cache-Control: max-age`, clamped to
  `[ci.signing_keys.min_refresh_interval, ci.signing_keys.max_refresh_interval]`, and is
  `default_refresh_interval` when no `max-age` is present.
- Unknown `kid`: trove MUST refresh on demand, at most once per
  `ci.signing_keys.unknown_kid_refresh_interval` per CI issuer and replica. If the `kid` is
  still unknown, it rejects the token with `unknown_signing_key`. This lets a key rotation take
  effect within seconds. Random `kid` values cannot turn trove into a request amplifier
  against GitLab.
- Removed keys: a key that is missing from the latest successful fetch is no longer accepted.
  Removing a key from the JWKS is how an issuer revokes it.
- Fetch failures: trove keeps using the last good key set. Once it is older than
  `ci.signing_keys.max_stale`, trove fails closed: exchanges answer 503
  `signing_keys_unavailable`. Failures are recorded in `last_error` and in metrics (R-CI-020).
- Outbound requests use TLS with the system roots plus `ca_bundle_ref`. They follow at most 3
  redirects, and only to `https` URLs. Responses are limited to
  `ci.signing_keys.max_response_bytes` and requests time out after
  `ci.signing_keys.fetch_timeout`. URLs come only from the CI issuer record and never from a
  token header: `jku`, `x5u`, `jwk` and `x5c` headers are ignored
  ([RFC 8725 §3.10](https://www.rfc-editor.org/rfc/rfc8725#section-3.10)).
- Every hop, including a `jwks_uri` taken from a discovery document and every redirect target,
  passes the egress address policy of the outbound client factory
  ([architecture.md](architecture.md) R-ARCH-022, with the address rules of
  [repositories-and-proxy.md](repositories-and-proxy.md) R-REPO-038). `last_error`, the key-status
  API and the `ci.keys.refresh_failed` event record the failure class, the HTTP status and the
  URL's host, never bytes of a response body. A discovery document that points `jwks_uri` at an
  internal service therefore cannot read that service's answer back through trove.

**Data model**

```sql
CREATE TABLE ci_issuer_keys (
  issuer_id        uuid PRIMARY KEY REFERENCES ci_issuers(id) ON DELETE CASCADE,
  jwks             jsonb NOT NULL,          -- last good key set, public keys only
  jwks_uri         text NOT NULL,           -- where it came from
  fetched_at       timestamptz NOT NULL,
  etag             text,
  next_refresh_at  timestamptz NOT NULL,
  last_error       text,
  last_error_at    timestamptz
);
CREATE INDEX ci_issuer_keys_due ON ci_issuer_keys (next_refresh_at);
```

**Configuration**

```yaml
ci:
  signing_keys:
    default_refresh_interval: 1h        # default (proposed)
    min_refresh_interval: 5m            # default (proposed)
    max_refresh_interval: 24h           # default (proposed)
    unknown_kid_refresh_interval: 60s   # default (proposed)
    max_stale: 24h                      # default (proposed)
    fetch_timeout: 5s                   # default (proposed)
    max_response_bytes: 1048576         # default (proposed)
```

**Acceptance criteria**

- In a test with a local OIDC issuer double, rotating to a new key and signing a token with it
  causes exactly one on-demand fetch. The token is accepted. 100 further tokens with random
  `kid` values within 60 s cause no further fetch.
- Removing a key from the double's JWKS and forcing a refresh makes tokens signed with that key
  fail with `unknown_signing_key`.
- With the double unreachable, exchanges keep working on cached keys until `max_stale`, then
  answer 503 `signing_keys_unavailable`.
- A token whose header carries `jku: https://attacker.example/keys` makes no request to
  `attacker.example`.
- A discovery double whose `jwks_uri` is `https://169.254.169.254/keys` causes no connection to
  that address, and `GET …/keys` shows `last_error` with class `egress_denied`. A `jwks_uri` that
  answers 200 with a non-JSON body leaves no byte of that body in `last_error`.

### R-CI-004 · Subject-token validation
Priority: **MVP** · Covers: CIOIDC-019, CIOIDC-031, CIOIDC-032

trove MUST validate a subject token in this order and MUST stop at the first failure. The
reason code in brackets appears in the error response and the audit event (R-CI-008).

1. Size: at most `ci.exchange.max_subject_token_bytes` [`subject_token_malformed`].
2. Shape: compact JWS with three base64url parts. JWE (five parts), unsecured JWTs and nested
   JWTs are rejected [`subject_token_malformed`]. A value starting with `glcbt-` (GitLab's job
   token prefix) is rejected as [`wrong_subject_token_type`] with a hint to use `id_tokens:`.
3. Header: `alg` in `allowed_algorithms` [`algorithm_not_allowed`]; `typ` in `allowed_typ`,
   or absent with `allow_missing_typ` [`token_type_not_allowed`]; no `crit` parameter [`subject_token_malformed`]; `kid`, when
   present, at most 256 characters and used only as a lookup key.
4. Payload: valid UTF-8 JSON object without duplicate member names
   ([RFC 7519 §4](https://www.rfc-editor.org/rfc/rfc7519#section-4))
   [`subject_token_malformed`].
5. Issuer: the unverified `iss` selects the CI issuer (R-CI-005) [`unknown_issuer`]. A disabled
   CI issuer answers [`issuer_disabled`]. Nothing else from the unverified payload is used
   before step 6.
6. Signature: verified with the key named by `kid`. Without `kid`, every usable key of the
   matching key type is tried, at most 10 [`signature_invalid`, `unknown_signing_key`].
7. Audience: `aud` is a string or an array of strings. At least one element MUST equal an entry
   of `allowed_audiences` [`audience_invalid`]. With `reject_multiple_audiences`, an array of
   more than one element is rejected [`audience_not_exclusive`]
   ([RFC 8725 §3.9](https://www.rfc-editor.org/rfc/rfc8725#section-3.9)).
8. Time ([RFC 7519 §4.1.4–4.1.6](https://www.rfc-editor.org/rfc/rfc7519#section-4.1.4)): `exp`
   required and `now < exp + skew` [`token_expired`]; `nbf`, when present,
   `now >= nbf - skew` [`token_not_yet_valid`]; `iat` required and `iat <= now + skew`
   [`issued_in_future`]. NumericDate values may be non-integer.
9. Required claims: every claim in `required_claims` is present and not `null`
   [`required_claim_missing`, with the claim name in `error_description`].

GitLab sets `nbf` a few seconds before `iat` in its documented example. The default 60-second
skew absorbs this and normal clock drift between runners, GitLab and trove.

**Acceptance criteria**

- Unit tests cover each step with a failing token and assert the reason code. One case is a
  token signed with HS256 using the RSA public key's bytes as the HMAC secret (the
  algorithm-confusion attack), which must fail with `algorithm_not_allowed`.
- A GitLab ID token whose `aud` is `["https://packages.example.com", "https://vault.example.com"]`
  is rejected with `audience_not_exclusive` under the default settings.
- A token with `exp = now - 30` is accepted with a 60-second skew. A token with `exp = now - 61`
  is rejected.

### R-CI-005 · Several GitLab instances and public issuers
Priority: **MVP** · Covers: CIOIDC-001, CIOIDC-002, CIOIDC-033

- Each GitLab instance is its own CI issuer with its own mappings. A token from one instance can
  never be evaluated against the mappings of another, because the CI issuer is selected by
  exact `iss` and verified with that issuer's keys only.
- The exchange request MAY name the CI issuer (`ci_issuer` parameter, R-CI-007). When it does,
  the name MUST match the issuer selected by `iss`, otherwise [`issuer_mismatch`]. The parameter
  exists for clarity in scripts and is required only for the job-token fallback (R-CI-022),
  whose subject token carries no `iss`.
- Claim values such as `project_id` and `namespace_id` are only unique within one GitLab
  instance. Everything that stores them stores the CI issuer with them: issuance records,
  ownership owner keys (R-CI-015) and delegations (R-CI-023).
- Public issuers: anyone with an account on GitLab.com (or on any instance with open sign-up)
  can obtain an ID token with any `aud`. So the audience check proves nothing about who the
  caller is. For a CI issuer with `public_issuer: true`, every mapping MUST be pinned. That
  means its condition MUST contain, at the top level of an `all` combinator, a leaf with
  `equals` or `in` on one of the issuer's `pin_claims` (R-CI-010). Path claims alone are not
  enough: GitLab paths change on rename and transfer and can later be registered by someone
  else. GitLab's own guidance is to prefer `project_id` and `namespace_id` over path claims.

**Acceptance criteria**

- With CI issuers `gitlab-com` and `gitlab-internal` configured, a gitlab.com token whose claims
  equal those of an internal mapping matches only `gitlab-com` mappings.
- Saving a mapping for `gitlab-com` whose condition is only
  `{"claim": "namespace_path", "glob": "acme/**"}` fails with 422 `pin_required`.

### R-CI-006 · GitLab job configuration contract
Priority: **MVP** · Covers: CIOIDC-030, CIOIDC-031, CIOIDC-032, CIOIDC-033, CIOIDC-034

The GitLab side needs only an `id_tokens:` entry whose `aud` equals trove's base URL
(INV-10):

```yaml
publish:
  id_tokens:
    TROVE_ID_TOKEN:                       # example: any variable name works
      aud: https://packages.example.com     # example: one of the CI issuer's allowed_audiences
```

- `aud` SHOULD be a single string. GitLab also accepts an array, which delivers the same token to
  several services. trove rejects such tokens by default (R-CI-004 step 7).
- `id_tokens` may be set under `default:`. A job that declares its own `id_tokens` replaces the
  default entirely; GitLab does not merge them
  ([CI/CD YAML reference, `id_tokens`](https://docs.gitlab.com/ci/yaml/#id_tokens)).
- The predecessors `CI_JOB_JWT` and `CI_JOB_JWT_V2` were removed in GitLab 17.0 (catalog
  CIOIDC-030) and are not supported.
- trove never parses `sub`. Conditions SHOULD use the separate claims (`project_id`,
  `ref_type`, `ref`, …) and not a `sub` string, because a project can reconfigure the
  components of `sub`. Git forbids `:` in ref names
  ([git-check-ref-format](https://git-scm.com/docs/git-check-ref-format)), so the default `sub`
  format is unambiguous. It still adds nothing over the separate claims.

Claims trove relies on (GitLab serializes IDs and booleans as JSON strings; R-CI-010 normalizes
them):

| Claim | Required in the `gitlab` preset | Used for |
|---|---|---|
| `iss`, `aud`, `exp`, `iat` | yes | R-CI-004 |
| `nbf` | no (checked when present) | R-CI-004 |
| `jti` | yes | Replay tracking (R-CI-018) |
| `sub` | yes | Audit only |
| `project_id`, `namespace_id` | yes | Pinning, ownership key (R-CI-015) |
| `project_path`, `namespace_path` | yes | Conditions, templating, display |
| `job_project_id`, `job_project_path`, `job_namespace_id`, `job_namespace_path` | no (GitLab 18.4+) | Cross-project guard (R-CI-012) |
| `source_project_id`, `source_project_path` | no (GitLab 19.5+) | Cross-project guard (R-CI-012) |
| `ref`, `ref_type`, `ref_path`, `ref_protected` | yes (`ref_path` no) | Conditions (R-CI-011) |
| `pipeline_source`, `pipeline_id`, `job_id` | yes | Conditions, audit |
| `environment`, `environment_protected`, `deployment_tier`, `environment_action` | no (only for jobs with an environment) | Conditions (R-CI-011) |
| `user_id`, `user_login` | yes | Audit, user-bound grants (R-CI-024) |
| `sha`, `runner_id`, `runner_environment`, `project_visibility` | no | Conditions, audit |

**Acceptance criteria**

- A job with the YAML above, run against the reference GitLab test instance, exchanges
  successfully. The same job with `aud: https://other.example.com` fails with
  `audience_invalid`.
- A job that sends `$CI_JOB_TOKEN` as `subject_token` with the ID-token type gets a 400. The
  reason is `wrong_subject_token_type` or `subject_token_malformed` for the legacy format, and
  `unknown_issuer`, `unknown_signing_key`, `signature_invalid` or `audience_invalid` for the
  JWT format, depending on what that undocumented format contains. It never gets a 5xx and
  never gets a token.

### R-CI-007 · Token exchange endpoint
Priority: **MVP** · Covers: CIOIDC-018, ADMAPI-031

`POST /api/v1/auth/oidc/token` implements OAuth 2.0 Token Exchange
([RFC 8693](https://www.rfc-editor.org/rfc/rfc8693)). It requires no trove credential: the
subject token is the credential. Other methods answer 405.

Request content types: `application/x-www-form-urlencoded` (RFC 8693 §2.1, normative) and
`application/json` (an object with the same parameter names and string values, for scripts that
already send JSON). Any other content type answers 400 [`unsupported_content_type`]. A parameter
sent twice answers 400 ([RFC 6749 §3.2](https://www.rfc-editor.org/rfc/rfc6749#section-3.2)).

| Parameter | Required | Accepted values |
|---|---|---|
| `grant_type` | yes | `urn:ietf:params:oauth:grant-type:token-exchange` |
| `subject_token` | yes | The GitLab ID token (or, R-CI-022, the job token) |
| `subject_token_type` | yes | `urn:ietf:params:oauth:token-type:id_token` or `urn:ietf:params:oauth:token-type:jwt` (equivalent); `urn:trove:token-type:gitlab-job-token` only with R-CI-022 enabled |
| `requested_token_type` | no | Absent or `urn:ietf:params:oauth:token-type:access_token` |
| `scope` | no | Narrows the grants, space-delimited items `<action>` or `<action>:<repoKey>`, e.g. `read deploy:npm-internal`. Grammar to be aligned with R-AUTH, see Q-CI-2. |
| `audience`, `resource` | no | When present, MUST equal trove's configured base URL; otherwise 400 `invalid_target` |
| `actor_token`, `actor_token_type` | no | Not supported: present → 400 [`actor_token_not_supported`] |
| `ci_issuer` (trove extension) | no | CI issuer name; must match the issuer selected by `iss` (R-CI-005) |
| `ttl` (trove extension) | no | Requested lifetime in seconds; can only shorten (R-CI-016) |

Processing order:

1. Parse and validate the request.
2. Validate the subject token (R-CI-004).
3. Record the `jti` (R-CI-018).
4. Run the cross-project guard (R-CI-012).
5. Select mappings (R-CI-009).
6. Render grants (R-CI-013, R-CI-014).
7. Apply caps (cross-project, fallback).
8. Intersect with `scope`.
9. Compute the TTL (R-CI-016).
10. Issue the token.

Step 3 and step 10 run in one PostgreSQL transaction together with the issuance record and the
audit event. A failed transaction leaves no trace of the `jti`, so a client retry is safe even
with single-use enforcement.

In that transaction trove MUST re-read the CI issuer row and every matched mapping row with
`FOR SHARE` and confirm that each is still enabled and still carries the `version` the evaluation
used. Disabling, deleting or editing a CI issuer or mapping takes `FOR UPDATE` on its row. If a row
changed, the exchange discards its result, reloads the configuration and evaluates once more; a
second mismatch answers 503 [`configuration_changed`]. Without this check, a replica whose mapping
cache has not yet received the `NOTIFY` (R-CI-009) could issue a token under a mapping that was
just disabled, deleted or narrowed, and the revocation of R-CI-017 would not catch it, because the
token did not exist when the revocation ran.

Success response (200, `Content-Type: application/json`, `Cache-Control: no-store`,
`Pragma: no-cache` per [RFC 6749 §5.1](https://www.rfc-editor.org/rfc/rfc6749#section-5.1)):

```json
{
  "access_token": "trv_cit_3kqN8sV1pTz0WmYbR4cD7fHj2LxQe9GuA5oKs6Bn1Vw0aB3cD",
  "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
  "token_type": "Bearer",
  "expires_in": 900,
  "scope": "read:npm-all read:nuget-all read:npm-internal deploy:npm-internal read:nuget-internal deploy:nuget-internal",
  "ci_issuer": "gitlab-com",
  "mappings": ["acme-release-publish"]
}
```

- `scope` is always returned. It summarizes the granted actions per repository; package and path
  patterns are not shown. The authoritative grant is the issuance record (R-CI-020).
- No `refresh_token` is ever returned. CI tokens cannot be refreshed. A job that needs a new
  token exchanges its ID token again.
- The token value appears only in this response. trove stores only its SHA-256 (INV-8).

**Configuration**

```yaml
ci:
  exchange:
    enabled: true                    # default (proposed)
    expose_reason: true              # default (proposed); R-CI-008
    max_body_bytes: 65536            # default (proposed)
    max_subject_token_bytes: 16384   # default (proposed)
```

**Acceptance criteria**

- Form-encoded exchange:

  ```sh
  printf '%s' "$TROVE_ID_TOKEN" | curl -sS https://packages.example.com/api/v1/auth/oidc/token \
    --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
    --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
    --data-urlencode 'subject_token@-'
  ```

  returns 200 with the fields above and `Cache-Control: no-store`.
- The same request as JSON (`-H 'Content-Type: application/json' -d '{"grant_type": …}'`)
  returns an equivalent response.
- `scope=deploy:nuget-internal` from a job whose mapping grants only npm returns 400
  `invalid_scope`.
- Two parallel exchanges of one ID token on two replicas both succeed under
  `single_use_subject_tokens: false`. Under `true`, exactly one succeeds.
- With the `LISTEN` session of replica B blocked, mapping `acme-release-publish` is disabled
  through replica A. An exchange on B whose claims match that mapping issues no token carrying its
  grants: after the reload it answers `no_matching_mapping`, or the grants of a later mapping.

### R-CI-008 · Exchange errors and diagnostics
Priority: **MVP** · Covers: CIOIDC-018

Errors use the OAuth error response of
[RFC 6749 §5.2](https://www.rfc-editor.org/rfc/rfc6749#section-5.2). RFC 8693 §2.2.2 requires
`invalid_request` for a subject token that is invalid or unacceptable by policy, so a rejected
token answers 400, not 401 (Q-CI-8). trove adds a stable machine-readable `reason`:

```json
{
  "error": "invalid_request",
  "error_description": "no identity mapping of CI issuer gitlab-com matched the token",
  "reason": "no_matching_mapping"
}
```

| HTTP | `error` | `reason` values |
|---|---|---|
| 400 | `invalid_request` | `malformed_request`, `unsupported_content_type`, `unsupported_subject_token_type`, `unsupported_requested_token_type`, `actor_token_not_supported`, `subject_token_malformed`, `wrong_subject_token_type`, `unknown_issuer`, `issuer_disabled`, `issuer_mismatch`, `algorithm_not_allowed`, `token_type_not_allowed`, `unknown_signing_key`, `signature_invalid`, `audience_invalid`, `audience_not_exclusive`, `token_expired`, `token_not_yet_valid`, `issued_in_future`, `required_claim_missing`, `subject_token_replayed`, `cross_project_source_denied`, `no_matching_mapping`, `grant_render_failed`, `subject_token_expires_too_soon`, `job_token_rejected`, `job_not_running` |
| 400 | `unsupported_grant_type` | `grant_type_unsupported` |
| 400 | `invalid_scope` | `scope_malformed`, `scope_not_granted` |
| 400 | `invalid_target` | `audience_not_this_server` |
| 429 | `slow_down` | `rate_limited` (with `Retry-After`) |
| 503 | `temporarily_unavailable` | `signing_keys_unavailable`, `gitlab_unreachable`, `database_unavailable`, `configuration_changed` |
| 500 | `server_error` | `internal` |

- `error_description` contains no claim values and no token material. Details go to the audit
  event (R-CI-020).
- With `ci.exchange.expose_reason: false`, every 400 answers `invalid_request` without `reason`
  and with a generic description. Logs and audit still record the reason.

**Acceptance criteria**

- Every reason code in the table has an integration test that triggers it and asserts status,
  `error` and `reason`.
- No response body or log line produced by these tests contains the subject token or any
  `trv_cit_` value (checked with a grep over the captured output).

### R-CI-009 · Identity mapping model and evaluation order
Priority: **MVP** · Covers: CIOIDC-007, CIOIDC-009

An identity mapping belongs to one CI issuer and consists of:

- a name, unique within the issuer;
- a `priority`, a positive integer that is unique within the issuer's tier;
- `enabled`;
- `subject_kinds`: `["id_token"]` by default; R-CI-022 adds `job_token`;
- a condition `match` (R-CI-010);
- a grant specification `grants` (R-CI-013);
- an optional `max_ttl_seconds`;
- `options`: `cross_project_source` and `allow_unprotected_refs`;
- a description.

Evaluation per exchange:

1. Consider enabled mappings of the selected CI issuer whose `subject_kinds` include the subject
   token's kind.
2. Group them into tiers. The MVP has one tier, `system` (administrator mappings). P1 adds one
   tier per delegation (R-CI-023).
3. Within a tier, evaluate in ascending `priority`. The first mapping whose condition is true is
   that tier's match. Later mappings of the tier are not evaluated.
4. The token's grants are the union of the tier matches. In the MVP that is exactly one mapping.
5. No match in any tier → 400 [`no_matching_mapping`].
6. If rendering the grants of a matched mapping fails (R-CI-014), the exchange fails
   [`grant_render_failed`]. Evaluation does not fall through to the next mapping, because a
   silent fall-through would hand out a different, unintended grant.

First match within a tier allows narrow rules at low priority numbers to shape what broader rules
below them give (for example, "merge request pipelines of acme: read only" before "acme
protected tags: publish"). Whether to use the union of all matches instead is Q-CI-1.

Limits: at most `ci.mappings.max_per_issuer` mappings per CI issuer.

**Data model**

```sql
CREATE TABLE ci_mappings (
  id             uuid PRIMARY KEY,
  issuer_id      uuid NOT NULL REFERENCES ci_issuers(id) ON DELETE RESTRICT,
  tier           text NOT NULL DEFAULT 'system',      -- 'system' or 'delegation:<uuid>' (P1)
  delegation_id  uuid,                                -- R-CI-023, NULL for the system tier
  name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,63}$'),
  priority       integer NOT NULL CHECK (priority BETWEEN 1 AND 1000000),
  enabled        boolean NOT NULL DEFAULT true,
  subject_kinds  text[] NOT NULL DEFAULT '{id_token}',
  match          jsonb NOT NULL,
  grants         jsonb NOT NULL,
  max_ttl        interval,
  options        jsonb NOT NULL DEFAULT '{}',
  description    text NOT NULL DEFAULT '',
  version        bigint NOT NULL DEFAULT 1,
  created_by     text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (issuer_id, name),
  UNIQUE (issuer_id, tier, priority)
);
```

Mappings are cached in memory per replica and invalidated with `NOTIFY ci_mappings` (INV-12).

**Configuration**

```yaml
ci:
  mappings:
    max_per_issuer: 1000   # default (proposed)
```

**Acceptance criteria**

- Mappings `p10` (matches merge request pipelines, read only) and `p20` (matches protected tags,
  publish): a protected-tag job gets `p20`'s grants, and a merge request job gets only `p10`'s.
- A matched mapping whose template references an absent claim fails with
  `grant_render_failed`, even if a later mapping would match.
- Two mappings with equal `priority` in one tier cannot be saved (409).

### R-CI-010 · Claim conditions: matchers and combinators
Priority: **MVP** · Covers: CIOIDC-008

A condition is a tree of combinators and leaves:

```json
{
  "all": [
    {"claim": "namespace_id", "in": ["4711", "4712"]},
    {"claim": "project_path", "glob": "acme/**"},
    {"claim": "ref_type", "equals": "tag"},
    {"claim": "ref", "regex": "v[0-9]+\\.[0-9]+\\.[0-9]+"},
    {"claim": "ref_protected", "equals": true},
    {"any": [
      {"claim": "pipeline_source", "equals": "push"},
      {"claim": "pipeline_source", "equals": "web"}
    ]},
    {"not": {"claim": "environment_action", "in": ["prepare", "verify", "access"]}}
  ]
}
```

Combinators: `all` (true if every child is true; an empty list is invalid), `any` (true if any
child is true; an empty list is invalid) and `not` (one child). The maximum depth is 4, a
condition has at most 64 leaves, and an `in` list has at most 256 values.

Leaves name a top-level claim with `claim`, or a nested value with `path` (an array of member
names, e.g. `["job_config", "url"]`; dots in claim names are never interpreted). Each leaf has
exactly one operator:

| Operator | True when |
|---|---|
| `equals` | The normalized claim value equals the normalized literal |
| `in` | It equals one of the literals |
| `glob` | It matches the glob pattern (below) |
| `regex` | It fully matches the regular expression (below) |
| `present` | `true`: the claim exists and is not `null`; `false`: it is absent or `null` |

Optional modifier `ignore_case: true` (Unicode simple case folding) for `equals`, `in`, `glob` and
`regex`. Comparison is case-sensitive by default.

Value normalization, applied to claim values and literals alike:

- Strings are compared byte for byte, with no Unicode normalization.
- Booleans become `"true"` and `"false"`. Integers become their decimal form. So
  `{"equals": true}` matches GitLab's `"ref_protected": "true"` and `{"equals": 20}` matches
  `"project_id": "20"`.
- `null` counts as absent.
- An array claim is multi-valued. A leaf is true if any element matches. Elements that are
  objects or arrays never match.
- An object can be reached only through `path`. A leaf on an object value is false.

Missing claims: every leaf except `present: false` is false on a missing claim. As a
consequence, `not` around a leaf on a missing claim is true. A condition that needs a claim to
exist MUST say `present: true`.

Glob semantics (explicit; the separator is always `/`):

| Token | Matches |
|---|---|
| `*` | Zero or more characters within one segment (never `/`) |
| `?` | Exactly one character other than `/` |
| `**` | Only as a whole segment. In the middle (`a/**/b`): zero or more whole segments. At the end (`a/**`): one or more segments. At the start (`**/b`): zero or more leading segments. |
| `\x` | The literal character `x` (escapes `*`, `?`, `\`, `[`, `]`, `{`, `}`) |

The whole value must match. There are no character classes and no `{a,b}` alternation (use `in`
or `any`). `a**`, `**b` and unescaped `[`, `]`, `{`, `}` are rejected when the mapping is saved.

| Pattern | Value | Match |
|---|---|---|
| `acme/*` | `acme/app` | yes |
| `acme/*` | `acme/billing/app` | no |
| `acme/**` | `acme/billing/app` | yes |
| `acme/**` | `acme` | no |
| `acme/**/app` | `acme/app` | yes |
| `acme/app-*` | `acme/app-ui` | yes |
| `acme*/**` | `acme-evil/app` | yes (the prefix trap: why R-CI-005 requires ID pins) |
| `release/\*` | `release/*` | yes |

Regular expressions are opt-in: they apply only through the `regex` operator, and a glob or
`equals` literal is never interpreted as one. The dialect is Go RE2
([syntax](https://github.com/google/re2/wiki/Syntax)), which runs in linear time. trove
anchors the expression as `^(?:…)$`, limits it to 512 characters, and compiles it when the
mapping is saved; an invalid expression answers 422.

Save-time rules (422 with a `reason` on violation):

- `catch_all_condition`: the condition has no leaf.
- `pin_required`: the CI issuer is public and the top-level `all` (or the root leaf) has no
  `equals` or `in` leaf on a `pin_claims` claim (R-CI-005).
- `unprotected_write`: see R-CI-011.
- `unpinned_write`: on a CI issuer with `public_issuer: false`, a mapping without such a pin leaf
  whose grants include `deploy`, `overwrite` or `delete` is refused, unless every one of those
  actions comes from an inline grant with `ownership: bind_project`. Writes granted through
  `policies` or `groups` need a pin on every CI issuer. Path claims change on rename and transfer;
  ownership keyed by the immutable `project_id` (R-CI-015) keeps a project that inherits a
  released path from writing to packages another project published.

Warnings (returned in the save response's `warnings` array; the save succeeds):

- a `glob` or `regex` on `sub`;
- a condition on `user_login` or `user_email` (GitLab users can change these);
- a template that references a claim neither in `required_claims` nor guarded by
  `present: true` at the top level;
- `unpinned_condition`: a mapping of a CI issuer with `public_issuer: false` whose condition has
  no pin leaf (H-30);
- `repository_template_unconstrained`: a `repositories` template (R-CI-014) without a `require`
  filter. Every project inside the pin chooses the substituted value through its own path, so it
  can select any existing repository whose key fits the literal part.

**Acceptance criteria**

- A table-driven unit test covers every row of the glob table, normalization of
  `"true"`/`true`/`"20"`/`20`, array claims, `null` and missing claims under `not`.
- `{"claim": "ref", "regex": "(a+)+$"}` against a 10 000-character value evaluates in under
  1 ms (no backtracking).
- A condition with depth 5 or 65 leaves is rejected with 422.
- On a CI issuer with `public_issuer: false`, a mapping with the condition
  `{"claim": "project_path", "glob": "acme/**"}` that lists a policy granting `deploy` is refused
  with 422 `unpinned_write`. The same mapping whose only write is an inline grant with
  `ownership: bind_project` is saved with the warning `unpinned_condition`.

### R-CI-011 · Protected refs, environments and pipeline sources
Priority: **MVP** · Covers: CIOIDC-035, CIOIDC-037

These conditions are ordinary leaves (R-CI-010). trove enforces when they are mandatory:

- `unprotected_write` rule: a mapping whose grants include `deploy`, `overwrite` or `delete` MUST
  contain `{"claim": "ref_protected", "equals": true}` in its top-level `all`. Otherwise the save
  fails with 422, unless `options.allow_unprotected_refs: true`. That option is visible in the
  API, the UI and the audit event of the save. On a protected ref, only users allowed to push or
  merge can run pipelines, and protected variables and runners are available only there
  ([pipeline security on protected branches](https://docs.gitlab.com/ci/pipelines/#pipeline-security-on-protected-branches)).
  A branch-name condition alone can be met by anyone who can push a branch.
- Environment gating: a mapping MAY require `environment` (exact or glob),
  `environment_protected: true`, a `deployment_tier`, and `environment_action: start` to
  distinguish real deployments from jobs that only `prepare`, `verify` or `access` an
  environment
  ([protected environments](https://docs.gitlab.com/ci/environments/protected_environments/)).
  Protected environments need a paid GitLab tier. On GitLab Free, a condition
  `environment_protected: true` never matches (inferred from GitLab's tier statement, not tested).
- Pipeline sources: the `pipeline_source` claim takes the values of `CI_PIPELINE_SOURCE`
  ([list](https://docs.gitlab.com/ci/jobs/job_rules/#ci_pipeline_source-predefined-variable)).
  A write mapping without a `pipeline_source` condition draws a warning that recommends an
  allowlist such as `push`, `web`, `schedule`. `merge_request_event`,
  `external_pull_request_event` and `trigger` should not be allowlisted for writes.
- The dry-run endpoint (R-CI-019) shows, for a given claim set, which of these conditions failed.

**Acceptance criteria**

- Saving a mapping with `actions: ["deploy"]` and no `ref_protected` leaf returns 422
  `unprotected_write`. With `options.allow_unprotected_refs: true`, it returns 201 plus a
  warning.
- A job on an unprotected branch whose project matches the publish mapping gets no deploy grant.
  `npm publish` then fails with 403.

### R-CI-012 · Fork and merge request pipelines
Priority: **MVP** · Covers: CIOIDC-036, CIOIDC-034

GitLab facts
([merge request pipelines with forks](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/#use-with-forked-projects),
[cloud services](https://docs.gitlab.com/ci/cloud_services/)):

- A merge request pipeline from a fork normally runs in the fork, and all project claims name the
  fork. Mappings pinned to the parent's `project_id` or `namespace_id` do not match it.
- A parent-project member can run it in the parent project. The CI configuration then comes from
  the fork's branch, while the parent's runners and settings apply. `project_path` and
  `project_id` (and `source_project_*` since 19.5) name the fork, `job_project_*` (since 18.4)
  name the parent, and `ref_protected` is `"false"`.

Cross-project guard (runs before mapping evaluation): a subject token is *cross-project* when two
of `project_id`, `job_project_id` and `source_project_id` are both present and differ. Then:

- If the CI issuer's `cross_project_source` is `deny` (the default) and no matched mapping sets
  `options.cross_project_source: read_only`, the exchange fails
  [`cross_project_source_denied`].
- Otherwise the grants of the matched mappings are capped to `read`.

Fork code running in the parent pipeline controls its own CI configuration and could
exfiltrate anything the token can read. Hence the default `deny`, not `read_only` (Q-CI-9).

Older GitLab (before 18.4) has no `job_project_*` claims, so the guard cannot detect these
pipelines. The protections that remain:

- They carry the fork's `project_id`, so they match only mappings that do not pin the parent.
- `ref_protected` is `"false"`, so the `unprotected_write` rule (R-CI-011) keeps them from
  writing.

Same-project merge request pipelines (`pipeline_source = merge_request_event`) carry the source
branch in `ref`. `ref_protected` describes the source branch, so R-CI-011 governs them.

**Acceptance criteria**

- A synthetic token with `project_id = "900"` and `job_project_id = "42"` is rejected with
  `cross_project_source_denied` under the default settings. With a matched mapping that sets
  `read_only`, the issued `scope` contains only `read:` items.
- On a GitLab test instance, a fork merge request pipeline run in the parent project cannot
  publish to `npm-internal`.

### R-CI-013 · Grant specification
Priority: **MVP** · Covers: CIOIDC-010, CIOIDC-013, CIOIDC-014

A mapping grants permissions in three combinable ways. The token's grant is the union of all
three, before caps and `scope` narrowing:

```json
{
  "policies": ["ci-read-internal"],
  "groups": ["ci-readers"],
  "inline": [
    {
      "repositories": ["npm-internal"],
      "packages": {"include": ["@{{ namespace_path | slug }}/*"], "exclude": []},
      "actions": ["read", "deploy"],
      "ownership": "bind_project"
    },
    {
      "repositories": ["nuget-internal"],
      "packages": {
        "include": [
          "{{ project_path | replace \"/\" \".\" | replace \"-\" \"\" }}",
          "{{ project_path | replace \"/\" \".\" | replace \"-\" \"\" }}.*"
        ]
      },
      "actions": ["read", "deploy"],
      "ownership": "bind_project"
    }
  ]
}
```

- `policies`: names of permission policies ([authorization.md](authorization.md)). The mapping
  becomes a principal of each listed policy (INV-9 lists CI identity mappings as a principal
  kind). It receives the actions the policy grants to that principal. The binding is stored once,
  in the policy's principal list; this field reads and writes that same binding. Policy grants
  are evaluated when the token is used, so changing a policy affects tokens that were already
  issued.
- `groups`: trove groups whose grants the token also carries, also evaluated at use time.
- `inline`: grants defined in the mapping. Each has a set of `repositories` (keys or patterns as
  in INV-9), exactly one of `packages` or `paths` (include/exclude patterns), `actions` from
  `read`, `annotate`, `deploy`, `overwrite`, `delete`, and `ownership` (`none` by default, or
  `bind_project`, see R-CI-015). Patterns may be templated (R-CI-014). trove renders inline
  grants once at exchange time and stores them with the issuance (R-CI-020); a later mapping
  edit does not change them.
- `manage` is never grantable to a CI token (save fails with 422 `action_not_allowed`). CI tokens
  never carry the system administration role, and they cannot create tokens, call management
  endpoints or exchange again. They are accepted on the package-protocol endpoints, `/files/…`
  and the repository-content operations of `/api/v1` that need only repository actions.
- No user subject: a CI token's principal is the mapping, with the job identity as attributes. It
  is never a user. trove creates no transient user, and no "join automatically" group
  membership applies to CI tokens. This deliberately departs from the behavior described in
  CIOIDC-014, where user-subject CI tokens silently inherit such groups. Acting within a human's
  permissions is the explicit, narrowing option of R-CI-024.
- A mapping with an empty grant specification cannot be saved (422 `empty_grant`).

How the CI side uses this grant at request time (the evaluation itself is defined in
[authorization.md](authorization.md)): the effective permission is
`(policy grants ∪ group grants ∪ frozen inline grants) ∩ caps ∩ scope`, followed by the
ownership check of R-CI-015 on writes.

**Acceptance criteria**

- With the grant above, a job of `acme/billing/invoice-api` can `npm install` from `npm-all`
  through `ci-read-internal` and can `npm publish` `@acme-billing/invoice-client` to
  `npm-internal`. Publishing `@acme-shipping/x` gets 403.
- Removing the mapping from policy `ci-read-internal` makes the next `npm install` with an
  already issued token fail with 403, without a new exchange.
- Saving a mapping with `actions: ["manage"]` returns 422 `action_not_allowed`.

### R-CI-014 · Claim templating and escaping
Priority: **MVP** · Covers: CIOIDC-011, CIOIDC-013

Templates may appear in the `repositories`, `packages` and `paths` patterns of inline grants
(and, in P1, in `groups_from_claim`, R-CI-024). The template language is deliberately small, with
no conditionals, no loops and no user-defined functions:

```text
template   = *( literal / "{{" ws expr ws "}}" )
expr       = source *( ws "|" ws filter )
source     = claim-name / string        ; claim-name: [a-z_][a-z0-9_]* (top-level claims only)
filter     = "lower" / "slug"
           / "replace" ws string ws string
           / "trim_prefix" ws string / "trim_suffix" ws string
           / "segment" ws integer       ; 0-based "/" segment, negative counts from the end
           / "require" ws string        ; RE2 full match, else render error
string     = DQUOTE *( %x20-21 / %x23-5B / %x5D-10FFFF / "\" ( DQUOTE / "\" ) ) DQUOTE
```

In `groups_from_claim` (R-CI-024), the source name `value` stands for the current array element.

- `slug`: lowercase; every run of characters outside `[a-z0-9]` becomes `-`; leading and
  trailing `-` are removed; an empty result is a render error.
- A literal `{{` is written `{{ "{{" }}`.
- Claim values are normalized as in R-CI-010. An array, object, missing or `null` claim is a
  render error [`grant_render_failed`].

Escaping and validation by context (applied to each substituted value after its filters, before
the value is inserted into literal text):

| Context | Rule |
|---|---|
| `repositories` | The value MUST match `^[a-z0-9._-]+$`, otherwise it is a render error. No glob characters can be inserted. |
| `packages` | Glob metacharacters (`\ * ? [ ] { }`) are backslash-escaped. The value MUST NOT contain `/`, so a claim cannot add a segment to an npm scope. |
| `paths` | Glob metacharacters are escaped. The value MUST NOT be empty, contain `..` or empty segments, or start with `/`. |
| all | Control characters (U+0000–U+001F, U+007F) are a render error. The rendered pattern is at most 1024 characters (IAM-021). |

So a claim can never widen a pattern: a value of `*` renders as the literal `\*`. A template in
`repositories` MUST contain at least one literal character next to its substitution (for example
`{{ … }}-releases`). Without that rule, a claim value alone (such as a GitLab subgroup that a
group member names `npm-internal`) could select an arbitrary existing repository. The save fails
with 422 `template_unanchored`.

Examples (GitLab project `acme/billing/invoice-api`, namespace `acme/billing`):

| Template | Result |
|---|---|
| `@{{ namespace_path \| slug }}/*` | `@acme-billing/*` |
| `{{ project_path \| replace "/" "." \| replace "-" "" }}.*` | `acme.billing.invoiceapi.*` (NuGet IDs compare case-insensitively, [formats/nuget.md](formats/nuget.md)) |
| `{{ project_path \| segment -1 }}` in `paths` | `invoice-api` |
| `{{ project_path \| segment 0 \| require "acme" }}-releases` in `repositories` | `acme-releases` |

**Acceptance criteria**

- A unit test renders each example row above. A claim value `a*b` in a `packages` template
  produces the pattern `a\*b`, which matches only the literal name `a*b`.
- A `paths` template fed `../etc` fails with `grant_render_failed`.
- A template with an unknown filter is rejected on save with 422 `template_invalid`.

### R-CI-015 · Package ownership binding
Priority: **MVP** · Covers: CIOIDC-013

Templated name patterns can overlap. For example, `@{{ namespace_path | slug }}/*` renders
`@acme-billing/*` both for namespace `acme/billing` and for a top-level group `acme-billing`. A
project path can also be taken over by another project after a rename. An inline grant with
`ownership: bind_project` therefore binds each package name to the GitLab project that first
publishes it:

- Owner identity: `ci:<ci-issuer-id>:project:<project_id>`. It is keyed by the immutable
  `project_id`. `project_path` is kept as a display value and updated on every publish.
- On `deploy` or `overwrite` of package name *N* (normalized as the format defines: npm names
  case-sensitive, NuGet IDs case-insensitive) to repository *R* with such a grant:
  - *N* has no owner in *R*: the publish claims it. The owner record and the upload commit
    together, and a concurrent first publish of the same name loses on the store's unique
    constraint and gets 403.
  - The owner is this project: the publish is allowed.
  - Another owner, including a user or service account: 403 [`package_owned_by_other_owner`],
    plus audit event `ci.package_ownership.conflict`. This holds even though the grant pattern
    matches.
- A rename of the GitLab project keeps ownership, because `project_id` does not change. A new
  project that reuses the old path gets 403, which is the intended outcome.
- Ownership records live in the package-ownership store of [authorization.md](authorization.md).
  That page also owns the administrator operations to pre-assign, transfer and release an owner.
  This page defines only the CI owner identity and when a CI publish claims or checks ownership.
  Whether claiming on first publish is the right default is Q-CI-6.

**Acceptance criteria**

- Project A (`project_id` 101) publishes `@acme-billing/client@1.0.0`. Project B (`project_id`
  202), whose grant pattern also renders `@acme-billing/*`, runs `npm publish` for
  `@acme-billing/client@1.0.1` and gets 403 with reason `package_owned_by_other_owner`.
- After renaming project A in GitLab, its next tag pipeline publishes `1.0.2` successfully.
- Two concurrent first publishes of the same new name from two projects result in exactly one
  owner.

### R-CI-016 · CI token lifetime
Priority: **MVP** · Covers: CIOIDC-007, CIOIDC-031

The lifetime of a CI token is

```text
ttl = min( requested ttl          (if the request sent `ttl`; otherwise ci.token.default_ttl),
           mapping max_ttl        (smallest across matched mappings, if set),
           CI issuer max_ttl      (if set),
           ci.token.max_ttl,
           subject exp - now      (remaining ID-token lifetime) )
```

- GitLab sets the ID token's `exp` to the job timeout, or to 5 minutes when no timeout is
  specified. The last term therefore bounds the CI token by the remaining job lifetime.
- `ttl < ci.token.min_ttl` → 400 [`subject_token_expires_too_soon`].
- `ci.token.max_ttl` MUST NOT exceed `auth.tokens.cit.max_ttl`, the CI token ceiling of
  [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-026 (6 h). trove
  refuses to start with a larger value. One ceiling governs every CI token, so the two pages
  cannot drift apart.
- A job that runs longer than one CI token's lifetime exchanges its ID token again. The ID token
  stays valid until the job timeout, which is why single use is off by default (R-CI-018).
- `expires_in` in the response is the computed `ttl`. The token's stored expiry is
  `issued_at + ttl`.

**Configuration**

```yaml
ci:
  token:
    default_ttl: 15m   # default (proposed)
    max_ttl: 1h        # default (proposed); ceiling auth.tokens.cit.max_ttl (6h)
    min_ttl: 30s       # default (proposed)
# environment override, example: TROVE_CI_TOKEN_MAX_TTL=30m
```

**Acceptance criteria**

- An ID token with 10 minutes left and default settings yields `expires_in` ≤ 600.
- `ttl=60` yields `expires_in` = 60. `ttl=86400` yields at most `ci.token.max_ttl`.
- An ID token with 20 seconds left yields 400 `subject_token_expires_too_soon`.
- Using the CI token one second after `expires_in` returns 401 on `npm view`.
- `TROVE_CI_TOKEN_MAX_TTL=12h` with the default `auth.tokens.cit.max_ttl` makes startup fail
  with a message that names both keys.

### R-CI-017 · CI token identity, presentation and revocation
Priority: **MVP** · Covers: CIOIDC-010, CIOIDC-014, CIOIDC-018

- Format, hashing and storage follow INV-8 and [identity-and-authentication.md](identity-and-authentication.md)
  (prefix `trv_cit_`, SHA-256 at rest, CRC32 suffix). This page adds the issuance record
  (R-CI-020).
- Presentation per INV-8: `Authorization: Bearer <token>`, HTTP Basic with any username and the
  token as password, or `X-NuGet-ApiKey: <token>` on NuGet push.
- CI tokens do not appear in any user's token list. Administrators find them through the
  issuance API (R-CI-020).
- Revocation is immediate (INV-8) and happens:
  - when a mapping is disabled or deleted (its unexpired tokens);
  - when a CI issuer is disabled or deleted (all of its unexpired tokens);
  - through the bulk revoke operation (R-CI-020);
  - by an administrator revoking one token by ID (identity-and-authentication.md).

  Editing a mapping does not revoke its tokens. The inline grants of existing tokens stay frozen
  until they expire, and policy and group grants follow live changes.
- The `principal` row of a mapping (kind `ci_mapping`,
  [identity-and-authentication.md](identity-and-authentication.md) R-AUTH-016) mirrors `enabled`:
  disabling the mapping sets the principal's `status` to `disabled` in the same transaction, and
  enabling it sets it back. Token validation (R-AUTH-023) then rejects every CI token of a disabled
  mapping with `principal_disabled`, including one that escaped the revocation sweep. Together
  with the re-check of R-CI-007, no token of a disabled mapping is usable beyond the revocation
  latency bound.

**Acceptance criteria**

- The CI token works with `npm whoami --registry …` (Bearer), with a NuGet source configured
  through Basic credentials, and with `dotnet nuget push -k`.
- Disabling a mapping makes its unexpired tokens fail with 401 on the next request on every
  replica.

### R-CI-018 · Replay protection
Priority: **MVP** · Covers: CIOIDC-018, CIOIDC-019

- trove MUST record every exchanged subject token by (`ci_issuer`, `jti`) with the exchange
  count and the subject's `exp`. This happens in the exchange transaction as an atomic upsert,
  so all replicas see the same count (INV-12).
- With `single_use_subject_tokens: true`, a second exchange of the same `jti` fails
  [`subject_token_replayed`] and creates audit event `ci.exchange.replayed`. A subject token
  without `jti` is then rejected [`required_claim_missing`].
- With `false` (the default), repeated exchanges are allowed and counted. The issuance list shows
  the count. Once the count exceeds `ci.replay.warn_after_exchanges`, every further exchange
  emits a `ci.exchange.repeated` audit event.
- Rows are deleted by a cleanup job once `exp + clock_skew + ci.replay.retention_after_expiry`
  has passed. After that the token fails the time check anyway.
- Why the default is off: GitLab jobs often exchange once per tool step and retry on network
  errors, and a long job has to re-exchange (R-CI-016). The audience binding, the short
  ID-token lifetime and the masked job log already limit replay. Single use is the stricter
  option for CI issuers whose audience is not exclusive to trove (R-CI-026). See Q-CI-4.

**Data model**

```sql
CREATE TABLE ci_subject_token_uses (
  issuer_id           uuid NOT NULL REFERENCES ci_issuers(id) ON DELETE CASCADE,
  jti                 text NOT NULL CHECK (length(jti) <= 256),
  first_exchanged_at  timestamptz NOT NULL,
  last_exchanged_at   timestamptz NOT NULL,
  exchange_count      integer NOT NULL,
  subject_expires_at  timestamptz NOT NULL,
  PRIMARY KEY (issuer_id, jti)
);
CREATE INDEX ci_subject_token_uses_expiry ON ci_subject_token_uses (subject_expires_at);

-- exchange transaction (sketch)
INSERT INTO ci_subject_token_uses AS u
       (issuer_id, jti, first_exchanged_at, last_exchanged_at, exchange_count, subject_expires_at)
VALUES ($1, $2, now(), now(), 1, $3)
ON CONFLICT (issuer_id, jti)
DO UPDATE SET exchange_count = u.exchange_count + 1, last_exchanged_at = now()
RETURNING exchange_count;   -- > 1 with single use on: roll back and reject
```

**Configuration**

```yaml
ci:
  replay:
    retention_after_expiry: 15m   # default (proposed)
    warn_after_exchanges: 10      # default (proposed)
```

**Acceptance criteria**

- With single use on, exchanging the same ID token twice with `curl` returns 200 and then 400
  `subject_token_replayed`. Concurrent exchanges on two replicas yield exactly one 200.
- A failed exchange (for example the database rejects the token insert) does not increment the
  count, so a retry succeeds.

### R-CI-019 · Mapping management API and dry-run evaluation
Priority: **MVP** · Covers: CIOIDC-017, ADMAPI-029

System administrators manage mappings (delegates in P1, R-CI-023):

| Method and path | Purpose | Success | Errors |
|---|---|---|---|
| `GET /api/v1/ci/issuers/{issuer}/mappings` | List in evaluation order (tier, priority) | 200 | 404 issuer |
| `POST /api/v1/ci/issuers/{issuer}/mappings` | Create | 201 + record + `warnings` | 409 name or priority taken, 422 rule violation |
| `GET /api/v1/ci/issuers/{issuer}/mappings/{name}` | Read | 200 | 404 |
| `PUT /api/v1/ci/issuers/{issuer}/mappings/{name}` | Replace (`name` immutable) | 200 + `warnings` | 404, 409, 422 |
| `DELETE /api/v1/ci/issuers/{issuer}/mappings/{name}` | Delete; revokes its unexpired tokens | 204 | 404 |
| `POST /api/v1/ci/issuers/{issuer}/evaluate` | Dry run against a claim set | 200 | 404, 422 |

Mapping record sketch:

```json
{
  "name": "acme-release-publish",
  "description": "Protected tags of acme projects publish their own npm and NuGet packages",
  "priority": 100,
  "enabled": true,
  "subject_kinds": ["id_token"],
  "match": {"all": [
    {"claim": "namespace_id", "in": ["4711", "4712"]},
    {"claim": "ref_type", "equals": "tag"},
    {"claim": "ref_protected", "equals": true},
    {"claim": "pipeline_source", "in": ["push", "web"]}
  ]},
  "grants": {"policies": ["ci-read-internal"], "groups": [], "inline": ["… as in R-CI-013 …"]},
  "max_ttl_seconds": 1800,
  "options": {"cross_project_source": "deny", "allow_unprotected_refs": false},
  "version": 1
}
```

Dry run: the request carries a claim set, not a token, so nobody has to paste a live credential.
The response shows the decision trace. It performs no signature or time check unless `now` and
the time claims are given, issues nothing and records no `jti`:

```json
{
  "subject_kind": "id_token",
  "claims": {"iss": "https://gitlab.com", "project_id": "4242", "namespace_id": "4711",
             "project_path": "acme/billing/invoice-api", "namespace_path": "acme/billing",
             "ref": "v1.4.0", "ref_type": "tag", "ref_protected": "true",
             "pipeline_source": "push", "jti": "x", "exp": 1792940000, "iat": 1792936400},
  "scope": "deploy:npm-internal"
}
```

```json
{
  "cross_project": false,
  "trace": [
    {"tier": "system", "mapping": "acme-mr-readonly", "matched": false,
     "failed_leaf": "/all/1", "detail": "pipeline_source equals \"merge_request_event\": value \"push\""},
    {"tier": "system", "mapping": "acme-release-publish", "matched": true}
  ],
  "selected": ["acme-release-publish"],
  "grants": {"inline": [{"repositories": ["npm-internal"], "packages": {"include": ["@acme-billing/*"]},
             "actions": ["read", "deploy"], "ownership": "bind_project"}], "policies": ["ci-read-internal"]},
  "scope": "deploy:npm-internal",
  "ttl_seconds": 900,
  "warnings": []
}
```

Every create, update and delete emits an audit event with the before and after record
(R-CI-020).

**Acceptance criteria**

- The dry run for the claim set above names `acme-release-publish` and renders
  `@acme-billing/*`. The same claim set with `"ref_protected": "false"` shows the failing leaf.
- A mapping created through the API is used by the next exchange on every replica within 1 s
  (cache invalidation through `NOTIFY`).

### R-CI-020 · Issuance records, audit and metrics
Priority: **MVP** · Covers: CIOIDC-018, CIOIDC-023

Every successful exchange writes an issuance record in the exchange transaction. Together with
the audit events on package operations, which carry `token_id`, it answers which job got which
token and what that token did.

- Job metadata comes only from verified claims (`project_id`, `pipeline_id`, `job_id`, `sha`,
  `ref`, …). The request carries no free-form run metadata that a caller could set, which
  replaces the purpose of CIOIDC-023 with values that can be trusted.
- The issuance record stores an allowlisted claim snapshot: the claims of R-CI-006 plus `sub`,
  without `user_email` and `user_identities`.

**Data model**

```sql
CREATE TABLE ci_token_issuances (
  token_id           uuid PRIMARY KEY,          -- id of the token row (identity-and-authentication.md)
  issuer_id          uuid NOT NULL REFERENCES ci_issuers(id),
  mapping_ids        uuid[] NOT NULL,
  mapping_names      text[] NOT NULL,           -- snapshot, survives mapping deletion
  subject_kind       text NOT NULL CHECK (subject_kind IN ('id_token', 'job_token')),
  jti                text,
  project_id         text NOT NULL,
  project_path       text,
  namespace_id       text,
  pipeline_id        text,
  job_id             text NOT NULL,
  ref                text,
  ref_type           text,
  ref_protected      boolean,
  pipeline_source    text,
  environment        text,
  sha                text,
  user_id            text,
  user_login         text,
  claims             jsonb NOT NULL,            -- allowlisted snapshot
  grants             jsonb NOT NULL,            -- rendered inline grants, policy and group names, caps
  requested_scope    text,
  granted_scope      text NOT NULL,
  cross_project      boolean NOT NULL DEFAULT false,
  issued_at          timestamptz NOT NULL,
  expires_at         timestamptz NOT NULL,
  client_ip          inet,
  user_agent         text
);
CREATE INDEX ci_issuances_project ON ci_token_issuances (issuer_id, project_id, issued_at DESC);
CREATE INDEX ci_issuances_job     ON ci_token_issuances (issuer_id, job_id);
CREATE INDEX ci_issuances_expiry  ON ci_token_issuances (expires_at);
```

**API** (system administrator; read access for the auditor role once
[authorization.md](authorization.md) defines it)

| Method and path | Purpose |
|---|---|
| `GET /api/v1/ci/issuances?issuer=&project_id=&project_path=&pipeline_id=&job_id=&mapping=&active=&from=&to=` | List issuance records, newest first |
| `GET /api/v1/ci/issuances/{token_id}` | One record, plus a usage summary from the audit store: request count, last use, packages deployed |
| `POST /api/v1/ci/issuances/revoke` | Revoke all unexpired CI tokens that match a filter (`issuer` required; at least one of `project_id`, `pipeline_id`, `job_id`, `mapping`). Returns `{"revoked": <n>}`. |

**Audit events** (stored and streamed per INV-16, see
[operations-and-observability.md](operations-and-observability.md)):

| Event | When | Content beyond actor, time and source |
|---|---|---|
| `ci.exchange.succeeded` | Token issued | `token_id`, CI issuer, mappings, claim snapshot, granted scope, TTL |
| `ci.exchange.denied` | Any 400/429 | `reason`, and the CI issuer if one was resolved. Before the signature is verified, only the unverified `iss` and `kid` are logged and no other claims, so forged tokens cannot inject log content. After verification, the claim snapshot is logged. |
| `ci.exchange.replayed`, `ci.exchange.repeated` | R-CI-018 | `jti`, count |
| `ci.token.revoked` | Any revocation of a CI token | cause (mapping, issuer, bulk, single) |
| `ci.issuer.*`, `ci.mapping.*` | Create, update, delete | before and after record |
| `ci.keys.refreshed`, `ci.keys.refresh_failed` | R-CI-003 | `kid` list, error |
| `ci.package_ownership.claimed`, `ci.package_ownership.conflict` | R-CI-015 | repository, package, owner, `token_id` |
| `ci.job_token.lookup` | R-CI-022 | outcome, GitLab status, latency |

trove never logs the subject token, the job token or the issued token, at any log level. The
HTTP client used for GitLab calls redacts the `JOB-TOKEN` and `Authorization` headers.

**Metrics** (INV-16; labels have bounded cardinality and carry no project or user names):

- `trove_ci_exchanges_total{issuer,outcome,reason}`
- `trove_ci_exchange_duration_seconds{issuer}` (histogram)
- `trove_ci_signing_keys_refresh_total{issuer,outcome}`
- `trove_ci_signing_keys_age_seconds{issuer}`
- `trove_ci_tokens_active{issuer}` (refreshed by a periodic count)
- `trove_ci_job_token_lookups_total{issuer,outcome}`
- `trove_ci_job_token_lookup_duration_seconds{issuer}`

**Configuration**

```yaml
ci:
  issuances:
    retention: 8760h   # default (proposed): one year; Go durations have no day unit
```

**Acceptance criteria**

- After the reference pipeline (R-CI-028) runs,
  `GET /api/v1/ci/issuances?job_id=<CI_JOB_ID>` returns one record per exchange. The
  `npm-internal` audit entry of the publish carries the same `token_id`.
- `POST /api/v1/ci/issuances/revoke {"issuer": "gitlab-com", "pipeline_id": "<id>"}` makes all
  of that pipeline's tokens fail with 401.
- A forged token with a bad signature and a 10 kB `project_path` produces a
  `ci.exchange.denied` event without `project_path`.

### R-CI-021 · Exchange endpoint hardening
Priority: **MVP** · Covers: CIOIDC-018

The endpoint is reachable without credentials, so the work an anonymous caller can cause must be
bounded:

- Body limit `ci.exchange.max_body_bytes`; subject-token limit (R-CI-004).
- Rate limits: a token bucket per client IP and one per CI issuer. Exceeding either answers 429
  with `Retry-After`. The client IP comes from the trusted-proxy handling of
  [architecture.md](architecture.md). Buckets are in memory per replica, so the cluster-wide
  limit is the per-replica limit times the number of replicas. This is a deliberate trade-off
  against a PostgreSQL round trip per request.
- Cost bounds: key refetch throttling (R-CI-003); bounded condition size and RE2 (R-CI-010);
  at most `ci.mappings.max_per_issuer` mappings.
- Signature verification happens before any database write. An unauthenticated caller can make
  trove write only an audit row for a denial. Audit rows for identical denials from one client
  IP are rate limited and aggregated: one `ci.exchange.denied` with a count per minute.

**Configuration**

```yaml
ci:
  exchange:
    rate_limit:
      per_client_ip_per_minute: 60    # default (proposed)
      per_client_ip_burst: 20         # default (proposed)
      per_issuer_per_second: 100      # default (proposed)
```

**Acceptance criteria**

- 200 exchange requests from one IP within a minute: the first 20 to 80 (burst plus refill) get
  a 200 or 400 status, the rest get 429 with `Retry-After`.
- 10 000 requests with garbage subject tokens produce no outbound request to GitLab and fewer
  than 100 audit rows.

### R-CI-022 · `CI_JOB_TOKEN` fallback
Priority: **P1** · Covers: CIOIDC-038, CIOIDC-039, CIOIDC-040

An optional mode for jobs that present `CI_JOB_TOKEN` instead of an ID token. It is off by
default (INV-10). Q-CI-5 weighs whether to build it at all. The mode is enabled only when both
the global `ci.job_token_fallback.enabled` and the CI issuer's `job_token_fallback.enabled` are
`true`, and only for `gitlab` issuers.

```mermaid
sequenceDiagram
    participant J as GitLab job
    participant A as trove
    participant GL as GitLab API
    J->>A: POST /api/v1/auth/oidc/token (subject_token_type = gitlab-job-token, ci_issuer = gitlab-internal)
    A->>GL: GET /api/v4/job with header JOB-TOKEN
    GL-->>A: 200 job JSON (status running) or 401
    A->>A: synthesize claims, evaluate job_token mappings, cap to read
    A-->>J: 200 trv_cit_... (short TTL)
```

Behavior:

- Request: `subject_token_type=urn:trove:token-type:gitlab-job-token` (a trove-private
  identifier; RFC 8693 §3 allows other URIs), `subject_token=$CI_JOB_TOKEN`, and `ci_issuer`
  (required, since the token carries no issuer trove can read).
- trove calls `GET <issuer>/api/v4/job` with header `JOB-TOKEN: <token>`. It never uses the
  query-string form, which ends up in proxy logs. The URL comes only from the CI issuer record
  (no SSRF). TLS, CA bundle and outbound proxy are as in R-CI-003. Timeout
  `ci.job_token_fallback.request_timeout`; one retry only on connection errors. GitLab 401/403 →
  [`job_token_rejected`]; other errors or a timeout → 503 [`gitlab_unreachable`].
- The response must have `status = "running"`, otherwise [`job_not_running`]. trove
  synthesizes claims only from fields documented for this endpoint (verified 2026-10-28 against
  [Jobs API, retrieve a job by job token](https://docs.gitlab.com/api/jobs/#retrieve-a-job-by-job-token)):

  | Synthesized claim | Source field |
  |---|---|
  | `job_id` | `id` |
  | `pipeline_id` | `pipeline.id` |
  | `project_id` | `pipeline.project_id` |
  | `ref` | `ref` |
  | `ref_type` | `tag` (`true` → `tag`, else `branch`) |
  | `sha` | `pipeline.sha` |
  | `user_id`, `user_login` | `user.id`, `user.username` |
  | `iss` | the CI issuer's issuer URL (not from GitLab) |

  The response contains no project path, no namespace and no ref protection. `project_path`,
  `namespace_*`, `ref_protected`, `environment*`, `pipeline_source` and `jti` are therefore
  absent, and conditions on them behave as R-CI-010 defines for missing claims.
- Only mappings with `job_token` in `subject_kinds` are evaluated. ID-token mappings never match
  fallback claims by accident. The pinning rule applies to `project_id`.
- Fallback grants are always capped to `read`. Without `ref_protected`, trove cannot tell a
  protected release pipeline from any branch push, so no write is possible in this mode.
- TTL: `min(ci.job_token_fallback.max_ttl, other bounds of R-CI-016 except the subject exp)`.
- Replay key: `jti = "job:" + hex(SHA-256(token))`. The raw token is never stored. Concurrent
  exchanges of the same token on one replica share one GitLab call (single-flight). Lookups are
  not cached across exchanges, so every exchange confirms that the job is still running.

Risks, per case, and their mitigations:

| Risk | When it is real | Mitigation |
|---|---|---|
| trove receives a GitLab credential that also works on GitLab's packages, releases, deployments, environments, repository-file and job-artifact APIs with the triggering user's access level | Every fallback exchange, while the job runs | Off by default. Token used in memory for one request and never stored or logged. `JOB-TOKEN` redacted in the HTTP client. A leak in trove would expose GitLab data (H-28). |
| A service that the job sends its job token to (another tool, a misconfigured upstream) can exchange it at trove | Fallback enabled and some job sends its job token elsewhere. The token has no audience, so nothing stops this. | Grants capped to `read`. Short TTL. Mappings pinned by `project_id`. Single use available through the replay key. |
| Dependency on GitLab's availability: GitLab down means no exchange | Every fallback exchange | 503 `gitlab_unreachable`. The ID-token path stays offline-verifiable and unaffected. |
| Latency: one GitLab round trip per exchange | Every fallback exchange | 5-second timeout, single-flight, metrics `trove_ci_job_token_lookup_duration_seconds`. Exchanges happen once per job step, not per package request. |
| GitLab-side scoping (job token allowlist, fine-grained permissions) does not apply to trove | Always: the allowlist governs access to GitLab resources only | trove's own mappings are the only gate. Documented in the CI issuer's UI help text. |

**Configuration**

```yaml
ci:
  job_token_fallback:
    enabled: false        # default (proposed); the CI issuer must enable it too
    max_ttl: 10m          # default (proposed)
    request_timeout: 5s   # default (proposed)
```

**Acceptance criteria**

- With the mode disabled (default), `subject_token_type=urn:trove:token-type:gitlab-job-token`
  answers 400 `unsupported_subject_token_type` and trove makes no call to GitLab.
- With the mode enabled and a mapping `{"claim": "project_id", "equals": "4242"}` with
  `subject_kinds: ["job_token"]`, a running job of project 4242 gets a token whose `scope` holds
  only `read:` items. `npm install` works and `npm publish` gets 403.
- After the job has finished, an exchange with its job token answers `job_token_rejected` or
  `job_not_running`.
- A packet capture of the GitLab call shows the token only in the `JOB-TOKEN` header, and no
  trove log line contains it.

### R-CI-023 · Delegated mapping management
Priority: **P1** · Covers: CIOIDC-006, CIOIDC-015, ADMAPI-030

trove has no project entity before P2 (IAM-051), so delegation is tied to a CI issuer and to
explicit bounds:

- A system administrator creates a *delegation* on a CI issuer for a user or group. It lists:
  - the GitLab `project_id`s and/or `namespace_id`s the delegate may write mappings for;
  - the repositories (keys or patterns) the delegate may grant on;
  - the maximum actions (a subset of `read`, `annotate`, `deploy`, `overwrite`, `delete`);
  - a maximum TTL.
- The delegate manages mappings in the delegation's own tier through the same mapping API, with
  `?tier=delegation:<id>`. A delegated mapping:
  - MUST pin, at the top level, one of the delegation's IDs;
  - may contain inline grants only, within the delegation's repositories and actions;
  - MUST NOT reference policies or groups;
  - is subject to every save-time rule of R-CI-010 and R-CI-011.

  A violation answers 403 `outside_delegation`.
- Evaluation: first match within each tier, union across tiers (R-CI-009). A delegate can
  therefore only add grants within their bounds for their own GitLab projects. They can neither
  shadow nor reduce what system mappings give.
- Visibility: delegates list only the mappings of their own delegations. The catalog's switch for
  hiding global mappings (CIOIDC-016, IAM-040) is therefore unnecessary.
- Deleting a delegation deletes its mappings and revokes their tokens.

**Data model**

```sql
CREATE TABLE ci_delegations (
  id                     uuid PRIMARY KEY,
  issuer_id              uuid NOT NULL REFERENCES ci_issuers(id) ON DELETE CASCADE,
  principal_kind         text NOT NULL CHECK (principal_kind IN ('user', 'group')),
  principal_id           uuid NOT NULL,
  allowed_project_ids    text[] NOT NULL DEFAULT '{}',
  allowed_namespace_ids  text[] NOT NULL DEFAULT '{}',
  allowed_repositories   text[] NOT NULL,
  allowed_actions        text[] NOT NULL,
  max_ttl                interval,
  created_by             text NOT NULL,
  created_at             timestamptz NOT NULL DEFAULT now(),
  CHECK (cardinality(allowed_project_ids) + cardinality(allowed_namespace_ids) >= 1),
  CHECK (NOT ('manage' = ANY (allowed_actions)))
);
```

**API**: `GET|POST /api/v1/ci/issuers/{issuer}/delegations` and
`GET|PUT|DELETE /api/v1/ci/issuers/{issuer}/delegations/{id}` (system administrator). The mapping
endpoints of R-CI-019 accept the `tier` query parameter for delegates.

**Acceptance criteria**

- A delegate for `project_id` 4242 and repository `npm-team-a` can create a mapping that grants
  `deploy` on `npm-team-a` to project 4242. A mapping pinned to project 5000 or granting on
  `npm-internal` gets 403 `outside_delegation`.
- A system mapping that grants project 4242 read on `npm-all`, plus a delegated mapping that
  grants it deploy on `npm-team-a`, gives a job of project 4242 both grants.

### R-CI-024 · Grants derived from the job's subject
Priority: **P1** · Covers: CIOIDC-012, CIOIDC-010

Three additional grant kinds. They combine with R-CI-013 by union, except `triggering_user`,
which narrows:

- `service_accounts: ["<name>"]`: the token also carries the live grants of the named service
  accounts ([identity-and-authentication.md](identity-and-authentication.md)). This eases moving
  pipelines from stored service-account tokens to OIDC without rewriting policies.
- `groups_from_claim: {"claim": "groups_direct", "template": "gitlab-{{ value | slug }}", "allowed": ["gitlab-*"]}`:
  - each element of an array claim is rendered (R-CI-014, with `value` as the source);
  - names that exist as trove groups and match an `allowed` glob contribute their grants,
    while others are ignored and listed in the issuance record;
  - `allowed` is mandatory, so a claim value cannot name an arbitrary group such as an
    administrators group;
  - on a CI issuer with `public_issuer: true` the grant kind is refused with 422
    `claim_groups_on_public_issuer`. On a public instance anyone can create a group under any
    free path and join it, and `slug` maps `a/b` and `a-b` to the same name, so the claim proves
    nothing about membership in the organization.

  GitLab includes `groups_direct` only behind the feature flag `ci_jwt_groups_direct` (off by
  default, GitLab 17.3+) and omits it for users in more than 200 groups
  ([token payload](https://docs.gitlab.com/ci/secrets/id_token_authentication/#token-payload)).
- `triggering_user: {"login_provider": "<human OIDC provider name>", "if_unknown": "deny"}`:
  - trove looks up the user whose login identity is (that provider's issuer, `sub` = the
    token's `user_id`). GitLab's OIDC `sub` for a user is the user ID
    ([OpenID Connect provider](https://docs.gitlab.com/integration/openid_connect_provider/)),
    so this works when the same GitLab instance is also the human login provider (INV-7). The
    named login provider's issuer MUST equal the CI issuer's `issuer`; otherwise the save answers
    422 `login_provider_issuer_mismatch`, because the user IDs of two instances are unrelated
    numbers and would bind the job to a stranger's account.
  - The token's effective permission is then the mapping's grant intersected with that user's
    own permissions at use time, so a job can do no more than the person who triggered it.
  - `if_unknown: deny` fails the exchange [`no_matching_mapping`] when no such user exists;
    `mapping_only` skips the intersection.
  - The match uses the immutable `user_id`, never `user_login`.
  - trove deliberately offers no "act as the user" mode, which would give every job the
    triggering person's full rights.

**Acceptance criteria**

- With `triggering_user` set, a job triggered by a user without deploy on `npm-internal` cannot
  publish even though the mapping grants deploy. A user with deploy can.
- `groups_from_claim` with `allowed: ["gitlab-*"]` and a claim value `admins` adds no group.
- Saving a mapping with `groups_from_claim` for the `gitlab-com` issuer (public) answers 422
  `claim_groups_on_public_issuer`. Saving `triggering_user` with a login provider whose issuer is
  `https://login.example.com/realms/acme` on that issuer answers 422
  `login_provider_issuer_mismatch`.

### R-CI-025 · GitHub Actions as a CI issuer
Priority: **P1** · Covers: CIOIDC-003

Same mechanism, kind `github`:

- Issuer `https://token.actions.githubusercontent.com` (the default for this kind). Discovery
  publishes `jwks_uri` `https://token.actions.githubusercontent.com/.well-known/jwks` and RS256
  (fetched 2026-10-28). An enterprise with its own issuer URL configures that URL instead.
- Preset: `public_issuer: true`; `pin_claims`: `repository_id`, `repository_owner_id`;
  `required_claims`: `iss`, `sub`, `aud`, `exp`, `iat`, `jti`, `repository`, `repository_id`,
  `repository_owner`, `repository_owner_id`, `ref`, `ref_type`, `event_name`, `run_id`.
  `repository_owner_id` plays the role of an organization restriction. Unlike the organization
  name, it survives renames.
- The `unprotected_write` rule uses GitHub's `ref_protected` claim (listed in GitHub's
  discovery `claims_supported`). For reusable workflows, `job_workflow_ref` can pin the called
  workflow.
- Workflow side ([GitHub OIDC](https://docs.github.com/en/actions/concepts/security/openid-connect)):

  ```yaml
  permissions:
    id-token: write
    contents: read
  jobs:
    publish:
      runs-on: ubuntu-latest
      steps:
        - name: Exchange the GitHub ID token for a trove token
          run: |
            id_token="$(curl -sS -H "Authorization: bearer ${ACTIONS_ID_TOKEN_REQUEST_TOKEN}" \
              "${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=https://packages.example.com" | jq -r .value)"
            token="$(printf '%s' "$id_token" | curl -sS https://packages.example.com/api/v1/auth/oidc/token \
              --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
              --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
              --data-urlencode 'subject_token@-' | jq -er .access_token)"
            echo "::add-mask::${token}"
            echo "TROVE_TOKEN=${token}" >> "$GITHUB_ENV"
  ```

**Acceptance criteria**

- A GitHub Actions workflow in a test repository exchanges and runs `npm publish` against
  trove. The same workflow in a fork, or in another owner's repository, gets
  `no_matching_mapping`.
- A `github` mapping without a `repository_id` or `repository_owner_id` pin is rejected with
  `pin_required`.

### R-CI-026 · Generic OIDC issuers
Priority: **P1** · Covers: CIOIDC-002, CIOIDC-004

Kind `generic` accepts any issuer that publishes OIDC discovery or a configured `jwks_uri` and
signs with an allowed asymmetric algorithm. Examples are Azure DevOps service connections,
Kubernetes service-account token issuers, and other CI systems.

- The administrator sets `required_claims` (at least `iss`, `aud`, `exp`, `sub`) and
  `pin_claims`. With `public_issuer: true`, `pin_claims` must not be empty.
- Azure DevOps (facts as recorded in catalog CIOIDC-004, not re-verified here):
  - issuer `https://vstoken.dev.azure.com/<organization-guid>`;
  - `sub` `sc://<organization>/<project>/<service-connection>`;
  - audience `api://AzureADTokenExchange`.

  Because that audience is shared with every other relying party of the same issuer, trove
  cannot rely on it for exclusivity. Mappings should pin `sub` exactly, and
  `single_use_subject_tokens: true` is recommended.
- Microsoft Entra applications that sign with application-specific keys are supported by setting
  `jwks_uri` explicitly. No separate application-ID setting exists.

**Acceptance criteria**

- A `generic` issuer backed by a local OIDC test issuer (any standard implementation) exchanges
  successfully with a mapping on `sub`.
- A `generic` issuer that offers only HS256 cannot be saved (`algorithms_not_offered`).

### R-CI-027 · CI setup snippets
Priority: **P1** · Covers: CIOIDC-024, UI-049

`GET /api/v1/ci/issuers/{issuer}/snippets/{kind}?mapping=<name>&format=npm|nuget`
(`kind` = `gitlab-ci` or `github-actions`, the latter only for `github` issuers) returns a
ready-to-paste snippet as `text/plain`:

- the `id_tokens:` block (or the GitHub permission and exchange step) with the issuer's first
  allowed audience;
- the exchange step;
- the `.npmrc` or `nuget.config` lines for the repositories named in the mapping's grants.

The snippet contains no secret. Required permission: system administrator, or the delegate who
owns the mapping. The UI shows it on the CI identity screen
([api-and-ui.md](api-and-ui.md), UI-049) with a copy button.

**Acceptance criteria**

- The `gitlab-ci` snippet for the reference mapping, pasted unchanged into a project of the
  matching GitLab group, produces a passing publish job.

### R-CI-028 · Reference pipeline: npm and NuGet from GitLab CI
Priority: **MVP** · Covers: CIOIDC-030

The examples in [Examples](#examples) are part of the specification. trove's end-to-end test
suite runs them against a GitLab test instance and a trove deployment from the Helm chart
(INV-13), with the repositories `npm-internal` (local), `npm-all` (virtual), `nuget-internal`
(local), `nuget-all` (virtual), the policy `ci-read-internal` and the CI issuer and mapping shown
there.

**Acceptance criteria**

- On a protected tag `v1.4.0` of `acme/billing/invoice-api`, both jobs pass:
  `@acme-billing/invoice-client@1.4.0` is in `npm-internal`, and
  `Acme.Billing.InvoiceApi 1.4.0` is in `nuget-internal`.
- The same pipeline on an unprotected branch (with the `rules:` removed) fails at `npm publish`
  and `dotnet nuget push` with 403. The exchange itself succeeds through a read-only mapping,
  or fails with `no_matching_mapping` if none exists.
- `GET /api/v1/ci/issuances?job_id=$CI_JOB_ID` lists both jobs' tokens with the right
  `project_id`, `ref` and `sha`.
- No job log contains a `trv_cit_` value or the ID token.

## Examples

### trove side

CI issuer for GitLab.com (`POST /api/v1/ci/issuers`; values are examples):

```json
{
  "name": "gitlab-com",
  "kind": "gitlab",
  "issuer": "https://gitlab.com",
  "allowed_audiences": ["https://packages.example.com"],
  "description": "GitLab.com group acme"
}
```

Mapping (`POST /api/v1/ci/issuers/gitlab-com/mappings`). Namespace `acme/billing` has ID `4711`
in this example:

```json
{
  "name": "acme-release-publish",
  "description": "Protected tags in acme/billing publish their own npm and NuGet packages",
  "priority": 100,
  "match": {"all": [
    {"claim": "namespace_id", "equals": "4711"},
    {"claim": "ref_type", "equals": "tag"},
    {"claim": "ref_protected", "equals": true},
    {"claim": "pipeline_source", "in": ["push", "web"]}
  ]},
  "grants": {
    "policies": ["ci-read-internal"],
    "inline": [
      {"repositories": ["npm-internal"],
       "packages": {"include": ["@{{ namespace_path | slug }}/*"]},
       "actions": ["read", "deploy"], "ownership": "bind_project"},
      {"repositories": ["nuget-internal"],
       "packages": {"include": [
         "{{ project_path | replace \"/\" \".\" | replace \"-\" \"\" }}",
         "{{ project_path | replace \"/\" \".\" | replace \"-\" \"\" }}.*"]},
       "actions": ["read", "deploy"], "ownership": "bind_project"}
    ]
  },
  "max_ttl_seconds": 1800
}
```

A second mapping with a higher priority number, for example `acme-read` at priority 900, pins
the same namespace and grants only `ci-read-internal` to every other pipeline of the group.

### GitLab side

`.gitlab-ci.yml` of project `acme/billing/invoice-api`. Its npm package is named
`@acme-billing/invoice-client` in `package.json`, and its NuGet package ID is
`Acme.Billing.InvoiceApi`:

```yaml
stages: [publish]

variables:
  TROVE_URL: "https://packages.example.com"          # example

# Hidden job: requests the ID token and exchanges it in before_script.
.trove:
  id_tokens:
    TROVE_ID_TOKEN:
      aud: https://packages.example.com                # must be an allowed audience of the CI issuer
  before_script:
    - |
      if ! command -v jq >/dev/null || ! command -v curl >/dev/null; then
        apt-get update -qq && apt-get install -y -qq --no-install-recommends curl jq ca-certificates >/dev/null
      fi
    - |
      response="$(printf '%s' "$TROVE_ID_TOKEN" | curl -sS \
        --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
        --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:id_token' \
        --data-urlencode 'subject_token@-' \
        "$TROVE_URL/api/v1/auth/oidc/token")"
      TROVE_TOKEN="$(printf '%s' "$response" | jq -er '.access_token // empty')" \
        || { printf '%s\n' "$response" | jq '{error, error_description, reason}' >&2; exit 1; }
      export TROVE_TOKEN
      unset response TROVE_ID_TOKEN

publish-npm:
  stage: publish
  image: node:24-bookworm                               # example
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    # npm expands ${TROVE_TOKEN} at run time; the token is never written to disk.
    - |
      cat > .npmrc <<'EOF'
      registry=https://packages.example.com/api/npm/npm-all/
      //packages.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}
      //packages.example.com/api/npm/npm-internal/:_authToken=${TROVE_TOKEN}
      EOF
    - npm ci
    - npm version --no-git-tag-version "${CI_COMMIT_TAG#v}"
    - npm publish --registry https://packages.example.com/api/npm/npm-internal/

push-nuget:
  stage: publish
  image: mcr.microsoft.com/dotnet/sdk:10.0              # example
  extends: .trove
  rules:
    - if: $CI_COMMIT_TAG && $CI_COMMIT_REF_PROTECTED == "true"
  script:
    # nuget.config (below) reads %TROVE_TOKEN% for restores from nuget-all.
    - dotnet restore
    - >-
      dotnet pack src/Acme.Billing.InvoiceApi/Acme.Billing.InvoiceApi.csproj
      -c Release -o out --no-restore -p:PackageVersion="${CI_COMMIT_TAG#v}"
    - >-
      dotnet nuget push "out/*.nupkg"
      --source "$TROVE_URL/api/nuget/nuget-internal/v3/index.json"
      --api-key "$TROVE_TOKEN"
```

`nuget.config` in the repository root. Developers use the same file with their personal access
token in `TROVE_TOKEN`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="trove" value="https://packages.example.com/api/nuget/nuget-all/v3/index.json" />
  </packageSources>
  <packageSourceCredentials>
    <trove>
      <add key="Username" value="ci" />
      <add key="ClearTextPassword" value="%TROVE_TOKEN%" />
    </trove>
  </packageSourceCredentials>
</configuration>
```

Notes on the examples:

- `printf … | curl --data-urlencode 'subject_token@-'` keeps the ID token out of the process
  list. `printf` is a shell builtin.
- NuGet expands `%VAR%` in `nuget.config` values on every platform, and leaves the literal text
  when the variable is unset
  ([nuget.config reference](https://learn.microsoft.com/en-us/nuget/reference/nuget-config-file#using-environment-variables)).
  An unset `TROVE_TOKEN` therefore shows up as a 401, not as anonymous access.
- `--api-key "$TROVE_TOKEN"` puts the token in `dotnet`'s argument list. From NuGet 7.6 the
  `NUGET_API_KEY` environment variable can carry it instead (same reference). trove accepts
  both through `X-NuGet-ApiKey` (INV-8). Pushing with the `nuget.config` Basic credentials and no
  API key depends on the 401 challenge behavior of [formats/nuget.md](formats/nuget.md).
- `npm` resolves `${TROVE_TOKEN}` in `.npmrc` at run time
  ([npmrc](https://docs.npmjs.com/cli/v11/configuring-npm/npmrc)). The auth lines are scoped to
  trove's host, so npm never sends the token to another registry.

## Deferred

- CIOIDC-016 (P2): switch hiding global mappings from project administrators. Delegates never
  see system-tier mappings anyway (R-CI-023).
- IAM-040 (P2): the same switch, as a security setting. Not needed, for the same reason.
- CIOIDC-020 (P2): reference token in the exchange response. trove's CI tokens are already
  short opaque handles (INV-8).
- CIOIDC-021 (P2): explicit mapping selection in the request. Could come later as an optional
  `mapping` parameter whose condition must still match.
- CIOIDC-022 (Later): application-scoped exchange. There is no application entity before the
  distribution features.
- CIOIDC-025 (P2): exchange command of a companion CLI. No CLI is planned; the `curl` steps and
  snippets (R-CI-027) cover it.
- CIOIDC-026 (P2): GitHub App onboarding with generated pull requests.
- CIOIDC-027 (P2): binding a cloud IAM role to a user.
- CIOIDC-028 (P2): exchange of a signed cloud caller-identity request.
- CIOIDC-029 (P2): passwordless image pulls for Kubernetes workloads. This depends on the
  container formats (P1) and on CIOIDC-027 and CIOIDC-028.

## Security notes

- The exchange endpoint is anonymous. Identity mappings are the entire access control. The
  save-time rules exist so that the common mistakes cannot be saved:
  - catch-all conditions (`catch_all_condition`);
  - path-only conditions on public issuers (`pin_required`);
  - unpinned writes without ownership binding on any issuer (`unpinned_write`);
  - writes from unprotected refs (`unprotected_write`);
  - `manage` in CI grants (`action_not_allowed`).
- GitLab.com and GitHub Actions are public issuers. A validly signed token with the right
  audience proves only that some job on that platform requested it. Only conditions on
  immutable IDs bind it to an organization.
- Subject tokens and CI tokens are bearer credentials. Both are kept out of logs, error bodies
  and process lists (examples). Responses carry `Cache-Control: no-store`.
- Multi-audience ID tokens are delivered to every listed service, and any of them could replay
  the token at trove. They are rejected by default (R-CI-004).
- Algorithm and key handling follow RFC 8725:
  - an allowlist of asymmetric algorithms and no `none` or HMAC;
  - the key type must match the algorithm;
  - no keys taken from token headers;
  - `kid` used only as a lookup key.
- Outbound calls go to administrator-configured URLs only: discovery, keys, and the GitLab Jobs
  API in fallback mode. Tokens cannot make trove fetch other URLs.
- Package ownership (R-CI-015) keeps overlapping name patterns and reused project paths from
  turning into package takeover.
- Rate limits are per replica (R-CI-021). An attacker can get roughly `replicas × limit`
  exchange attempts. That is acceptable because each attempt costs one signature check.

## Open questions

### Q-CI-1 · First match per tier or union of all matching mappings

Options:

- (a) First match within a tier, union across tiers (specified above).
- (b) Union of every matching mapping.
- (c) Strict first match over all mappings, as in the catalog (CIOIDC-009).

Recommendation: (a). Within a tier, first match gives one predictable, auditable answer and allows
narrowing rules at low priority numbers. Across tiers, union keeps delegates (P1) from shadowing
system grants and the other way round. (b) is simpler to explain but cannot express "merge request
pipelines get less than release pipelines" without negations in every rule. (c) makes delegated
mappings fight with system mappings over priority numbers.

### Q-CI-2 · Grammar of the `scope` request parameter

Options:

- (a) The interim `<action>[:<repoKey>]` grammar of R-CI-007.
- (b) The scope grammar that [identity-and-authentication.md](identity-and-authentication.md)
  defines for personal and service-account tokens.

Recommendation: (b) once R-AUTH fixes it, with (a) as its subset. One scope language for all
tokens keeps clients and the authorization code single. The CI page should not invent a second
one.

### Q-CI-3 · Default for ID tokens with several audiences

Options:

- (a) Reject (specified default).
- (b) Accept when one element is trove's audience (RFC 7519 allows it).

Recommendation: (a). A token addressed to trove and to, say, a secrets manager reaches that
secrets manager too, which could then exchange it at trove. GitLab lets a job request one
token per audience at no cost, so rejecting costs the user nothing.

### Q-CI-4 · Single exchange per `jti` by default

Options:

- (a) Off, tracked and counted (specified).
- (b) On for every CI issuer.

Recommendation: (a) for `gitlab` and `github` issuers. Their tokens are audience-bound to trove
and short-lived, and jobs legitimately exchange several times (per tool step, after a network
error, after the first CI token expired). Recommend (b) in the documentation for `generic` issuers
with a shared audience (R-CI-026).

### Q-CI-5 · Build the `CI_JOB_TOKEN` fallback at all

Options:

- (a) Build it as specified (P1, read-only, off by default).
- (b) Move it to P2 and build it only when a concrete need appears.
- (c) Drop it.

Recommendation: (b). Because of INV-8, the fallback still needs an exchange step in the job; it
saves only the `id_tokens:` block. In return it puts a GitLab credential in trove's hands, adds
a runtime dependency on GitLab, and can never grant writes. Every supported GitLab version issues
ID tokens. If the owner sees a population of pipelines that cannot add `id_tokens:`, (a) is
specified and ready.

### Q-CI-6 · How a CI project acquires package ownership

Options:

- (a) Claim on first publish within the grant pattern (specified).
- (b) Only administrator pre-registration.
- (c) No ownership; rely on non-overlapping patterns.

Recommendation: (a). It needs no extra administration for the common case, and (b) remains
available through the ownership store of [authorization.md](authorization.md) for names that must
be reserved. (c) silently allows one project to publish versions of another project's package
whenever the slugs collide.

### Q-CI-7 · Revoke CI tokens when the GitLab job ends

Options:

- (a) Rely on the TTL bound by the ID token's `exp` (specified).
- (b) Subscribe to GitLab job webhooks and revoke on job completion.

Recommendation: (a) for the MVP; reconsider (b) in P2. (b) needs a webhook secret per GitLab
project or group and an inbound endpoint. With the default TTL of 15 minutes, the remaining
window is short. The gap is recorded as H-26.

### Q-CI-8 · Status code for rejected subject tokens

Options:

- (a) 400 `invalid_request` with a `reason` (specified, as RFC 8693 §2.2.2 requires).
- (b) 401, as in the catalog (CIOIDC-018).

Recommendation: (a). Standard OAuth clients and libraries expect RFC 8693 behavior. The `reason`
field gives scripts the precision that a status code would not. No client is known to depend on
401.

### Q-CI-9 · Default for cross-project (fork) pipelines

Options:

- (a) `deny` (specified).
- (b) `read_only`.

Recommendation: (a). In such pipelines the fork author controls the CI configuration, so even
read access lets them exfiltrate private packages. GitLab requires a parent-project member to
start the pipeline, but that member may not read the fork's changes to `.gitlab-ci.yml`.
Mappings can opt into `read_only` where the group accepts this.

## What this does not cover

### H-24 · Trust in the GitLab instance and its signing key

trove trusts every token signed with the issuer's published keys. A GitLab administrator, a
compromise of GitLab's signing key, or a takeover of the issuer's domain or TLS can mint ID tokens
for any project, ref and protection state. trove cannot detect this. Mappings can narrow
whose tokens count, but not whether the issuer itself is honest. Exposure is bounded by the
CI token TTL, package ownership (R-CI-015), and the audit trail that links each publish to the
claimed job.

### H-25 · Bearer tokens without sender constraint

Both the ID token and the issued CI token are bearer credentials. Anyone who reads them while
they are valid can use them: from a runner shared between jobs, a privileged container, or a
debug log printed with `set -x`. Single use (R-CI-018) limits the ID token to one exchange. A
stolen CI token works until it expires or is revoked. Sender-constrained tokens (DPoP,
[RFC 9449](https://www.rfc-editor.org/rfc/rfc9449), or mutual TLS) are not specified.

### H-26 · CI tokens outlive the job

A CI token stays valid until its `expires_in`, even after the job has finished (Q-CI-7).
Default worst case: 15 minutes, or up to `ci.token.max_ttl`.

### H-27 · Group hierarchies cannot be pinned by immutable ID

GitLab ID tokens carry the immediate `namespace_id` but no ID of the root group or of ancestor
groups. A mapping meant for "every project below group `acme`" on a public issuer must either
list every subgroup's `namespace_id` or project ID, or fall back on `namespace_path` globs. Path
globs can be satisfied by a group created later under a released path. The pinning rule forces at
least one ID, so the exposure is limited to subgroups added later and not yet listed. Keeping
those ID lists current is a manual task.

### H-28 · The fallback hands trove a GitLab credential

With R-CI-022 enabled, every fallback exchange gives trove a live `CI_JOB_TOKEN`. That token
can call GitLab's API with the triggering user's access level, within the job-token resource
list, until the job ends. A memory disclosure or a logging defect in trove would turn into
GitLab access. Mitigations (in-memory only, header redaction, off by default) reduce but do not
remove this. The mode is dormant unless an administrator enables it globally and on the CI
issuer.

### H-29 · The job-token fallback cannot recognize fork pipelines

Component: the `CI_JOB_TOKEN` fallback (R-CI-022). Subject: a merge request pipeline from a fork
that a member of the parent project runs in the parent project. Dormant: it becomes live once
R-CI-022 is enabled globally and on a CI issuer and a `job_token` mapping matches the parent's
`project_id`. The job API response carries no `job_project_*` or `source_project_*` fields, so the
cross-project guard of R-CI-012 cannot run, and the synthesized `project_id` names the parent. The
fork author's CI configuration then receives the parent's read grants and can exfiltrate every
package they cover. The fallback's cap to `read` does not help, because reading is the exposure.
What makes it real: a parent project that runs fork pipelines in its own context, plus a fallback
mapping on that project.

### H-30 · Path-only read mappings on private issuers follow released paths

Component: identity mappings of CI issuers with `public_issuer: false` (R-CI-005, R-CI-010).
Subject: a GitLab project created later under a path that a mapping's `project_path` or
`namespace_path` condition matches, after a rename, transfer or deletion released it, or under a
new group whose name fits a glob prefix. Live with the MVP for read grants: `unpinned_write`
(R-CI-010) keeps such mappings from writing without ownership binding, but read-only mappings may
still be path-only, and they hand the newcomer every package the grant can read. What makes it
real: an internal GitLab where ordinary users may create groups or projects, combined with an
unpinned read mapping. The `unpinned_condition` warning is the only signal.
