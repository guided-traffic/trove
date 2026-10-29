# Identity and authentication

Status: specification — not implemented

This page specifies how trove establishes who is calling. It covers the browser login of humans
through OpenID Connect, the server-side UI session, the break-glass administrator, and the
principals trove knows: users, groups, service accounts, the anonymous user and the built-in
principal `authenticated`. It also covers
the credentials trove issues itself (personal, service-account and CI tokens per INV-8) and
how every package client and API caller presents them.

The neighbouring pages own the rest:

- What an authenticated principal may do is in [authorization.md](authorization.md).
- How a GitLab job exchanges its ID token for a `trv_cit_` token is in
  [gitlab-ci-oidc.md](gitlab-ci-oidc.md).
- Where audit events are stored and queried is in
  [operations-and-observability.md](operations-and-observability.md).
- The npm and NuGet endpoints that carry the credentials are in [formats/npm.md](formats/npm.md)
  and [formats/nuget.md](formats/nuget.md).
- Management API conventions (errors, paging, OpenAPI) and the UI screens are in
  [api-and-ui.md](api-and-ui.md).

Covers: this page draws on these catalog pages:

- [identity-and-access.md](../features/identity-and-access.md): users, groups, the anonymous
  user and the security settings.
- [authentication-providers.md](../features/authentication-providers.md): the login model, OpenID
  Connect single sign-on, SCIM, anonymous access, the session, the profile and command-line login.
- [access-tokens.md](../features/access-tokens.md): the token model, scopes, lifetime, operations
  and presentation.
- The matching operations of [api-admin-operations.md](../features/api-admin-operations.md).
- The client-credential entries of [formats/npm.md](../features/formats/npm.md) and
  [formats/nuget.md](../features/formats/nuget.md).
- The identity part of the audit entries in
  [operations-and-monitoring.md](../features/operations-and-monitoring.md).

## Overview

| Credential | Held by | Obtained through | Presented as | Lifetime (proposed) |
|---|---|---|---|---|
| UI session | user, break-glass administrator | OIDC login (R-AUTH-006), local form (R-AUTH-014) | `__Host-trv_session` cookie | idle 1 h, absolute 12 h |
| `trv_pat_…` | user | UI or API (R-AUTH-027), npm web login (R-AUTH-034), device login (R-AUTH-036) | Bearer, Basic password, NuGet API key | default 90 days, maximum 365 days |
| `trv_sat_…` | service account | administrator API or `trove admin` (R-AUTH-015, R-AUTH-027) | same | default 90 days, maximum 365 days |
| `trv_cit_…` | CI identity mapping | CI exchange ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)) | same | at most 6 h |

```text
 browser ──/auth/oidc/{p}/start──► IdP ──code──► /auth/oidc/{p}/callback ──► ui_session ──► __Host-trv_session cookie
                                                     │ JIT user, group sync
 browser (session) ──POST /api/v1/tokens──────────────────────────────────► api_token (PAT, hash only)
 npm login ──/-/v1/login──► approval page (session) ──► poll doneUrl ──────► api_token (PAT)
 GitLab job ──POST /api/v1/auth/oidc/token──────────────────────────────────► api_token (CIT)   [gitlab-ci-oidc.md]
 any client ──Bearer | Basic(any:token) | X-NuGet-ApiKey──► R-AUTH-001 pipeline ──► principal + scope ──► authorization.md
```

Conventions on this page:

- Durations in YAML use Go duration syntax (`720h`).
- API error bodies follow [api-and-ui.md](api-and-ui.md). The error `code` values named here are
  part of the contract.
- Paths are relative to the external base URL (R-AUTH-003). `https://trove.test` is the
  example host in acceptance criteria.

**Replicas.** The Kubernetes Service hands every request to a random `web` replica (INV-12), and a
node drain removes replicas at any time. Every flow on this page therefore works when its
consecutive requests reach different replicas. Its state lives in PostgreSQL, and Valkey never
decides who a caller is: it carries change messages and the cluster-wide counters and throttle
exemption markers of R-AUTH-038. Its loss costs nothing but per-replica limits and a narrower
throttle exemption (INV-3, INV-18). R-AUTH-042 maps every piece of state, and R-AUTH-043 assigns
the background work to the `worker` role.

## Request authentication

### R-AUTH-001 · Credential sources and precedence
Priority: **MVP** · Covers: AUTHN-001, TOK-001, TOK-033, TOK-034

Every HTTP request on the client listener passes one authentication stage before routing to a
handler. The stage yields exactly one of three results:

- an authenticated principal (kind, ID, name) together with the credential that proved it (type,
  token ID or session ID, scope);
- the anonymous principal (R-AUTH-037);
- a rejection.

Handlers and [authorization.md](authorization.md) receive only this result. They never parse
credentials themselves.

The stage MUST select the credential in this order and MUST evaluate only the first one present:

1. **NuGet API key.** This applies only on the NuGet push, delete and relist endpoints that
   [formats/nuget.md](formats/nuget.md) marks as API-key endpoints. Selection follows R-AUTH-033.
2. **`Authorization` header.** Either `Bearer <token>` ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750)
   §2.1) or `Basic base64(<any username>:<token>)`
   ([RFC 7617](https://www.rfc-editor.org/rfc/rfc7617)). The Basic username is recorded for
   diagnostics and is otherwise ignored (INV-8).
3. **Session cookie** `__Host-trv_session` (Q-AUTH-2). It is accepted on `/api/v1/…` for every
   method, with the CSRF rules of R-AUTH-012. On `/files/…` it is accepted for `GET` and `HEAD`, and for `PUT` and
   `DELETE` under the same CSRF rules, because the UI's deploy dialog and item actions use them
   ([api-and-ui.md](api-and-ui.md) R-API-006, R-UI-012). On `/api/<format>/…` it is accepted for
   `GET` and `HEAD` only. Everywhere else it is ignored.
4. **No credential.** The request becomes anonymous if R-AUTH-037 allows it; otherwise it is
   unauthenticated.

Normative rules:

- If the selected credential is invalid (malformed, unknown, expired, revoked, or belonging to a
  disabled principal), the request MUST be rejected with 401 (R-AUTH-002). It MUST NOT fall
  through to a lower-ranked source or to anonymous.
- `Bearer` and Basic credentials MUST be trove tokens (R-AUTH-022). Any other value MUST be
  rejected without a database lookup. This covers identity-provider access tokens and ID tokens,
  JWTs in general, and passwords, including the break-glass password. There is one extension
  point: when [gitlab-ci-oidc.md](gitlab-ci-oidc.md)'s optional job-token fallback is enabled,
  the stage hands a Basic credential whose username is `gitlab-ci-token` to that verifier.
- **Endpoint rules.** After selecting the credential, the stage applies the rules of the endpoint
  the request arrived on ([endpoints.md](endpoints.md) R-EP-010): the session cookie is evaluated
  only on an endpoint whose surface holds `ui` (the default endpoint) and is ignored elsewhere; a
  token type the endpoint does not accept is rejected with 401 `invalid_token` without a database
  lookup; a token bound to other endpoints, or an unbound token where the endpoint accepts only
  bound ones, is rejected with 401 `invalid_token`; and an endpoint with `auth.anonymous: false`
  makes no request anonymous. With the default endpoint's defaults, every rule of this requirement
  stays as written.
- Tokens in query parameters (`?access_token=`) MUST NOT be accepted
  ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) §2.3 discourages them; they leak into logs).
- **Exchange endpoints.** These routes take a GitLab ID token as request input:
  - the CI exchange `POST /api/v1/auth/oidc/token` (INV-10);
  - from P1, the npm trusted-publishing exchange, both at the host root,
    `POST /-/npm/v1/oidc/token/exchange/package/{name}`, and below every npm registry URL,
    `POST /api/npm/{repoKey}/-/npm/v1/oidc/token/exchange/package/{name}`
    ([formats/npm.md](formats/npm.md) R-NPM-022, Q-NPM-1).

  That ID token is not a credential for this stage, and the endpoints themselves are
  unauthenticated. The npm routes receive it as `Authorization: Bearer <ID token>`, the header npm,
  pnpm and Yarn send there (R-NPM-022). On `POST` to
  these exact routes the stage MUST NOT evaluate the `Authorization` header: the request reaches
  the handler as unauthenticated, and the rule above that rejects every non-token `Bearer` value
  with 401 does not apply. The handler validates the ID token exactly as the CI exchange does
  ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-004). The header is still redacted in logs (below).
- A storage outage during validation MUST answer 503, not 401, so clients do not discard or
  re-prompt for working credentials.
- Valkey being unavailable or emptied MUST NOT turn a rejection into an acceptance, and MUST NOT
  delay a revocation. It removes the fast path of change messages, turns the limits of R-AUTH-038
  into per-replica shares, and narrows the throttle exemption to the replica's own cache, nothing
  else (R-AUTH-023, R-AUTH-038, R-AUTH-042).
- The request log and every other log MUST redact the following before writing: the
  `Authorization` header, the `X-NuGet-ApiKey` header, the `__Host-trv_session`,
  `__Host-trv_csrf` and `__Host-trv_oidc_binding` cookies, every substring matching the token
  pattern of R-AUTH-022 in paths and query strings, and the path segment after `/auth/cli/`. npm logout carries the token in the path
  (R-AUTH-035), and the npm web-login poll URL carries the poll secret there (R-AUTH-034).

Acceptance criteria:

- Run `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PAT" https://trove.test/api/v1/auth/whoami`.
  It prints `200`. With a `__Host-trv_session` cookie of another user added to the same request,
  `whoami` still names the token owner.
- Use a valid ID token issued by the configured IdP for trove's client as `Bearer`. The
  request gets 401 with `error="invalid_token"`, and no token-table query is executed (asserted
  in an integration test through a query counter).
- Send an expired PAT on a repository that anonymous may read. The request gets 401, not the
  anonymous response.
- (P1, with R-NPM-022.) A GitLab ID token whose mapping grants `deploy` on `@acme/ui`, sent as
  `Bearer` to `POST /-/npm/v1/oidc/token/exchange/package/@acme%2Fui` and to
  `POST /api/npm/npm-all/-/npm/v1/oidc/token/exchange/package/@acme%2Fui`, gets 201 with a
  `trv_cit_` token on both routes. The same ID token as `Bearer` on `GET /api/npm/npm-all/@acme%2Fui`
  gets 401 `invalid_token` without a token-table query.
- Grep the request log after the whole acceptance suite has run. It contains no `trv_pat_`,
  `trv_sat_` or `trv_cit_` substring followed by more than 4 characters.

### R-AUTH-002 · Authentication failures and challenges
Priority: **MVP** · Covers: TOK-033, TOK-034, PKG-NPM-007, PKG-NUGET-016

- An unauthenticated request for a protected resource MUST get 401, as MUST a request with a
  rejected credential. 403 and 404 are decisions of [authorization.md](authorization.md) for
  authenticated principals only. Package clients send configured credentials only after a 401.
- The 401 challenge MUST follow the endpoint family:

| Endpoint family | `WWW-Authenticate` |
|---|---|
| `/api/v1/…` | `Bearer realm="trove"`, plus `error="invalid_token"` when a token was presented and rejected ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) §3) |
| `/api/<format>/…`, `/files/…` | `Basic realm="trove"`. Format pages may add challenges, for example the OCI token-service challenge. The npm endpoints MUST NOT include the token `otp` (PKG-NPM-012 is not implemented). |

- The error body carries one `code`:

| Code | Meaning |
|---|---|
| `credentials_required` | No credential, and anonymous access is off or insufficient |
| `invalid_token` | Malformed, unknown, expired or revoked token; the response does not say which |
| `session_expired` | The session cookie names an ended session |
| `principal_disabled` | The token is valid but its owner is disabled |
| `reauthentication_required` | See R-AUTH-011 |

  Expired and revoked tokens are distinguished only in the audit event (R-AUTH-041), not in
  the response.

Acceptance criteria:

- `curl -si https://trove.test/api/nuget/nuget-all/v3/index.json` with anonymous access off
  returns 401 with `WWW-Authenticate: Basic realm="trove"`. `dotnet restore` against that
  source with credentials in `nuget.config` then succeeds, which proves the client answers the
  challenge.
- No response on an npm endpoint contains `otp` in `WWW-Authenticate` (contract test over all
  npm routes).

## OpenID Connect login

### R-AUTH-003 · External base URL and redirect URIs
Priority: **MVP** · Covers: AUTHN-002, OPS-051

- OIDC login MUST NOT start unless the external base URL (`server.base_url`, owned by
  [architecture.md](architecture.md)) is configured. If a provider is configured without a base
  URL, startup MUST fail with a configuration error.
- The base URL MUST be `https`. The one exception is a loopback host, used in tests.
- Every URL that trove registers at or sends to an IdP MUST be derived from the base URL and
  never from the request's `Host` or `X-Forwarded-*` headers:

| URL | Value |
|---|---|
| Redirect URI | `<baseURL>/auth/oidc/<providerId>/callback` |
| Post-logout redirect URI | `<baseURL>/ui/logged-out` |

- Each provider has its own redirect URI. This is the mix-up defense of
  [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.4.2.2.

Configuration:

```yaml
server:
  base_url: https://trove.example.com  # example
```

Acceptance criteria:

- With provider `corp`, `curl -si https://trove.test/auth/oidc/corp/start -H 'Host: evil.test'`
  answers 302. The `redirect_uri` in the Location header is
  `https://trove.test/auth/oidc/corp/callback`.

### R-AUTH-004 · Provider configuration and discovery
Priority: **MVP** · Covers: AUTHN-007, AUTHN-008

- Login providers are configured only in the YAML file (INV-15), never in PostgreSQL (decided
  in Q-AUTH-1). This covers the whole provider entry: connection, client secret reference,
  claim mapping and gates (R-AUTH-007) and group rules (R-AUTH-008). No API or
  configuration-as-code document creates or changes a provider; a change takes effect with a
  rollout of the replicas.
  Any number of providers is allowed. Each needs:
  - `id`: matches `^[a-z0-9][a-z0-9-]{0,31}$` and is part of the redirect URI;
  - `display_name`;
  - `issuer`: `https`, or loopback in tests;
  - `client_id`;
  - the client authentication.
- The client secret MUST be read from a file (`client_secret_file`) or an environment variable,
  never written inline (INV-15). trove MUST re-read the file when its modification time
  changes, so that rotating the Kubernetes Secret needs no restart.
- Client authentication at the token endpoint supports `client_secret_basic` (the default),
  `client_secret_post`, and `none`. `none` is for a public client that relies on PKCE alone.
  `private_key_jwt` is deferred.
- Discovery follows [OpenID Connect Discovery 1.0](https://openid.net/specs/openid-connect-discovery-1_0.html)
  §4:
  - Each replica that serves the client listener (`web`, and `all` in the local profile) fetches
    `<issuer>/.well-known/openid-configuration`. A `worker` replica serves no login and loads no
    provider (R-AUTH-042). The `issuer` in the document MUST be identical to the configured issuer
    (§4.3), or the provider is unusable.
  - The document is refreshed every `discovery_refresh`.
  - JWKS are fetched from `jwks_uri` and cached. An ID token with an unknown `kid` triggers at
    most one JWKS refetch per provider and replica per 60 s. Each replica keeps its own copy, so
    right after a provider starts signing with an unpublished key, one replica may reject a login
    that another accepts, for at most that throttle ([architecture.md](architecture.md)
    R-ARCH-032); the user retries the login.
  - Every call to a provider (discovery, JWKS, token endpoint, userinfo) goes through the outbound
    client of [architecture.md](architecture.md) R-ARCH-022 with a 10 s timeout. A JWKS refetch
    for an unknown `kid` runs inside the callback that needs it (R-AUTH-006 step 7) and is bounded
    by the same timeout.
- If the discovery document advertises `code_challenge_methods_supported` without `S256`,
  configuration MUST be rejected. A provider that omits the field is accepted.
- A provider whose discovery fails MUST NOT block startup or readiness. Its login button is
  shown disabled with the error, discovery is retried with backoff (at most 5 min), and package
  traffic and break-glass login keep working.
- `extra_auth_params` may add authorization request parameters, for example `kc_idp_hint` or
  `domain_hint`. It MUST NOT override `response_type`, `client_id`, `redirect_uri`, `scope`,
  `state`, `nonce`, `code_challenge` or `code_challenge_method`; such keys are rejected at
  startup.
- `GET /api/v1/settings/auth` (R-AUTH-040) lists each provider read-only: `id`, `displayName`,
  `issuer`, the discovery status, and the last error with its time. The client secret is never
  listed.

Configuration:

```yaml
auth:
  oidc:
    providers:
      - id: corp                                   # example
        display_name: Corporate SSO                # example
        issuer: https://login.example.com/realms/acme  # example
        client_id: trove                         # example
        client_secret_file: /var/run/secrets/trove/oidc-corp/client-secret  # example
        client_auth_method: client_secret_basic    # default (proposed)
        scopes: [openid, profile, email]           # default (proposed); add the scope that releases groups, e.g. "groups"
        extra_auth_params: {}                      # default (proposed)
        allowed_signing_algs: [RS256, PS256, ES256]  # default (proposed)
        trusted_audiences: []                      # default (proposed)
        clock_skew: 60s                            # default (proposed)
        max_id_token_age: 10m                      # default (proposed)
        use_user_info: true                        # default (proposed)
        discovery_refresh: 24h                     # default (proposed)
        rp_initiated_logout: true                  # default (proposed)
```

The claim, provisioning and group keys of the same provider entry are in R-AUTH-007 and
R-AUTH-008.

Acceptance criteria:

- Start a replica whose provider issuer is unreachable. `/readyz` turns ready, the login-options
  response shows the provider as unavailable, and `npm install` with a PAT succeeds.
- Configure an issuer whose discovery document returns a different `issuer`. The provider is
  marked unusable, and a log record names the mismatch.
- Rewrite the secret file. The next token request uses the new secret without a restart, as
  observed at a test IdP.

### R-AUTH-005 · Authorization request with PKCE, state and nonce
Priority: **MVP** · Covers: AUTHN-009, AUTHN-011

`GET /auth/oidc/{providerId}/start?return_to=<path>` starts a login. No credential is needed. The
endpoint is rate-limited per R-AUTH-038.

**Generation.** trove MUST generate the following with `crypto/rand`:

- `state`: 32 bytes, base64url;
- `nonce`: 32 bytes, base64url;
- the PKCE `code_verifier`: 32 bytes, base64url, 43 characters
  ([RFC 7636](https://www.rfc-editor.org/rfc/rfc7636) §4.1);
- `code_challenge`: `BASE64URL(SHA256(code_verifier))`, sent with `code_challenge_method=S256`
  (§4.2).

PKCE is always on and `plain` is never used ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700)
§2.1.1, §4.8).

**Authorization request.** It follows
[OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) §3.1.2.1 with:

- `response_type=code`, `client_id`, `redirect_uri` (R-AUTH-003), `scope`;
- `state`, `nonce`, `code_challenge`, `code_challenge_method`;
- the configured `extra_auth_params`.

**Browser binding.** The flow is bound to the browser by the cookie `__Host-trv_oidc_binding`,
whose value is 32 random bytes:

- attributes `Secure; HttpOnly; SameSite=Lax; Path=/; Max-Age=600`, without `Domain`. The
  `__Host-` prefix (Q-AUTH-2) requires `Path=/`; it stops a sibling subdomain from planting an
  attacker's binding value, which would otherwise reopen login CSRF;
- an existing valid value is reused, so parallel logins in several tabs do not overwrite each
  other.

This is the CSRF defense of [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.7.

**Flow state.** The flow state lives in PostgreSQL, so the callback may land on any replica and
the load balancer needs no session affinity (AUTHN-011, INV-18). No part of it is kept in a
replica's memory or in Valkey (Q-AUTH-12). The `session-cleanup` task deletes expired rows
(R-AUTH-043).

**`return_to`.** It MUST be a relative path that starts with `/ui/`, contains no `//`, no `\`
and no scheme. Any other value is replaced by `/ui/`. This is the open-redirect defense,
[RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.11.

Data model:

```sql
CREATE TABLE oidc_login_flow (
  state_sha256           bytea PRIMARY KEY,          -- SHA-256 of state; state itself is not stored
  provider_id            text        NOT NULL,
  browser_binding_sha256 bytea       NOT NULL,
  code_verifier          text        NOT NULL,       -- needed in clear for the token request
  nonce_sha256           bytea       NOT NULL,
  purpose                text        NOT NULL CHECK (purpose IN ('login', 'reauth')),
  reauth_session_id      uuid,                       -- set when purpose = 'reauth' (R-AUTH-011)
  return_to              text        NOT NULL,
  created_at             timestamptz NOT NULL DEFAULT now(),
  expires_at             timestamptz NOT NULL        -- created_at + 10 min
);
CREATE INDEX oidc_login_flow_expires ON oidc_login_flow (expires_at);
```

The table has the backup export class `transient` ([backup-and-restore.md](backup-and-restore.md)
R-BAK-004): it holds PKCE verifiers in clear, and a restore ends every flow in progress.

API:

| Method and path | Response |
|---|---|
| `GET /auth/oidc/{providerId}/start?return_to=/ui/…[&purpose=reauth]` | 302 to the authorization endpoint; 404 for an unknown provider; 503 `provider_unavailable` if discovery has not succeeded. `purpose=reauth` needs a live session and records it in `reauth_session_id` (R-AUTH-011). |

Acceptance criteria:

- The Location of `/auth/oidc/corp/start` meets all of the following:
  - it carries `code_challenge_method=S256`, a 43-character `code_challenge`, and `state` and
    `nonce` of 43 characters each;
  - the response sets `__Host-trv_oidc_binding` with `Secure`, `HttpOnly`, `SameSite=Lax`,
    `Path=/` and no `Domain`.
- In the e2e profile `scatter` ([architecture.md](architecture.md) R-ARCH-032: three `web`
  replicas, every request to another replica), 50 consecutive logins against the test IdP all
  succeed. With `server.replica_header: always`
  ([operations-and-observability.md](operations-and-observability.md)), the `X-Trove-Replica`
  headers show that start and callback of each login were served by different replicas.
- `return_to=https://evil.test/` and `return_to=//evil.test` both end the login at `/ui/`.

### R-AUTH-006 · Callback handling and ID-token validation
Priority: **MVP** · Covers: AUTHN-009

`GET /auth/oidc/{providerId}/callback` MUST perform these steps in order. Any failure ends the
login with an error page (HTTP 400, or 502 for IdP errors) and the audit event
`auth.login.failed` with a reason code. No session is created.

**Validating the response (steps 1–5):**

1. Delete and return the flow row whose `state_sha256` matches the `state` parameter (`DELETE …
   RETURNING`). This makes the flow single-use, also when the same callback reaches two replicas
   at once: only one of them gets the row. Missing or expired gives `flow_expired`. The delete
   commits on its own, before step 6, because no outbound call runs inside a transaction
   ([architecture.md](architecture.md) R-ARCH-019).
2. The flow's provider MUST equal the path's provider. Otherwise `provider_mismatch`.
3. The SHA-256 of the `__Host-trv_oidc_binding` cookie MUST equal the flow's binding. Otherwise
   `browser_mismatch`.
4. If the response carries `iss`, it MUST equal the issuer. If discovery advertises
   `authorization_response_iss_parameter_supported: true`, `iss` is mandatory
   ([RFC 9207](https://www.rfc-editor.org/rfc/rfc9207)). Otherwise `issuer_mismatch`.
5. If the response carries `error`, the error code is recorded (`idp_error`). The IdP's
   `error_description` is shown HTML-escaped and truncated to 200 characters.

**Exchanging the code (step 6):**

6. Redeem the code at the token endpoint over TLS with these parameters, using a 10 s timeout:
   - `grant_type=authorization_code`, `code`, `redirect_uri`;
   - `code_verifier` ([RFC 7636](https://www.rfc-editor.org/rfc/rfc7636) §4.5);
   - the configured client authentication.

**Validating the ID token (step 7).** This follows
[OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) §3.1.3.7:

| Check | Rule | Reason code |
|---|---|---|
| Form | JWS compact serialization; encrypted ID tokens are not supported | `id_token_invalid` |
| `alg` | In `allowed_signing_algs`; `none` and `HS*` are always refused | `id_token_invalid` |
| Signature | Verified with the JWKS key named by `kid` | `id_token_invalid` |
| `iss` | Exactly the configured issuer | `id_token_invalid` |
| `aud` | Contains `client_id`; every other value must be in `trusted_audiences` | `id_token_invalid` |
| `azp` | If present, or if `aud` has more than one value, MUST equal `client_id` | `id_token_invalid` |
| `exp`, `nbf` | `exp > now − clockSkew`; `nbf ≤ now + clockSkew` if present | `id_token_expired` |
| `iat` | `now − maxIDTokenAge ≤ iat ≤ now + clockSkew` | `id_token_invalid` |
| `nonce` | The SHA-256 equals the flow's `nonce_sha256` ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.5.3.2) | `nonce_mismatch` |
| `sub` | Non-empty, at most 255 characters | `id_token_invalid` |

**Completing the login (steps 8–10):**

8. If `use_user_info` is set and discovery lists a `userinfo_endpoint`, call it with the access
   token, using a 10 s timeout:
   - The response's `sub` MUST equal the ID token's `sub`
     ([OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) §5.3.2).
     Otherwise `userinfo_mismatch`.
   - Only JSON responses are supported.
   - For each mapped claim, the ID-token value wins and userinfo fills the gaps.
9. The IdP's access token and refresh token MUST be discarded after step 8. They are never
   stored and never accepted as credentials (INV-8).
10. Continue with R-AUTH-007 (user resolution), R-AUTH-008 (group sync) and R-AUTH-010 (session),
    in one transaction that starts after step 8. For a returning identity, that transaction first
    locks the user's `principal` row (`SELECT … FOR UPDATE`), so concurrent logins of one user whose
    callbacks reach different replicas apply their claims, memberships and session limit one after
    the other. A first login has no row to lock yet; R-AUTH-007 resolves its race. Then redirect
    with 302 to the flow's `return_to`.

At most three outbound calls (the token endpoint in step 6, a JWKS refetch for an unknown `kid` in
step 7, userinfo in step 8), each bounded by the 10 s timeout of R-AUTH-004, bound the callback to
about 30 s plus two short transactions. That fits the `server.shutdown_timeout` of a `web` replica
with the chart's defaults (110 s, [ha-and-helm.md](ha-and-helm.md) R-HA-011), so a replica that is
drained finishes it ([architecture.md](architecture.md) R-ARCH-024). A replica killed, or cut at a
shorter deadline, between step 1 and step 10 leaves no session and no half-created user; the user
starts the login again.

Acceptance criteria:

- Replaying a callback URL that already succeeded gives 400 `flow_expired`.
- With the test IdP's token endpoint delayed by 5 s, `pg_stat_activity` shows no trove connection
  in state `idle in transaction` during the callback.
- A callback opened in a second browser without the binding cookie gives 400 `browser_mismatch`.
- The test IdP is switched to issue tokens:
  - with a wrong `nonce`, the login gives `nonce_mismatch`;
  - signed with an unpublished key, it gives `id_token_invalid`;
  - with `alg: none`, it gives `id_token_invalid`;
  - with an extra untrusted audience, it gives `id_token_invalid`.
- Each failure above writes one `auth.login.failed` audit event that carries the reason.
- A mix-up test uses two providers. Feeding provider A's callback a `state` created for provider
  B gives `provider_mismatch`.

### R-AUTH-007 · Claim mapping and just-in-time provisioning
Priority: **MVP** · Covers: AUTHN-005, AUTHN-006, IAM-001

**Identity key.** The key of an external identity is the pair (`iss`, `sub`), the only stable
identifier in [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html)
§5.7. Usernames and email addresses MUST NOT be used to find the user of a returning identity.
Two provider entries with the same issuer resolve to the same user.

**Claim mapping.** Mapping is per provider:

| trove attribute | Default claim | Rule |
|---|---|---|
| Username | `preferred_username` | The value goes through Unicode NFC, then lower case, then `username_prefix` is prepended. The result must match `^[a-z0-9][a-z0-9._@+-]{0,127}$` and must not be reserved (`anonymous`, `authenticated`, `system`, `me`, the break-glass username; see below). Otherwise the login fails with `username_invalid`. |
| Email | `email` | Stored together with `email_verified`. It is never used for matching. |
| Display name | `name` | Free text, at most 256 characters. |
| Groups | `groups` | See R-AUTH-008. |

Claim names may be dotted paths into nested JSON objects, for example `realm_access.roles`.

**Reserved usernames.** `anonymous`, `authenticated`, `system`, `me` and the configured
break-glass username (`auth.break_glass.username`, R-AUTH-014) are reserved. `anonymous` and
`authenticated` name the built-in principals (R-AUTH-037); a user of either name could be
mistaken for them wherever principals are shown by name, for example when an administrator picks
the principal of a policy entry. `me` is reserved because
`/api/v1/users/me` is a literal route at the position of `{username}`, so a user of that name
would be unreachable by name ([api-and-ui.md](api-and-ui.md) R-API-004, Q-API-7). The rule holds
for every source of a username:

- an OIDC login, including a rename through `sync_username`, fails with `username_invalid`;
- `POST /api/v1/users` answers 422 `reserved_name` (R-AUTH-016);
- SCIM (R-AUTH-039) answers 400 with `scimType: invalidValue`
  ([RFC 7644](https://www.rfc-editor.org/rfc/rfc7644) §3.12).

The OpenAPI document lists them as `x-trove-reserved-names` of the username schema. A
break-glass username of `anonymous`, `authenticated`, `system` or `me` makes startup fail.

**First login of an identity.** No (`iss`, `sub`) link exists yet. Exactly one of these applies:

1. **Pending link.** An administrator or SCIM pre-provisioned a user with a pending link for
   this provider and the mapped username (R-AUTH-016, R-AUTH-039). The link is completed by
   setting `subject`. Each pending link is used once.
2. **Username taken.** Another user already holds the mapped username. The login fails with
   `username_taken`. Accounts are never linked automatically by username or email address,
   which prevents account takeover through a second IdP or a recycled address.
3. **Just-in-time creation.** `jit` is true. A user with origin `jit` is created and linked, and
   auto-join groups are applied (R-AUTH-019).
4. **Not provisioned.** None of the above holds. The login fails with `not_provisioned`.

Concurrent first logins of one identity, for example from two browser tabs whose callbacks reach
different replicas, race on `UNIQUE (issuer, subject)` and on the username. The losing transaction
fails with a unique violation and is retried once from the start of R-AUTH-006 step 10. The retry
finds the identity linked and continues as a returning login, or fails with `username_taken` if a
different identity holds the name. A pending link is completed with a conditional update
(`… SET subject = $1 WHERE id = $2 AND subject IS NULL`), so two concurrent logins cannot both use
it.

**Every login.** On each login trove:

- updates email, `email_verified` and display name from the claims;
- renames the user to a changed mapped username if `sync_username` is true and the new name is
  free; otherwise it keeps the old name and emits `user.rename_conflict`;
- records `last_login_at`, `last_interactive_auth_at` and the mapped claim values
  (`last_claims`). Raw tokens are never stored.

**Gates.** Gates run before the session is created and fail with the reason shown:

| Gate | Fails with |
|---|---|
| User disabled | `user_disabled`. Exception: a user disabled only for dormancy is re-enabled by a successful login (R-AUTH-021). |
| `require_verified_email` is set and `email_verified` is not `true` | `email_unverified` |
| `login_requires_groups` is non-empty and none of its values is in the raw group claim | `not_in_allowed_group` |

**Providers with open registration.** A provider entry sets `public_issuer: true` when anyone can
obtain an account at the issuer (open sign-up, a public SaaS instance). trove treats the issuers
`https://gitlab.com` and `https://accounts.google.com` as public whatever the setting says. For a
public issuer, startup MUST refuse:

- `jit: true` together with an empty `login_requires_groups`. Every account of the issuer would
  otherwise become a trove user with the auto-join groups (R-AUTH-019).
- a `login_requires_groups` value or a group rule (R-AUTH-008) that is neither an exact `match` nor
  a `match_regex` whose literal prefix ends in `/` (a namespace the organization owns, such as
  `acme/`). On such issuers users create and name groups themselves, so a rule such as
  `trove-team-(.+)` is satisfied by a group anyone can register.

Transient users do not exist. Every OIDC login persists the user, so every human principal can
hold tokens and open the profile (AUTHN-006, R-AUTH-017).

Data model (the user tables are in R-AUTH-016):

```sql
CREATE TABLE user_identity (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      uuid        NOT NULL REFERENCES app_user (principal_id) ON DELETE CASCADE,
  provider_id  text        NOT NULL,
  issuer       text        NOT NULL,
  subject      text,                          -- NULL = pending link (pre-provisioned, R-AUTH-016)
  linked_at    timestamptz,
  last_claims  jsonb,                         -- mapped values of the last login only
  UNIQUE (issuer, subject),
  UNIQUE (user_id, provider_id)
);
```

Configuration (same provider entry as R-AUTH-004):

```yaml
        claims:
          username: preferred_username   # default (proposed)
          email: email                   # default (proposed)
          display_name: name             # default (proposed)
          groups: groups                 # default (proposed)
        username_prefix: ""              # default (proposed)
        sync_username: true              # default (proposed)
        jit: true                        # default (proposed)
        require_verified_email: false    # default (proposed)
        login_requires_groups: []        # default (proposed): any authenticated identity of this issuer
        public_issuer: false             # default (proposed); forced true for known public issuers
```

Acceptance criteria:

- The first login of `alice` creates a user with origin `jit` and writes one `user.created`
  event with `source=jit`. A second login creates nothing new.
- A second provider with a different issuer returns `preferred_username=alice`. Its login fails
  with `username_taken`, and the existing `alice` is unchanged.
- The IdP renames `alice` to `alice.smith`. The next login renames the user, and the user's
  PATs keep working, because policies and tokens reference the principal ID.
- With `jit: false`, an unknown identity gets `not_provisioned`. A user pre-created with a
  pending link for `corp` logs in once and is linked.
- A provider with issuer `https://gitlab.com`, `jit: true` and no `login_requires_groups` makes
  startup fail with a message naming the provider. With `login_requires_groups: [acme]` and the
  group rule `match_regex: "acme/trove-(.+)"` it starts; with the rule
  `match_regex: "trove-(.+)"` it fails again.

### R-AUTH-008 · Group synchronization from claims
Priority: **MVP** · Covers: IAM-013, IAM-014, IAM-016

**Reading the claim.** On every login, and on every re-authentication (R-AUTH-011), trove
reads the configured group claim:

- The claim must be a JSON string or an array of strings. Only the first 1000 values are used.
- The claim counts as missing in any of these cases:
  - it is absent from both the ID token and userinfo;
  - it has another type;
  - it is replaced by an overage marker. An example is the distributed-claims indicator
    `_claim_names.groups`; distributed claims are not resolved.

**Mapping rules.** Rules turn claim values into trove group names:

- `match` compares a value exactly and case-sensitively.
- `match_regex` is RE2 (Go `regexp`), implicitly anchored to the whole value. `group` may use
  `$1` or `${name}` expansions from it.
- The target set is the union of the results of all rules.
- A result that violates the group-name rule of R-AUTH-018 is skipped with a warning event.

**Missing target groups.** If a target group does not exist:

- with `create_groups: false`, it is skipped;
- otherwise it is created with origin `oidc`. The insert uses `ON CONFLICT DO NOTHING` and then
  reads the group, so two logins on different replicas that create the same group at the same
  moment both end up with the one group.

**Writing the memberships.** In one transaction, trove sets the user's memberships whose
source is `oidc:<providerId>` to exactly the target set. Memberships with source `manual`,
`auto` or `scim` are never touched. If anything changed, the same transaction records the change
in the change area `auth` ([architecture.md](architecture.md) R-ARCH-018, R-AUTH-023), so every
replica applies the new memberships within `auth.revocation_bound`, and writes a
`user.groups.synced` event with the added and removed groups.

**Missing claim.** The behavior follows `on_missing_claim` (default `clear`, decided in
Q-AUTH-4). It applies to all three cases above alike, including the overage marker:

| Value | Behavior |
|---|---|
| `clear` (default) | Remove all `oidc:<providerId>` memberships and continue. Fails closed: an IdP misconfiguration removes rights, it never adds or keeps them. |
| `keep` | Leave the memberships unchanged. |
| `deny` | Fail the login with `groups_claim_missing`. |

With `clear` and `keep`, every login with a missing claim writes the warning event
`user.groups.claim_missing` (R-AUTH-041) with the provider and `details.reason`: `absent`,
`wrong_type` or `overage`. It also increments
`trove_auth_group_claim_missing_total{provider,reason}`. trove also records the reason with the
identity's `last_claims` (R-AUTH-007). The user's profile (R-AUTH-017) and
`GET /api/v1/users/me` show it as `groupClaimMissing: {"provider", "reason"}` until a later
login delivers the claim. This makes a broken group mapper or an overage visible to administrators and
to the affected users themselves, who can still log in with their direct grants. An
administrator who loses rights this way sees why on the profile and recovers through the
break-glass account (R-AUTH-014) or by fixing the IdP mapper.

**Administrator rights.** A group becomes an administrator group by a role binding in
[authorization.md](authorization.md); there is no administrator flag on the group record. If a
synchronized group carries that binding, the IdP decides who is administrator. See the security
notes.

**Effect on tokens.** Group changes at the IdP reach trove only at the user's next login, or
through SCIM (R-AUTH-039). Tokens evaluate group memberships at use time, so a PAT gains or
loses group rights with that next sync. See H-16.

Configuration:

```yaml
        group_sync:
          enabled: true                          # default (proposed)
          on_missing_claim: clear                # default (proposed): clear | keep | deny (Q-AUTH-4)
          create_groups: false                   # default (proposed)
          rules:
            - match: trove-platform-admins     # example
              group: platform-admins             # example
            - match_regex: "trove-team-(.+)"   # example
              group: "team-$1"                   # example
```

Acceptance criteria:

- The test IdP sends `groups: ["trove-team-web", "unrelated"]`. After login the user is a
  member of `team-web` with source `oidc:corp`, and `unrelated` is ignored.
- At the next login the IdP sends `groups: []`. The `oidc:corp` membership is removed, and a
  manual membership in `team-web` added by an administrator survives.
- The IdP omits the claim. With the default, all `oidc:corp` memberships are removed, the
  login succeeds, one `user.groups.claim_missing` event with `reason=absent` is written, and
  `GET /api/v1/users/me` returns `groupClaimMissing` with `reason: "absent"`. The next login
  that carries the claim clears the field. With `deny`, the login fails with
  `groups_claim_missing`.
- The IdP sends `_claim_names: {"groups": "src1"}` instead of the claim. With the default, the
  login succeeds without `oidc:corp` memberships, and the event carries `reason=overage`.

### R-AUTH-009 · Multiple providers and the login page
Priority: **MVP** · Covers: AUTHN-007, AUTHN-037

`GET /api/v1/auth/login-options` is unauthenticated. The UI login page (screen in
[api-and-ui.md](api-and-ui.md)) renders only from this response:

```json
{
  "providers": [
    {"id": "corp", "displayName": "Corporate SSO", "startUrl": "/auth/oidc/corp/start", "available": true}
  ],
  "localLogin": false,
  "anonymousAccess": true,
  "autoRedirect": "corp"
}
```

- Providers are listed in configuration order. The response never contains issuers, client IDs
  or error details. Those are for administrators (R-AUTH-040).
- `auto_redirect` names the single provider to which the login page redirects immediately. It is
  set only when exactly one provider is configured and `auth.login_page.auto_redirect` is true.
- `localLogin` is true only when the break-glass account is enabled and
  `auth.break_glass.show_on_login_page` is true. Independently of that, the local form at
  `/ui/login?local=1` is reachable whenever the break-glass account is enabled (R-AUTH-014).

Configuration:

```yaml
auth:
  login_page:
    auto_redirect: true  # default (proposed)
```

Acceptance criteria:

- With two providers, the login page shows two buttons in configuration order.
- With one provider and the default setting, opening `/ui/login` lands at the IdP without a
  click.

## Sessions

### R-AUTH-010 · Server-side UI sessions
Priority: **MVP** · Covers: AUTHN-038, AUTHN-011

**Creation.** A session is created only after a successful OIDC callback (R-AUTH-006) or a
break-glass login (R-AUTH-014):

- A session cookie presented at that moment is ended and replaced, which prevents session
  fixation.
- The session secret is 32 random bytes, base64url (43 characters).
- PostgreSQL stores only its SHA-256.

**Cookie.** The cookie is set as:

```text
Set-Cookie: __Host-trv_session=<secret>; Secure; HttpOnly; SameSite=Lax; Path=/
```

- It has no `Domain`, `Max-Age` or `Expires` attribute, so it lives for the browser session and
  is host-only.
- `SameSite=Lax` rather than `Strict` is deliberate. The first navigation after the cross-site
  IdP redirect must carry the cookie.
- The `__Host-` prefix (decided in Q-AUTH-2) makes the browser refuse any cookie of this name
  that lacks `Secure` or `Path=/` or carries a `Domain` attribute (cookie name prefixes in
  [draft-ietf-httpbis-rfc6265bis](https://datatracker.ietf.org/doc/draft-ietf-httpbis-rfc6265bis/)).
  A sibling subdomain or a plain-HTTP response therefore cannot plant or shadow it. All three
  trove cookies carry the prefix: `__Host-trv_session`, `__Host-trv_csrf` (R-AUTH-012) and
  `__Host-trv_oidc_binding` (R-AUTH-005). This amends the cookie name in INV-8.

**Validation.** Lookup follows the token path of R-AUTH-023: the replica's in-process session
cache, invalidated through the change area `auth`, with the same revocation bound and the same
freshness gate. No session is read from or written to Valkey (Q-AUTH-9). The principal must still
be active.

**Deadlines are decided from the row (Q-AUTH-13).** Every replica ends a session at the same
moment, whichever replica the request reaches:
- `absolute_expires_at` never changes, so a cached entry decides it.
- The idle deadline is `last_seen_at + idle_timeout` as stored in PostgreSQL. A cached entry whose
  idle deadline has passed is not trusted: the replica reads the row again. Only if the row's
  deadline has passed too does the session end, with reason `idle`
  (`UPDATE … SET ended_at = now(), end_reason = 'idle' WHERE id = $1 AND ended_at IS NULL`), and
  the request gets 401 `session_expired`. A replica that has not seen the session for an hour
  therefore never ends it while the user works through other replicas.

**Activity.** A request with a valid session counts as activity. The touch is this statement:
`UPDATE ui_session SET last_seen_at = now() WHERE id = $1 AND ended_at IS NULL AND last_seen_at < now() - interval '60 seconds'`.
- The condition keeps the row at one change per minute whatever the number of replicas.
- The statement returns the row's `last_seen_at` whether or not it changed it (a data-modifying
  CTE). The replica keeps the newest `last_seen_at` it has read for the session, from that result
  or from a cache fill, and sends the touch only for a request that arrives 60 s or more after that
  value.
- The kept value never exceeds the row's, so a replica never skips a touch that the row needs. The
  value only saves statements, never decides that a session has ended, and no later request
  depends on it. The time of the replica's own last touch is not used for this decision: after a
  touch that changed nothing, the row may be up to 60 s older than that time.
- A failed write is counted in `trove_auth_session_touch_failures_total` and never fails the
  request; the kept value stays as it was, so the next request sends the touch again.

After any request at time *L* whose touch did not fail, the row's `last_seen_at` is at least
*L* − 60 s. A session therefore
ends between `idle_timeout` − 60 s and `idle_timeout` after its last request, on every replica
alike.

**Concurrency.** A user may hold up to `max_per_user` concurrent sessions. Creating one more ends
the oldest with reason `evicted`. The creating transaction locks the user's `principal` row
(`SELECT … FOR UPDATE`) before it counts: an OIDC login takes the lock at the start of R-AUTH-006
step 10, the break-glass login before it creates the session. Concurrent logins of one user on
different replicas therefore cannot exceed the limit.

**Housekeeping.** The `session-cleanup` task (R-AUTH-043) deletes ended and expired rows 30 days
after their end. A session that expired without a request has no `ended_at`; its end is the
earlier of its idle and absolute deadlines.

Data model:

```sql
CREATE TABLE ui_session (
  id                  uuid        PRIMARY KEY,           -- UUIDv7; shown in session lists
  secret_sha256       bytea       NOT NULL UNIQUE,
  csrf_sha256         bytea       NOT NULL,              -- R-AUTH-012
  principal_id        uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  auth_method         text        NOT NULL CHECK (auth_method IN ('oidc', 'break_glass')),
  provider_id         text,
  idp_sid             text,                              -- 'sid' claim if present
  id_token            text,                              -- for id_token_hint (R-AUTH-013); never returned by any API
  authenticated_at    timestamptz NOT NULL,              -- last interactive authentication (R-AUTH-011)
  created_at          timestamptz NOT NULL DEFAULT now(),
  last_seen_at        timestamptz NOT NULL DEFAULT now(),
  idle_timeout        interval    NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  created_addr        inet,
  user_agent          text,
  ended_at            timestamptz,
  end_reason          text CHECK (end_reason IN ('logout', 'idle', 'absolute', 'evicted', 'revoked',
                                                 'principal_disabled', 'reauth_rotated', 'password_changed'))
);
CREATE INDEX ui_session_principal ON ui_session (principal_id) WHERE ended_at IS NULL;
```

The table has the backup export class `transient` ([backup-and-restore.md](backup-and-restore.md)
R-BAK-004): it holds raw ID tokens, and a restore ends every session.

API:

| Method and path | Purpose | Permission | Status codes |
|---|---|---|---|
| `GET /api/v1/users/me/sessions` | List own active sessions: `id`, `createdAt`, `lastSeenAt`, `createdAddr`, `userAgent`, `authMethod`, `providerId`, `current` | session or token of the user | 200 |
| `DELETE /api/v1/users/me/sessions/{id}` | End one own session | same | 204, 404 |
| `DELETE /api/v1/users/me/sessions?keepCurrent=true` | End all own sessions, optionally except the current one | same | 204 |
| `GET /api/v1/users/{username}/sessions` | List a user's sessions | administrator | 200, 404 |
| `DELETE /api/v1/users/{username}/sessions` | End all of a user's sessions | administrator | 204, 404 |

Configuration:

```yaml
auth:
  sessions:
    idle_timeout: 1h         # default (proposed); upper bound 24h
    absolute_timeout: 12h    # default (proposed); upper bound 168h
    max_per_user: 20         # default (proposed)
    recent_auth_window: 1h   # default (proposed); R-AUTH-011
```

Acceptance criteria:

- After login the browser holds `__Host-trv_session` with `Secure`, `HttpOnly`, `SameSite=Lax`,
  `Path=/` and no `Domain`. The database holds only its SHA-256.
- A response from a sibling host that sets `trv_session` or `__Host-trv_session` with
  `Domain=<parent domain>` leaves the browser's cookie for trove unchanged (browser test).
- Ending a session through the API makes the next request with that cookie fail with 401
  `session_expired` within the revocation bound of R-AUTH-023. This holds on every replica, also
  with Valkey stopped.
- A 21st login of the same user ends the oldest session. 30 concurrent logins of one user, spread
  over three `web` replicas, leave exactly `max_per_user` active sessions.
- In the `scatter` profile with `idle_timeout: 5m`, a session used every 30 s for 20 minutes, each
  request on another replica, stays valid. After 6 minutes without a request, every replica
  answers 401 `session_expired`, and the row's `end_reason` is `idle`.

### R-AUTH-011 · Session timeouts and recent authentication
Priority: **MVP** · Covers: AUTHN-038

**Timeouts.** A session ends at whichever comes first: `last_seen_at + idle_timeout` or
`absolute_expires_at`, both as stored in the session row (R-AUTH-010), so every replica ends it
at the same time. Break-glass sessions use their own shorter values (R-AUTH-014). Startup
MUST reject configured timeouts above the upper bounds of R-AUTH-010.

**Recent authentication.** Some actions mint long-lived credentials:

- creating a PAT or SAT through a session;
- approving a command-line login (R-AUTH-034, R-AUTH-036);
- changing authentication settings (R-AUTH-040).

These actions MUST require `now − authenticated_at ≤ recentAuthWindow`. Otherwise the API
answers 401 `reauthentication_required`.

**Re-authentication round trip.** The UI then navigates to
`/auth/oidc/{providerId}/start?purpose=reauth&return_to=…`:

- The flow carries the current session's ID.
- On callback, the ID token's (`iss`, `sub`) MUST match the session's user. Otherwise the result
  is `reauth_user_mismatch`, and the original session is unchanged.
- On success a new session replaces the old one (end reason `reauth_rotated`), with a fresh
  `authenticated_at`.

The round trip sends no `prompt` parameter, so an IdP with a live session answers silently. It
still re-confirms at the IdP that the account is active, so a stolen session cannot mint tokens
after the user was deactivated there. Forcing a fresh credential entry (`max_age=0`) is
deferred together with the authentication-strength check (H-18).

Acceptance criteria:

- Set the session clock past `idle_timeout` without requests. The next request gets
  `session_expired`. Requests every 5 minutes keep the session alive until `absolute_timeout`,
  then it ends.
- Set `authenticated_at` to 2 h ago. `POST /api/v1/tokens` with the session gets 401
  `reauthentication_required`. After the silent round trip the same call succeeds, and the old
  session cookie is invalid.

### R-AUTH-012 · CSRF protection for cookie-authenticated requests
Priority: **MVP** · Covers: trove-only

**The CSRF token.** Each session has a CSRF token: 32 random bytes, base64url. It is delivered in
a second cookie:

```text
Set-Cookie: __Host-trv_csrf=<token>; Secure; SameSite=Strict; Path=/
```

The cookie is not `HttpOnly`. The Angular `HttpClient` XSRF support reads it, configured with
`withXsrfConfiguration({cookieName: '__Host-trv_csrf', headerName: 'X-CSRF-Token'})`
([Angular security guide](https://angular.dev/best-practices/security)).

**Checks on unsafe requests.** A `POST`, `PUT`, `PATCH` or `DELETE` request that is
authenticated by the session cookie, on `/api/v1/…`, `/files/…` or `/auth/logout`, MUST pass both
checks:

1. The `X-CSRF-Token` header's SHA-256 equals `csrf_sha256` of the session, compared in constant
   time.
2. Its origin is the base URL's origin. This is satisfied in either case:
   - the `Origin` header equals the base URL's origin;
   - `Origin` is absent and `Sec-Fetch-Site` is `same-origin`
     ([Fetch Metadata](https://www.w3.org/TR/fetch-metadata/)).

   If both headers are absent, the request is rejected.

Failures answer 403 `csrf_failed` and are audited.

**Exempt requests.** Requests that carry an `Authorization` header are exempt, and their cookies
are ignored (R-AUTH-001). Package-protocol endpoints (`/api/<format>/…`) never accept the cookie
for unsafe methods, so they have no CSRF exposure.

**Related rules:**

- No endpoint answers with `Access-Control-Allow-Credentials: true`. Cross-origin use of the
  session is impossible by construction. The rest of CORS is owned by
  [api-and-ui.md](api-and-ui.md).
- The browser binding of R-AUTH-005 defeats login CSRF.
- The command-line approval pages (R-AUTH-034, R-AUTH-036) and the login page MUST send
  `Content-Security-Policy: frame-ancestors 'none'`. This is the clickjacking defense,
  [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.16.

Acceptance criteria:

- An HTML form on another origin posting to `/api/v1/tokens` with the victim's cookie gets 403
  `csrf_failed`. A `fetch` from the UI with the header succeeds.
- `curl -X DELETE -b "__Host-trv_session=$S" https://trove.test/api/v1/users/me/sessions`
  without the header gets 403.
- `curl -X PUT -b "__Host-trv_session=$S" -T x.bin https://trove.test/files/generic-local/x.bin`
  without the header gets 403 `csrf_failed`. A `PUT` on `/api/npm/npm-local/…` carrying only the
  cookie gets 401.

### R-AUTH-013 · Logout and RP-initiated logout
Priority: **MVP** · Covers: AUTHN-038

**Logout.** `POST /auth/logout` (cookie plus CSRF) does the following:

- ends the session (reason `logout`) and records the end in the change area `auth` in the same
  transaction (R-AUTH-023);
- clears `__Host-trv_session` and `__Host-trv_csrf` with `Max-Age=0`, `Secure` and `Path=/`
  and without `Domain`. The browser ignores a `__Host-` Set-Cookie that lacks these attributes,
  so a bare `Max-Age=0` would leave the cookie in place;
- answers 200 with `{"redirectTo": "<url>"}`. The UI navigates there.

**Where `redirectTo` points:**

- If the provider has an `end_session_endpoint` and `rp_initiated_logout` is true, it is that
  endpoint with these parameters, per
  [OpenID Connect RP-Initiated Logout 1.0](https://openid.net/specs/openid-connect-rpinitiated-1_0.html):
  - `id_token_hint` (the stored ID token);
  - `client_id`;
  - `post_logout_redirect_uri=<baseURL>/ui/logged-out`.
- Otherwise it is `/ui/logged-out`.

The IdP client registration must list the post-logout URI.

**Other rules:**

- Logout is idempotent. With an already ended session it still answers 200 and
  `/ui/logged-out`.
- `GET /auth/logout` answers 405, so that image tags and links cannot log users out.
- Logout ends only the browser session. PATs are independent credentials and stay valid. Ending
  every session is `DELETE /api/v1/users/me/sessions`; revoking every token is R-AUTH-029.
- IdP-initiated logout (back-channel or front-channel) is deferred. The `idp_sid` column keeps
  the door open for it.

Acceptance criteria:

- Logout against a Keycloak test IdP ends the IdP session too. A new `/auth/oidc/corp/start`
  asks for credentials again.
- After logout, the old cookie gets 401 on every replica within the revocation bound of
  R-AUTH-023.

## Break-glass administrator

### R-AUTH-014 · Break-glass administrator
Priority: **MVP** · Covers: IAM-002, IAM-010, AUTHN-003, IAM-036, IAM-069

**The account.** There is exactly one local account (INV-7). It is a user row with
`is_break_glass = true` and origin `system`, named `auth.break_glass.username`. It always holds
system administration: [authorization.md](authorization.md) MUST treat it as administrator
without a role binding, so that recovery cannot depend on the state it recovers.

**Password source.** The password is never stored in PostgreSQL. It comes from
`auth.break_glass.password_file`, a mounted Kubernetes Secret, in one of two forms:

- an Argon2id hash in PHC string format (preferred; the plaintext then never enters the
  cluster);
- a plaintext password, which each replica hashes in memory at startup.

Only replicas that serve the client listener read the file: `web`, and `all` in the local profile.
A `worker` replica serves no login, neither needs nor reads the file, and does not get the Secret
mounted (R-AUTH-042). The rules below that make a replica refuse to start apply to `web` replicas.

**Hashing.** Verification uses Argon2id ([RFC 9106](https://www.rfc-editor.org/rfc/rfc9106)) with
at least `m=65536` (KiB), `t=3` and `p=4`, which is the second recommended option of §4.

Each verification holds the memory its parameters name, at least 64 MiB. A replica therefore runs
at most `max_concurrent_verifications` of them at a time. An attempt that cannot start one within 1 s answers 503 `server_busy` with
`Retry-After: 1`, like the in-flight limit of [architecture.md](architecture.md) R-ARCH-005, and
is counted in `trove_auth_ratelimited_total{limit="break_glass_verifications"}`. Without this
bound, concurrent attempts from many addresses, each within its per-address limit (R-AUTH-038),
could exhaust a `web` replica's memory, and random distribution would carry them to every replica.

**Password strength.** A plaintext password MUST have at least 20 characters and MUST NOT appear
in an embedded list of the 10,000 most common passwords. Otherwise the replica refuses to start
(IAM-069). The strength of a supplied hash cannot be checked; the replica logs a warning.

**Password rotation (Q-AUTH-14).** The file is re-read when its modification time changes. The
kubelet of each node delivers a rotated Secret to its pods on its own schedule, within its sync
period plus the cache propagation delay
([Secrets](https://kubernetes.io/docs/concepts/configuration/secret/); `syncFrequency` defaults to
1 min in the
[kubelet configuration](https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/)),
so replicas switch to the new password at different moments, within the Secret propagation time
of [ha-and-helm.md](ha-and-helm.md) R-HA-030 (2 minutes). A changed password ends all break-glass
sessions (reason `password_changed`). The change is coordinated in PostgreSQL, so it does not
depend on the replica that noticed it staying alive:
- A `web` replica whose file content changes upserts the `break-glass` row of `auth_lockout`
  (R-AUTH-038) with `password_changed_at = now()`, unless a rotation hold opened less than
  `rotation_hold` ago. If it opened a hold, the same transaction ends every break-glass session
  created before it and records the ends in the change area `auth` (R-AUTH-023). Concurrent
  detections serialize on the row, so one rotation opens one hold.
- For `rotation_hold` after `password_changed_at`, the local login answers 503
  `break_glass_rotating` with `Retry-After` on every replica, whichever file the replica holds.
  Every attempt reads that row anyway (R-AUTH-038), so this costs no query. Neither the old nor the
  new password opens a session while the replicas disagree.
- After the hold, every replica holds the new file within the propagation time, so only the new
  password works. A replica that notices the change later than the hold opens a new hold, which
  ends every break-glass session again; a session opened with the new password before that has to
  sign in again.
- The ends are one committed transaction, so they hold when the replica that noticed the change is
  drained right afterwards. A rotation that no running replica notices is H-52.

**Disabled account.** If `enabled` is false, the local login is refused with 403
`local_login_disabled`, and a replica rejects every break-glass session presented to it with 401
`session_expired` and ends it. The rule is checked on every request, so a session that a replica
still running with `enabled: true` opened during the rollout of the change is ended on its next
request to an updated replica. Startup also ends any remaining break-glass sessions. With
`enabled` true and no readable password file, the replica refuses to start.

**Login.** The local login is `POST /auth/local/login` with `{"username": "...", "password":
"..."}`:

- The request must pass the origin check of R-AUTH-012.
- The answer is 204 with the session cookies, or 401 `invalid_credentials`. During a rotation
  hold it is 503 `break_glass_rotating`, and while the replica's verification slots are taken
  503 `server_busy` (above).
- Argon2 verification always runs, also for unknown usernames, so response time does not reveal
  the account name.
- Lockout follows R-AUTH-038.

**Restrictions.** The break-glass password is accepted only by this endpoint, never as a Basic
password (R-AUTH-001). This is how trove maps the catalog's switch for disabling Basic
authentication with internal passwords (IAM-036): password Basic authentication does not exist.

The break-glass account:

- cannot own tokens;
- cannot create tokens for anyone, including service accounts;
- cannot approve command-line logins;
- cannot be deleted or renamed through the API (409 `break_glass_immutable`).

Bootstrapping automation uses `trove admin` instead (R-AUTH-015).

**Visibility.** Every break-glass login (success or failure) writes an `auth.breakglass.login`
event with severity `high`. The UI shows a permanent banner during a break-glass session.

Configuration:

```yaml
auth:
  break_glass:
    enabled: true                                   # default (proposed); disable once an OIDC administrator exists
    username: admin                                 # default (proposed)
    password_file: /var/run/secrets/trove/break-glass/password  # example
    show_on_login_page: false                       # default (proposed)
    idle_timeout: 15m                               # default (proposed)
    absolute_timeout: 1h                            # default (proposed)
    rotation_hold: 2m                               # default (proposed); the Secret propagation time of ha-and-helm.md R-HA-030
    max_concurrent_verifications: 2                 # default (proposed); Argon2id verifications per replica at a time
```

Acceptance criteria:

- `curl -u admin:$BREAKGLASS https://trove.test/api/v1/auth/whoami` gets 401. The local form
  with the same password succeeds.
- Start a replica with a 12-character plaintext password. It exits with a non-zero status and
  names the strength rule.
- `POST /api/v1/tokens` from a break-glass session gets 403 `break_glass_restricted`.
- Rotate the Secret while three `web` replicas run and two break-glass sessions are open. From
  the moment the first replica notices the new file, the local login answers 503
  `break_glass_rotating` on every replica for `rotation_hold`. Afterwards the new password
  succeeds and the old one gets 401 on every replica, and both earlier sessions get 401
  `session_expired` on every replica. The same holds when the replica that noticed the change
  first is deleted with `--grace-period=0 --force` right after it noticed.
- A break-glass session is open, and `enabled: false` is rolled out. After the rollout, the
  session's next request gets 401 `session_expired` on every replica.
- 20 concurrent local-login attempts against one replica run at most 2 Argon2id verifications at
  a time; attempts that wait longer than 1 s get 503 `server_busy`, and the pod is not killed for
  memory.
- A `worker` pod rendered by the chart has no break-glass Secret mounted and starts with
  `auth.break_glass.enabled: true`.

### R-AUTH-015 · Bootstrap and recovery commands
Priority: **MVP** · Covers: TOK-041, IAM-010

The `trove admin` subcommands (INV-2) give an operator who can run commands in a replica pod
(`kubectl exec deploy/trove-web`, [ha-and-helm.md](ha-and-helm.md) R-HA-030) the bootstrap and
recovery operations. They use that pod's configuration and open their own short-lived database
connection; someone with that access already holds the database credentials. They load neither
the OIDC providers nor the break-glass password, so they also run in `worker` pods, which have
neither Secret (R-AUTH-042). They need no Valkey:
a revocation, a disable or a session end commits through the change log
([architecture.md](architecture.md) R-ARCH-018) and is in force on every replica within
`auth.revocation_bound` (R-AUTH-023), whichever pod ran the command. A role binding written by
`--admin` commits through the change area `authz` and is in force within the bound of
[authorization.md](authorization.md) R-AUTHZ-028.

| Command | Effect |
|---|---|
| `trove admin service-account create <name> [--description …] [--admin]` | Creates a service account. `--admin` binds the system role `admin` ([authorization.md](authorization.md) R-AUTHZ-010) in the transaction that creates the account, and that transaction calls `cluster.Notify("authz", …)` exactly as the role-binding API does (R-AUTHZ-022, R-AUTHZ-028), so every replica rebuilds its grant index. The command is one of the writes of R-AUTHZ-028's completeness test. |
| `trove admin token create --service-account <name> [--ttl 15m] [--scope-file scope.json]` | Creates a SAT and prints it once to stdout. The TTL defaults to 15 min; the maximum is the SAT maximum (R-AUTH-026). |
| `trove admin token revoke --id <uuid>` or `--value-stdin` | Revokes a token (R-AUTH-029). |
| `trove admin break-glass status` or `unlock` or `end-sessions` | Shows the lockout state, the rotation hold (R-AUTH-014) and the sessions, clears a lockout, or ends every break-glass session (reason `revoked`). |
| `trove admin user disable <username> [--keep-tokens]` | Disables a user (R-AUTH-016). |
| `trove admin sessions end --user <username>` or `--all` | Ends sessions. |

This replaces the catalog's file-drop bootstrap token (TOK-041). There is no request file and no
token file on disk. The token reaches only the operator's terminal or the Kubernetes Job that
ran the command; how such a Job feeds a Secret is up to [ha-and-helm.md](ha-and-helm.md).

Every command writes an audit event with actor `admin-cli`, the pod name and the OS user. Token
values are never logged.

**After a restore.** `ui_session` (R-AUTH-010), `oidc_login_flow` (R-AUTH-005) and `cli_login`
(R-AUTH-034) have the backup export class `transient`: no backup ever contains them
([backup-and-restore.md](backup-and-restore.md) R-BAK-004), and a restore leaves them empty
(R-BAK-022). A restore therefore ends every UI session, break-glass sessions included, and every
browser, npm web or device login in progress. Every user signs in again; a session cookie issued
before the restore names no row and gets 401 (R-AUTH-001). Tokens and principals are `data` and
come back as they were at the snapshot, and the access removals made after it are re-applied from
the revocation journal (R-AUTH-041, R-BAK-024).

Acceptance criteria:

- On a fresh installation, the following lets `$T` create a repository through
  `POST /api/v1/repositories`:

  ```shell
  kubectl exec deploy/trove-web -- trove admin service-account create bootstrap --admin
  T=$(kubectl exec deploy/trove-web -- trove admin token create --service-account bootstrap)
  ```

  After 15 minutes `$T` gets 401.
- `trove admin break-glass unlock` clears an active lockout immediately, on all replicas, because
  every login attempt reads the lockout row (R-AUTH-038).
- `kubectl exec deploy/trove-worker -- trove admin token revoke --id <id>`, run with Valkey
  stopped, makes the token fail on every `web` replica within `auth.revocation_bound`.
- `trove admin service-account create ops --admin` advances the `authz` epoch in `cluster_epoch`
  in the transaction that inserts the `authz_role_binding` row. Run with Valkey stopped, a token of
  `ops` then creates a repository through every `web` replica within the bound of
  [authorization.md](authorization.md) R-AUTHZ-028.
- A user signed in when a backup was taken, and still signed in when that backup is restored with
  `trove backup restore`, gets 401 with the same session cookie on every `web` replica afterwards.
  `ui_session`, `oidc_login_flow` and `cli_login` are empty, and the user's PAT created before the
  snapshot still authenticates.

## Principals

### R-AUTH-016 · User accounts
Priority: **MVP** · Covers: IAM-001, IAM-003, IAM-004, IAM-009, ADMAPI-004, ADMAPI-005, ADMAPI-006, ADMAPI-007, ADMAPI-008, ADMAPI-009

**Principal table.** All principal kinds share one table, so
[authorization.md](authorization.md) can bind policies to `principal.id` uniformly. The kinds
`anonymous` and `authenticated` each have exactly one built-in row (R-AUTH-037). The
`ci_mapping` subtype is owned by [gitlab-ci-oidc.md](gitlab-ci-oidc.md):

- Its principal `name` is `<CI issuer name>/<mapping name>`, for example
  `gitlab-com/acme-release-publish` (Q-CI-10). Mapping names are unique only per CI issuer, but
  both parts match `^[a-z0-9][a-z0-9._-]{1,63}$` and so contain no `/`, which makes the composite
  unique under `UNIQUE (kind, name)` and splittable back into its parts.
- It is never renamed, because both parts are immutable.
- The row is created and deleted together with the mapping, in the same transaction
  ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-009).

**User record:**

- Users have no password; only the break-glass account has one, and it lives outside the
  database.
- Status is `active` or `disabled`. The break-glass lockout of R-AUTH-038 appears separately as
  `lockedUntil`.
- `isAdmin` and `adminVia` are read-only. They are derived from the role bindings of
  [authorization.md](authorization.md).
- Users are addressed by username in paths. The record also returns the stable `id`.

**The catalog's per-user options (IAM-003) map to trove as follows.** trove has none of the
three switches; the table fixes the behavior that replaces each:

| Catalog option | trove |
|---|---|
| UI access disabled | No per-user switch. Every human user has UI access. API-only automation uses a service account (R-AUTH-020), which has no login, session or UI access. |
| Internal password disabled | Not applicable: no user has an internal password, so there is nothing to fall back to. |
| Profile updatable | No per-user switch. Users cannot edit their profile data in trove: email and display name come from the IdP and are overwritten at every login (R-AUTH-007). An administrator can correct them through `PATCH /api/v1/users/{username}` until the next login. |
| Password change (the catalog's open point under "profile updatable") | Not applicable: users have no password to change. The only password, the break-glass password, changes only by rotating its Kubernetes Secret (R-AUTH-014). No API changes it. |

**Disable.** `POST /api/v1/users/{username}/disable` does the following:

- ends all sessions;
- makes every token of the user fail with 401 `principal_disabled`;
- with `revokeTokens: true` (the default), also revokes the tokens with reason
  `principal_disabled`.

Enabling a user does not resurrect revoked tokens. Dormancy (R-AUTH-021) disables without
revoking.

**Delete:**

- It ends sessions and deletes the user's tokens, identity links and memberships. Policy bindings
  are removed by cascade ([authorization.md](authorization.md)).
- An audit event records the counts.
- A later login of the same identity creates a new principal with a new ID, so deleted rights
  never come back by name.

Data model:

```sql
CREATE TABLE principal (
  id          uuid        PRIMARY KEY,                     -- UUIDv7
  kind        text        NOT NULL CHECK (kind IN ('user', 'group', 'service_account', 'anonymous', 'authenticated', 'ci_mapping')),
  name        text        NOT NULL,                        -- ci_mapping: '<CI issuer name>/<mapping name>'
  status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid        REFERENCES principal (id) ON DELETE SET NULL,
  UNIQUE (kind, name)
);

CREATE TABLE app_user (
  principal_id              uuid PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
  email                     text,
  email_verified            boolean     NOT NULL DEFAULT false,
  display_name              text,
  origin                    text        NOT NULL CHECK (origin IN ('jit', 'admin', 'scim', 'system')),
  is_break_glass            boolean     NOT NULL DEFAULT false,
  scim_external_id          text UNIQUE,
  last_login_at             timestamptz,
  last_interactive_auth_at  timestamptz,
  disabled_reason           text CHECK (disabled_reason IN ('admin', 'dormant', 'scim')),
  disabled_at               timestamptz,
  dormancy_warned_at        timestamptz                  -- R-AUTH-021 (P1): last user.dormancy_warning
);
CREATE UNIQUE INDEX app_user_one_break_glass ON app_user (is_break_glass) WHERE is_break_glass;
```

The migration adds the integrity rules of Q-AUTH-16 to this sketch: a `CHECK` on `principal.name` by kind
(the name rules of R-AUTH-007, R-AUTH-018 and R-AUTH-020, the reserved user names, `<issuer>/<mapping>`
for `ci_mapping`), a unique index on the built-in kinds, the two built-in rows with the fixed
identifiers `00000000-0000-0000-0000-000000000001` (`anonymous`) and
`00000000-0000-0000-0000-000000000002` (`authenticated`), and triggers that refuse a change of those
rows and a subtype, membership, token or session row that points at a principal of the wrong kind.

API (administrator unless stated; read operations also accept the system scope
`identities:read`, R-AUTH-025):

| Method and path | Request | Response | Status codes |
|---|---|---|---|
| `GET /api/v1/users?q=&status=&origin=&group=&admin=&limit=&cursor=` | — | Page of users (paging per [api-and-ui.md](api-and-ui.md)) | 200 |
| `GET /api/v1/users/{username}` | — | User record (below) | 200, 404 |
| `GET /api/v1/users/me` | — | Own record; any authenticated user | 200, 401 |
| `POST /api/v1/users` | `{"username", "email", "displayName", "identities": [{"provider": "corp", "subject": null}]}` | User with origin `admin`; `subject: null` is a pending link (R-AUTH-007) | 201, 400, 409, 422 `reserved_name` (reserved username, R-AUTH-007) |
| `PATCH /api/v1/users/{username}` | Any of `displayName`, `email` (the IdP overwrites both at the next login), `identities` | User | 200, 400, 404 |
| `POST /api/v1/users/{username}/disable` | `{"reason": "…", "revokeTokens": true}` | — | 204, 404, 409 (break-glass) |
| `POST /api/v1/users/{username}/enable` | — | — | 204, 404 |
| `DELETE /api/v1/users/{username}` | — | — | 204, 404, 409 (break-glass) |
| `PATCH /api/v1/users/{username}/groups` | `{"add": ["team-web"], "remove": ["old"]}`, which affects `manual` memberships only | `{"groups": [{"name", "source"}]}` | 200, 404 (unknown user or group; nothing applied) |

```json
{
  "id": "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10",
  "username": "alice",
  "email": "alice@example.com",
  "emailVerified": true,
  "displayName": "Alice Example",
  "origin": "jit",
  "status": "active",
  "isAdmin": false,
  "adminVia": [],
  "identities": [{"provider": "corp", "linked": true}],
  "groups": [{"name": "team-web", "source": "oidc:corp"}],
  "lastLoginAt": "2026-10-28T09:12:00Z",
  "createdAt": "2026-09-01T08:00:00Z"
}
```

Acceptance criteria:

- Disabling `alice` with the defaults:
  - `npm whoami` with her PAT gets 401;
  - her open UI session gets 401 within the revocation bound of R-AUTH-023, on every replica;
  - re-enabling her does not make the old PAT valid again.
- Deleting `alice` and logging in again creates a user with a different `id`. Policies bound to
  the old principal do not apply.
- `POST /api/v1/users` with an existing username gets 409. With the username `me` it gets 422
  `reserved_name`, and an OIDC login whose mapped username is `me` fails with `username_invalid`.

### R-AUTH-017 · Self-service profile
Priority: **MVP** · Covers: AUTHN-039, AUTHN-006, AUTHN-040

**The profile.** Every persisted OIDC user has a profile; the screen is in
[api-and-ui.md](api-and-ui.md). It shows:

- the identity from the IdP, read-only;
- groups with their source, and a notice when the last login carried no usable group claim
  (`groupClaimMissing`, R-AUTH-008);
- the effective administrator flag;
- active sessions, which the user can end (R-AUTH-010);
- personal tokens, which the user can create, list, rotate and revoke (R-AUTH-027 to
  R-AUTH-030).

The profile needs no extra unlock step, because the UI session already proves the login.
Creating tokens is guarded by recent authentication (R-AUTH-011).

**Who may hold personal tokens.** The catalog switch for externally created users (AUTHN-006)
becomes `auth.tokens.pat.allowed_groups`:

- empty: every user may hold PATs;
- non-empty: only members of the listed groups may create them.

Existing PATs of users who leave those groups keep working until they expire or are revoked.

**Other principals.** Service accounts and the built-in principals `anonymous` and
`authenticated` have no profile. The break-glass account sees its profile without the tokens
section.

Configuration:

```yaml
auth:
  tokens:
    pat:
      allowed_groups: []  # default (proposed): everyone may create personal tokens
```

Acceptance criteria:

- With `allowedGroups: [developers]`, a user outside `developers` gets 403 `pat_not_allowed`
  from `POST /api/v1/tokens`. A member succeeds.

### R-AUTH-018 · Groups and memberships
Priority: **MVP** · Covers: IAM-013, IAM-014, IAM-016, IAM-018, ADMAPI-013, ADMAPI-014, ADMAPI-015, ADMAPI-016, ADMAPI-017, ADMAPI-018

**Group record:**

- Group names match `^[a-z0-9][a-z0-9._-]{0,127}$`.
- Groups have a description, an `autoJoin` flag (R-AUTH-019) and an origin: `admin`, `oidc` or
  `scim`.
- Groups are flat; nesting is not supported.

**Members.** Members are users and service accounts. Member lists write service accounts as
`sa:<name>`. The built-in principals `anonymous` and `authenticated` (R-AUTH-037) cannot be
members.

**Memberships carry a source:**

| Source | Written by |
|---|---|
| `manual` | Administrators |
| `auto` | Auto-join (R-AUTH-019) |
| `oidc:<providerId>` | Group sync (R-AUTH-008) |
| `scim` | SCIM (R-AUTH-039) |

A principal is a member if any source says so. The management API changes `manual`
memberships only. Removing a member removes only its `manual` membership; the response shows
the remaining sources, so the caller sees why the member is still in the group.

**Ownership and administrator rights:**

- Groups with origin `scim` are owned by the IdP. Renaming them, deleting them or changing their
  members through the API answers 409 `managed_by_scim`.
- Administrator rights and other administration roles come from role bindings in
  [authorization.md](authorization.md) (IAM-016). The group record shows them read-only as
  `roles`.

**Effect of changes.** Every membership change records itself in the change area `auth` in its
own transaction. Tokens and sessions see the new membership on every replica within the
revocation bound of R-AUTH-023, with or without Valkey.

Data model:

```sql
CREATE TABLE user_group (
  principal_id      uuid PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
  description       text    NOT NULL DEFAULT '',
  auto_join         boolean NOT NULL DEFAULT false,
  origin            text    NOT NULL CHECK (origin IN ('admin', 'oidc', 'scim')),
  scim_external_id  text UNIQUE
);

CREATE TABLE group_membership (
  group_id   uuid        NOT NULL REFERENCES user_group (principal_id) ON DELETE CASCADE,
  member_id  uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,  -- user or service_account
  source     text        NOT NULL,       -- 'manual' | 'auto' | 'scim' | 'oidc:<providerId>'
  added_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, member_id, source)
);
CREATE INDEX group_membership_member ON group_membership (member_id);
```

API (administrator; read operations also accept `identities:read`):

| Method and path | Request | Response | Status codes |
|---|---|---|---|
| `POST /api/v1/groups` | `{"name": "team-web", "description": "…", "autoJoin": false, "members": ["alice", "sa:ci-bot"]}` | Group | 201, 400, 404 (unknown member; nothing created), 409 |
| `GET /api/v1/groups?q=&origin=&limit=&cursor=` | — | Page of groups with member counts | 200 |
| `GET /api/v1/groups/{name}` | — | Group, including `roles` (read-only) | 200, 404 |
| `PATCH /api/v1/groups/{name}` | Any of `description`, `autoJoin`, `name` (rename) | Group | 200, 400, 404, 409 |
| `DELETE /api/v1/groups/{name}` | — | — | 204, 404, 409 (`managed_by_scim`) |
| `GET /api/v1/groups/{name}/members?source=&limit=&cursor=` | — | `{"items": [{"principal": "alice", "kind": "user", "sources": ["manual", "oidc:corp"]}]}` | 200, 404 |
| `PATCH /api/v1/groups/{name}/members` | `{"add": ["bob"], "remove": ["sa:old-bot"]}` | Resulting member page | 200, 404 (unknown member; nothing applied), 409 |

Acceptance criteria:

- Removing `alice` from `team-web` through the API while she is also a member through
  `oidc:corp` answers 200. Her remaining source is `oidc:corp`, and she still gets the group's
  rights.
- A service account added as `sa:ci-bot` to `team-web` gets the group's repository grants with
  its SAT.
- `PATCH /api/v1/groups/team-web/members` with `{"add": ["authenticated"]}` answers 404 and
  changes nothing: the name is a reserved username (R-AUTH-007), so it resolves to no user, and
  the built-in principal is never a member.

### R-AUTH-019 · Auto-join groups
Priority: **P1** · Covers: IAM-015

- A group with `autoJoin: true` receives an `auto` membership for every user created afterwards:
  JIT, administrator and SCIM users alike. It is not applied retroactively, and never to
  service accounts, the break-glass account or CI identities.
- Clearing the flag does not remove existing `auto` memberships. They can be removed per member
  like manual ones (`PATCH …/members` with `"source": "auto"`).
- A group that carries any administration role binding ([authorization.md](authorization.md))
  MUST NOT be auto-join. Setting the flag on such a group, or binding such a role to an
  auto-join group, answers 409 `auto_join_admin_conflict`. Both pages enforce this.
- CI tokens never pick up auto-join groups. Their principal is a CI mapping, not a user, so the
  catalog's widening of CI tokens through default groups (TOK-015) cannot occur.
- The MVP built-in principal `authenticated` (R-AUTH-037, [authorization.md](authorization.md)
  R-AUTHZ-030) draws the same line: like auto-join groups, it covers users only and never service
  accounts or CI identities. Until auto-join groups ship, it is how an installation grants `read`
  to every signed-in user.

Acceptance criteria:

- With `autoJoin` set on `everyone`, the first login of a new user makes them a member with
  source `auto`. Users created earlier are not members.
- `PATCH /api/v1/groups/platform-admins` with `{"autoJoin": true}` on an administrator group
  answers 409.

### R-AUTH-020 · Service accounts
Priority: **MVP** · Covers: TOK-010, TOK-015, IAM-003

A service account is a non-human principal for automation (deploy jobs outside GitLab, IdP
provisioning, monitoring). It is trove's answer to the catalog's group-scoped tokens for
subjects without a user account (TOK-010): permissions come from policies bound to the service
account or to its groups, never from placeholder users.

**Properties:**

- Names match `^[a-z0-9][a-z0-9-]{1,62}$` and are shown as `sa:<name>` in member lists, audit
  events and `whoami`.
- A service account has no login, no session, no UI access and no IdP link.
- It can be a group member (R-AUTH-018) and a policy principal.
- It holds `trv_sat_` tokens, at most `max_active_per_principal`.

**Owners (P1).** Owners are users who may create, rotate and revoke the account's tokens without
being administrators. They cannot change the account's grants.

**Disable and delete.** Disabling makes all its tokens fail with `principal_disabled`. Deleting
deletes its tokens.

Data model:

```sql
CREATE TABLE service_account (
  principal_id  uuid PRIMARY KEY REFERENCES principal (id) ON DELETE CASCADE,
  description   text NOT NULL DEFAULT ''
);

CREATE TABLE service_account_owner (            -- P1
  service_account_id uuid NOT NULL REFERENCES service_account (principal_id) ON DELETE CASCADE,
  user_id            uuid NOT NULL REFERENCES app_user (principal_id) ON DELETE CASCADE,
  PRIMARY KEY (service_account_id, user_id)
);
```

API (administrator; token operations also by owners from P1):

| Method and path | Request | Status codes |
|---|---|---|
| `POST /api/v1/service-accounts` | `{"name": "ci-bot", "description": "…", "owners": ["alice"]}` | 201, 400, 409 |
| `GET /api/v1/service-accounts?q=&limit=&cursor=`, `GET /api/v1/service-accounts/{name}` | — | 200, 404 |
| `PATCH /api/v1/service-accounts/{name}` | `description`, `owners` | 200, 404 |
| `POST /api/v1/service-accounts/{name}/disable`, `…/enable` | — | 204, 404 |
| `DELETE /api/v1/service-accounts/{name}` | — | 204, 404 |
| `POST /api/v1/service-accounts/{name}/tokens` | See R-AUTH-027 | 201 |

Acceptance criteria:

- A SAT of `ci-bot`, whose only grant is deploy on `npm-local`, can `npm publish` to
  `npm-local`. `npm whoami` prints `sa:ci-bot`.
- No path creates a session for a service account. `POST /auth/local/login` with
  `{"username": "sa:ci-bot", "password": "<its SAT>"}` answers 401 `invalid_credentials`, and
  `GET /api/v1/users/me/sessions` with the SAT answers 403.

### R-AUTH-021 · Dormant-user deactivation
Priority: **P1** · Covers: trove-only

Without SCIM, deactivating a person at the IdP does not reach trove; their PATs keep working.
Dormancy bounds that window:

- The daily task `user-dormancy` (R-AUTH-043), which runs once per slot on a `worker` replica,
  disables every active user with origin `jit` or `admin` whose `last_interactive_auth_at` (or
  `created_at` if they never logged in) is older than `dormancy_period`. The disable uses reason
  `dormant` and keeps the tokens.
- Users managed by SCIM and the break-glass account are exempt.
- While a user is disabled, their tokens fail with `principal_disabled`.
- A successful OIDC login re-enables a dormant user automatically, because the IdP has just
  vouched for the account. A user disabled by an administrator stays disabled.
- `user.dormancy_warning` is emitted `dormancy_warning` before the deadline, once per user and
  dormancy period. The task sets `dormancy_warned_at` with a conditional update (null, or older
  than the user's last interactive authentication) and writes the event in the same transaction
  only when the update changed the row, so a run that is resumed on another worker or repeated
  emits it once. Webhooks can carry it
  ([operations-and-observability.md](operations-and-observability.md)), and email is P2.
- `dormancy_period: 0` switches the job off. The default is on, with 90 days (decided in
  Q-AUTH-5). It matches the default PAT lifetime (R-AUTH-026): a user who renews tokens at the
  default cadence logs in through the UI anyway, because creating a token needs a session.
- The 401 for a dormant user's token carries `principal_disabled`, and its human-readable text
  says that one sign-in at `<baseURL>/ui/` re-enables the account: the problem `detail` on
  `/api/v1/…`, and the `error` and `message` fields on npm endpoints, which npm prints
  (R-NPM-005 in [formats/npm.md](formats/npm.md)). NuGet clients report only the status code;
  their users learn the reason from the UI. A user disabled for any other reason gets the same code
  without that hint.

Configuration:

```yaml
auth:
  users:
    dormancy_period: 2160h   # default (proposed): 90 days; 0 disables (Q-AUTH-5)
    dormancy_warning: 336h   # default (proposed): 14 days
```

Acceptance criteria:

- Set `last_interactive_auth_at` 91 days back and run the job. The user's PAT gets 401
  `principal_disabled`. One OIDC login later the same PAT works again.

## Tokens

### R-AUTH-022 · Token types and format
Priority: **MVP** · Covers: TOK-002, TOK-004, TOK-006

**Token types** (INV-8):

| Type | Prefix | Owner principal | Issued by |
|---|---|---|---|
| Personal access token (PAT) | `trv_pat_` | user | the user (UI, API, npm web login, device login) |
| Service-account token (SAT) | `trv_sat_` | service account | administrators, owners (P1), `trove admin` |
| CI token (CIT) | `trv_cit_` | CI identity mapping | the CI exchange only ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)) |

**Format.** The format is exact, so independent implementations and secret scanners agree:

```text
random   = 32 bytes from crypto/rand
body     = prefix + base62(random as unsigned big-endian integer, fixed width 43)
checksum = base62(CRC-32/IEEE(body as ASCII), fixed width 6)      // Go: crc32.ChecksumIEEE
token    = body + checksum                                         // always 57 characters
```

- The alphabet is `0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`, most
  significant digit first, left-padded with `0`. 43 base62 digits hold 256 bits; 6 digits hold
  32 bits.
- The scanner pattern is `\btrv_(?:pat|sat|cit)_[0-9A-Za-z]{49}\b`.
- The checksum lets a scanner reject random look-alikes offline. It is not a security mechanism.
- Test vector, with 32 zero bytes as `random`:
  - `trv_pat_000000000000000000000000000000000000000000010kJbh`;
  - CRC-32 is `374382dd`;
  - its SHA-256 is
    `e1ba47eddf817cc30ede3aa6f55391378f03840da097aa8c387dca45fb10c703`.
- The token secret appears exactly once, in the response that creates it (create, rotate, npm or
  device login completion, `trove admin token create`). No API, log, audit event or database
  column returns it later.
- Each token has a public `hint`, the prefix plus the first 4 characters of the random part (for
  example `trv_pat_3kQ9`), so users can tell their tokens apart in lists. 24 of 256 bits are
  disclosed; the remaining entropy is ample.

**Reference tokens (TOK-004).** Every trove token is what the catalog calls a reference token:
an opaque string that is resolved through the database on every uncached request (R-AUTH-023).
There is no second, self-contained form (TOK-003 is P2). Consequences:

- The create request has no "also issue a reference token" option; the one token it returns is
  the reference.
- No payload limit applies. Scope and group memberships live in PostgreSQL, not in the string,
  so long or many group names never change the token.
- Every type is exactly 57 characters. This settles the catalog's open point between a
  64-character and a 128-character form: trove uses neither.

**Attributes.** Every token carries the attributes of TOK-002, adapted:

- ID (UUIDv7), type, owner principal, description;
- scope (R-AUTH-024);
- creation time and creator, `createdVia`;
- expiry (R-AUTH-026);
- the revocation fields;
- `rotatedFrom`;
- `lastUsedAt` and `lastUsedAddr` (R-AUTH-031).

There is no issuer or audience attribute. Tokens are valid only on the installation whose
database holds them.

Acceptance criteria:

- A unit test reproduces the test vector above.
- One million generated tokens are all 57 characters long, unique, matched by the scanner
  pattern, and pass the checksum.
- Flipping any single character of a valid token fails the checksum, and the request is
  rejected without a database query.

### R-AUTH-023 · Token storage, validation and caching
Priority: **MVP** · Covers: TOK-004, TOK-032

**Storage.** PostgreSQL stores the SHA-256 of the full 57-character string (INV-8). With 256
bits of entropy, a salt or a slow hash adds nothing, and a single indexed lookup suffices.

**Validation steps.** These run in order:

1. Syntax: the prefix is known, the length is 57, the alphabet is base62, and the checksum
   matches. Failure gives 401 with no database access.
2. The replica's token cache (below), keyed by the hash. On a miss, or while the freshness gate
   below bypasses the cache, the failed-credential throttle of R-AUTH-038 is checked first, then
   `SELECT … FROM api_token WHERE secret_sha256 = $1`, joined with the owner's status.
3. The token is rejected unless all of the following hold:
   - `revoked_at IS NULL`;
   - `expires_at IS NULL OR expires_at > now()`;
   - the owner principal has `status = 'active'`.

   The type in the prefix must equal the row's `type`.
4. A use of a revoked or expired token whose row still exists writes `token.rejected` with the
   reason, at most once per token per hour (R-AUTH-041). The hour is kept in the row, not in a
   replica: `UPDATE api_token SET rejection_logged_at = now() WHERE id = $1 AND
   (rejection_logged_at IS NULL OR rejection_logged_at < now() - interval '1 hour')`, and the event
   is written in the same transaction only when the update changed the row. The cluster as a whole
   therefore writes one event per token and hour, whatever the number of replicas. The statement
   runs only when the row read in step 2 shows `rejection_logged_at` null or older than one hour,
   so a client that keeps presenting a revoked token causes one write per hour, not one per
   request.

**Caches.** Each replica keeps four registered in-process caches
([architecture.md](architecture.md) R-ARCH-018):

| Cache | Key | Holds |
|---|---|---|
| `auth_tokens` | secret hash | the token row and its owner's status |
| `auth_sessions` | secret hash | the session row and its principal's status (R-AUTH-010) |
| `auth_principals` | principal ID | status and group memberships |
| `auth_settings` | — | the row of R-AUTH-040 |

- They hold positive results only. `SafetyTTL` is `cache_ttl`, which bounds staleness only if an
  invalidation were ever lost through a defect; the revocation bound below does not depend on it.
- The caches never move to Valkey, the token cache included: no entry is ever read from or written
  to Valkey, and Valkey holds no shared second level of them (Q-AUTH-9). Each replica fills its own
  caches from PostgreSQL. Valkey carries only the change messages of the area `auth` below.

**Invalidation.** Every change that ends, narrows or alters a credential, a principal or a setting
records itself in the change area `auth` ([architecture.md](architecture.md) R-ARCH-018) in its
own transaction. That covers revocation, rotation, a session's end, principal disable, enable or
delete, membership changes and settings changes.
- The payload names the token ID with the hash of the ended credential, the session ID or the
  principal ID; it never carries a live secret (R-ARCH-018 rules).
- After commit, the writing replica evicts at once, and Valkey carries the change to the other
  replicas as an early eviction. The change-log poll applies it on every replica whatever Valkey
  does. Only the poll advances the replica's applied epoch, so a lost, delayed or forged Valkey
  message can neither hide nor delay a revocation.
- A fill reads the applied epoch of `auth`, and of every other area that invalidates the cache,
  before its query, and caches the result only if the epochs are unchanged afterwards
  (R-ARCH-018). A fill that raced a revocation therefore serves its one request and is never
  cached.

**Freshness gate (Q-AUTH-10).** A replica answers from these caches only while its last completed
change-log poll started less than `auth.revocation_bound` ago
(`cluster.ChangesFreshWithin(auth.revocation_bound)`, measured on the replica's monotonic clock).
Otherwise every lookup goes to PostgreSQL as on a miss and is not cached. A lookup that
PostgreSQL does not answer within the request's retry budget
([ha-and-helm.md](ha-and-helm.md) R-HA-009) gives 503 (R-AUTH-001), never an answer from the cache.
`trove_auth_cache_bypassed_total{reason="changes_stale"}` counts these lookups.

The gate is deliberately stricter than the `invalidation` readiness check of R-ARCH-018, which
takes a replica out of the Service only once its poll is older than `cluster.epoch_max_staleness`
(at least the PostgreSQL readiness window of about 15 s, [ha-and-helm.md](ha-and-helm.md)
R-HA-008). The caches are bypassed from `auth.revocation_bound` on, so a replica that is still in
the Service never answers from a cache older than the bound (Q-AUTH-10).

While the gate bypasses the caches, concurrent lookups of one credential on one replica share one
query, provided that query started less than `auth.revocation_bound` before the joining request
arrived; the shared result is not cached either. The query then started after every change older
than the bound, so sharing keeps the revocation bound. While lookups finish within the bound, a
replica therefore has one lookup per credential in flight, and a gate that trips on every replica
during a slow PostgreSQL does not multiply the database's load by the request rate.

**Revocation latency.** A revoked token, an ended session or a disabled principal MUST be rejected
by every replica within `auth.revocation_bound` (2 s) after the change committed. The bound holds
whatever Valkey does: available, unavailable, failed over, emptied, partitioned from one replica
or carrying forged messages. It also holds for a replica cut off from PostgreSQL, which answers
503 instead. While Valkey works, a change typically applies within 100 ms
([architecture.md](architecture.md) R-ARCH-027, P-13). A poll reads every change committed before
it started ([READ COMMITTED](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)),
which is why a poll younger than the bound proves it. Startup MUST fail, naming both keys, if
`auth.revocation_bound` is below 2 × `cluster.epoch_poll_interval` or above 10 s.

**Retention.** The `token-cleanup` task (R-AUTH-043) deletes token rows `retention_after_end`
after expiry or revocation. A deleted row means an unknown token. The audit trail keeps the
history.

Data model:

```sql
CREATE TABLE api_token (
  id                 uuid        PRIMARY KEY,                       -- UUIDv7
  type               text        NOT NULL CHECK (type IN ('pat', 'sat', 'cit')),
  secret_sha256      bytea       NOT NULL UNIQUE CHECK (octet_length(secret_sha256) = 32),
  hint               text        NOT NULL,
  principal_id       uuid        NOT NULL REFERENCES principal (id) ON DELETE CASCADE,
  description        text        NOT NULL CHECK (char_length(description) BETWEEN 1 AND 1024),
  scope              jsonb       NOT NULL,                          -- R-AUTH-024
  self_rotatable     boolean     NOT NULL DEFAULT false,            -- R-AUTH-030
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid        REFERENCES principal (id) ON DELETE SET NULL,
  created_via        text        NOT NULL CHECK (created_via IN ('ui', 'api', 'npm-web-login', 'device-login',
                                                                'admin-cli', 'rotation', 'ci-exchange')),
  expires_at         timestamptz,                                    -- NULL only for an allowed non-expiring SAT
  revoked_at         timestamptz,
  revoked_by         uuid        REFERENCES principal (id) ON DELETE SET NULL,
  revocation_reason  text CHECK (revocation_reason IN ('owner', 'admin', 'rotated', 'npm-logout', 'leaked',
                                                       'principal_disabled', 'scim', 'mapping_deleted',
                                                       'parent_revoked')),
  rotated_from       uuid,                                           -- no foreign key (Q-AUTH-15)
  derived_from       uuid,                                           -- R-AUTH-027 derivation rule; no foreign key (Q-AUTH-15)
  last_used_at       timestamptz,
  last_used_addr     inet,
  rejection_logged_at timestamptz,                                   -- step 4: token.rejected once per hour, cluster-wide
  expiry_notified_at timestamptz,
  ci_context         jsonb,                                          -- owned by gitlab-ci-oidc.md
  CHECK (type = 'sat' OR expires_at IS NOT NULL)
);
CREATE INDEX api_token_principal_active ON api_token (principal_id) WHERE revoked_at IS NULL;
CREATE INDEX api_token_expiry_active    ON api_token (expires_at)   WHERE revoked_at IS NULL;
```

Configuration:

```yaml
auth:
  revocation_bound: 2s            # default (proposed); at least 2 × cluster.epoch_poll_interval, at most 10s (Q-AUTH-10)
  tokens:
    cache_ttl: 15s                # default (proposed); upper bound 60s; SafetyTTL of the four auth caches
    retention_after_end: 2160h    # default (proposed): 90 days
```

Acceptance criteria:

- Revoke a token through replica A while replica B serves a request loop with it. B rejects the
  token within 2 s; with Valkey available, within 200 ms at p99 over 100 trials.
- B rejects the token within 2 s in each of these cases: Valkey stopped; B's Pub/Sub connection
  killed (`CLIENT KILL TYPE pubsub`); every message on the change channel dropped and forged
  messages injected (the fault-injection build of R-ARCH-018); a Sentinel failover during the
  revocation.
- B caches the token, then a NetworkPolicy denies B's egress to PostgreSQL, and the token is
  revoked through A. From 2 s after the policy applied, B answers requests with the token with 503
  and never with 200. After the policy is removed, B answers 401.
- `auth.revocation_bound: 1s` with `cluster.epoch_poll_interval: 1s` makes startup fail and names
  both keys.
- A database dump contains no token value. `SELECT count(*) FROM api_token WHERE
  encode(secret_sha256, 'hex') = '<sha256 of a live token>'` returns 1.

### R-AUTH-024 · Token scopes
Priority: **MVP** · Covers: TOK-005, TOK-007, TOK-008, TOK-009, TOK-012, ADMAPI-040

**Effective permission.** A token's scope narrows its owner's permissions. It never widens them.
Per INV-9:

```text
effective = owner's grants at use time (own + groups) ∩ token scope
```

Evaluation belongs to [authorization.md](authorization.md). This page defines the scope
structure, its validation and who may request what.

**Scope structure:**

```json
{
  "inherit": false,
  "admin": false,
  "repositories": [
    {
      "repositories": ["npm-local", "npm-team-*"],
      "packages": {"include": ["@acme/*"]},
      "actions": ["read", "deploy"]
    }
  ],
  "system": ["identities:read"],
  "builds": [
    {
      "builds": {"include": ["acme/**"], "exclude": []},
      "actions": ["read"]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `inherit` | `true` means all repository grants of the owner and, from P1, all build grants, evaluated at use time. This is the catalog's user-permissions scope (TOK-008) and gives identity tokens (TOK-005). `repositories` and `builds` must then be empty. |
| `admin` | `true` lets the token exercise system administration if, and only if, the owner holds the administrator role at use time (TOK-009). An administrator's token without `admin: true` acts as a non-administrator, so administrator power is always an explicit choice. |
| `repositories` | Restricted grants with the same resource selector as a policy grant: repository keys or patterns, package-name and path include/exclude patterns (`packages`, `paths`; [authorization.md](authorization.md) R-AUTHZ-005), actions from `read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage` (INV-9). Each element is `{"repositories": [...], "packages": {"include": [...], "exclude": [...]}, "paths": {"include": [...], "exclude": [...]}, "actions": [...]}`; `packages` and `paths` are optional. Pattern syntax and matching are defined by [authorization.md](authorization.md). |
| `system` | Items from the closed list of R-AUTH-025 (P1). |
| `endpoints` | The endpoints the token is valid on ([endpoints.md](endpoints.md) R-EP-011): at most 16 configured endpoint names; absent or empty means unbound. A binding, not a resource type: it grants nothing and only limits where the token is accepted (R-EP-010). An unknown name answers 400 `invalid_scope`. |
| `builds` | P1. Restricted build grants: each element is `{"builds": {"include": [...], "exclude": [...]}, "actions": [...]}`, with build-name globs (syntax of the `packages` dimension) and actions from `read`, `annotate`, `deploy`, `delete`, `manage` ([authorization.md](authorization.md) R-AUTHZ-027). `overwrite` does not apply to build records and answers 400 `invalid_scope`. `inherit: true` also admits build rights from P1; a restricted scope without a `builds` element admits no build action. |

**Resource types.** A scope can name only this closed list of resource types: `repositories`
(repository content, MVP), `system` (system capabilities, P1) and `builds` (build records, P1),
the resource types the grant model has ([authorization.md](authorization.md) R-AUTHZ-013,
Q-AUTHZ-10). `inherit` and `admin` are switches and `endpoints` is a binding, not resource types. There is no project resource
type before project roles exist (TOK-011, TOK-013, P2), and no per-repository configuration type:
repository configuration rights are system capabilities and apply instance-wide.

**Validation and defaults:**

- At most 50 grants (elements of `repositories` and `builds` together), and at most 16 KiB of
  serialized JSON. Unknown actions or fields answer 400 `invalid_scope`. This includes `include`
  or `exclude` at the top level of a `repositories` element: name and path patterns belong in
  `packages` and `paths` ([authorization.md](authorization.md) Q-AUTHZ-9).
- Scopes are immutable after creation. Rotation keeps the scope; changing it means a new token.
- The default for PATs and SATs is `{"inherit": true}`.
- A CIT uses the same structure. The identity mappings of [gitlab-ci-oidc.md](gitlab-ci-oidc.md)
  decide what its owner holds; the stored scope only narrows that, as for every token:
  - without `scope` in the exchange request, the stored scope is `{"inherit": true}`;
  - with `scope`, it is `{"inherit": false, "repositories": [...]}`, translated item by item from
    that OAuth scope string: each item `<action>:<selector>` becomes one grant
    `{"repositories": [<selector>], "actions": [<action>]}`, and an item without selector stands
    for `@any` ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-007, Q-CI-2). For example,
    `read:npm-all deploy:npm-internal` is stored as
    `{"inherit": false, "repositories": [{"repositories": ["npm-all"], "actions": ["read"]},
    {"repositories": ["npm-internal"], "actions": ["deploy"]}]}`.
  - The package-bound token of npm trusted publishing (P1, [formats/npm.md](formats/npm.md)
    R-NPM-022) is the one exception: its scope is a single grant whose `packages.include` pattern
    is the package name, with the actions `read` and `deploy`.

**Deliberate departure.** The catalog's resource scope (TOK-012) grants rights independently of
any policy. trove does not allow that. A scope that names actions the owner does not hold
simply yields nothing for them, because a token can never be a back door around policies.

Acceptance criteria:

- `alice` may read and deploy `npm-local`. Her token with scope
  `{"repositories": [{"repositories": ["npm-local"], "actions": ["read"]}]}` can install from
  `npm-local`, and `npm publish` with it gets 403.
- An administrator's PAT created with the default scope gets 403 on
  `POST /api/v1/repositories`. One created with `{"inherit": true, "admin": true}` succeeds.
- `alice`'s group loses deploy on `npm-local`. Within the bound of
  [authorization.md](authorization.md) R-AUTHZ-028 her `inherit` token can no longer publish.
- Creating a token whose scope element carries `"include": ["@acme/*"]` at the top level answers
  400 `invalid_scope`. The same pattern as `"packages": {"include": ["@acme/*"]}` is accepted.
- (P1) `alice` may deploy build records named `acme/**`. Her token with scope
  `{"repositories": [{"repositories": ["npm-local"], "actions": ["read"]}]}` gets 403 on
  uploading build record `acme/web/frontend`; her `inherit` token succeeds.

### R-AUTH-025 · System scope items
Priority: **P1** · Covers: TOK-014, ADMAPI-041

`system` restricts a token to named administration capabilities, without the blanket
`admin: true`.

**Effectiveness.** An item is effective only if the owner holds a role that grants that
capability ([authorization.md](authorization.md): the administrator role, or a narrower role if
that page defines one). `admin: true` implies every item.

**Item list (v1):** the items are exactly the capability names of the system-capability table in
[authorization.md](authorization.md) R-AUTHZ-010 (for example `repositories:write-config`,
`ci:write`, `webhooks:read`); that table is authoritative, and a capability added there is a valid
scope item without an edit here. The table below explains the read-oriented items most often
used for monitoring and audit tokens:

| Item | Operations it unlocks |
|---|---|
| `identities:read` | List and read users, groups, service accounts; SCIM reads |
| `identities:write` | SCIM create, update, delete of users and groups (R-AUTH-039) |
| `tokens:read` | List and read all tokens (R-AUTH-028) |
| `tokens:revoke` | Revoke any token (R-AUTH-029) |
| `policies:read` | Read permission policies ([authorization.md](authorization.md)) |
| `settings:read` | `GET /api/v1/settings/auth` and other settings reads |
| `audit:read` | Read audit events ([operations-and-observability.md](operations-and-observability.md)) |
| `repositories:read-config` | Read repository configurations ([repositories-and-proxy.md](repositories-and-proxy.md)) |

**Declaring items.** Every administration operation declares the item that admits it, in the
OpenAPI document as `x-trove-system-scope`. Operations without a declared item need
`admin: true`. Pages that add administrative operations extend this list through review of this
page.

Acceptance criteria:

- A SAT of an administrator service account with `{"system": ["identities:read"]}` can call
  `GET /api/v1/users`. `POST /api/v1/groups` with the same SAT gets 403.

### R-AUTH-026 · Token lifetime policy
Priority: **MVP** · Covers: TOK-017, TOK-018, TOK-019, IAM-038, ADMAPI-042

**Lifetime table:**

| Type | Default TTL | Maximum TTL | Non-expiring |
|---|---|---|---|
| PAT | 2160 h (90 days) | 8760 h (365 days) | never |
| SAT | 2160 h | 8760 h | only if `sat.allow_non_expiring` is true (default false), and only by an administrator |
| CIT | set by [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | 6 h (hard ceiling) | never |

**Requests (TOK-017):**

- A request gives `ttlSeconds` or `expiresAt` (RFC 3339), or neither, which applies the default
  of its creation path (table below). Giving both answers 400 `invalid_ttl`.
- `ttlSeconds` is an integer. `0` requests a non-expiring token (next paragraph); zero is a valid
  value, not an invalid one. Every other value must be at least 60. A negative value, a value
  from 1 to 59, a non-integer, and an `expiresAt` less than 60 s in the future answer 400
  `invalid_ttl`.
- A lifetime above the maximum answers 400 `ttl_exceeds_maximum`. trove does not silently
  clamp it.

**Non-expiring tokens (TOK-017).** Only a SAT can be non-expiring, and all three conditions must
hold:

- `sat.allow_non_expiring` is true. Otherwise `ttlSeconds: 0` answers 400
  `non_expiring_not_allowed`.
- The caller is an administrator: a session with recent authentication, a token with
  `admin: true`, or `trove admin token create --ttl 0`. Service-account owners (P1) get 403
  `non_expiring_restricted`.
- The request does not run under the derivation rule (R-AUTH-027). A token derived from a
  non-expiring SAT gets a finite lifetime, at most the SAT maximum.

PATs and CITs are never non-expiring; `ttlSeconds: 0` for them answers 400
`non_expiring_not_allowed`.

**Default per creation path (TOK-018).** The default applies to every caller, administrators
included. Every default is a finite duration; there is no "no default forced" setting.

| Creation path | Default lifetime |
|---|---|
| `POST /api/v1/tokens`, UI token dialog | `pat.default_ttl` (its runtime value, R-AUTH-040); the UI pre-fills it |
| `POST /api/v1/service-accounts/{name}/tokens` | `sat.default_ttl` (its runtime value) |
| Token-authenticated create under the derivation rule (R-AUTH-027) | The path's default, capped at the presenting token's expiry |
| npm web login (R-AUTH-034), device login (R-AUTH-036) | `cli_login.token_ttl`, capped by the PAT maximum; the approver may shorten it |
| `trove admin token create` (R-AUTH-015) | 15 min |
| Rotation (R-AUTH-030) | The original lifetime, capped by the current maximum |
| CI exchange | Set by [gitlab-ci-oidc.md](gitlab-ci-oidc.md), at most `cit.max_ttl` |

**Who the caps bind (TOK-019).** The maximum binds every creator: users, service-account owners
(P1), administrators and `trove admin`. There is no separate cap for non-administrators or
project administrators, and no variant that caps at the default. This departs from the catalog,
where administrators are uncapped, so that the security setting means what it says. The only
way past the maximum is the non-expiring SAT above, which is off by default and reserved to
administrators.

**YAML ceilings and runtime values.** The YAML values are ceilings. Administrators may lower the
default and the maximum at runtime (R-AUTH-040), but never raise them above the YAML values.
Lowering the maximum does not shorten existing tokens. `GET /api/v1/tokens?expiresAfter=` finds
them for revocation. Startup MUST fail if any `default_ttl` or `max_ttl` is zero or negative
(there is no "0 means unlimited"), if a `default_ttl` exceeds its `max_ttl`, or if
`cit.max_ttl` exceeds 6 h.

Configuration:

```yaml
auth:
  tokens:
    pat:
      default_ttl: 2160h      # default (proposed)
      max_ttl: 8760h          # default (proposed); ceiling for the runtime setting
    sat:
      default_ttl: 2160h      # default (proposed)
      max_ttl: 8760h          # default (proposed)
      allow_non_expiring: false  # default (proposed)
    cit:
      max_ttl: 6h             # default (proposed); hard ceiling, gitlab-ci-oidc.md sets the issued TTL
    max_active_per_principal:
      pat: 50                 # default (proposed)
      sat: 20                 # default (proposed)
```

Acceptance criteria:

- `POST /api/v1/tokens` without a lifetime returns `expiresAt` 90 days ahead.
- `{"ttlSeconds": 40000000}` gets 400 `ttl_exceeds_maximum`, also for an administrator.
- `POST /api/v1/tokens` with `{"ttlSeconds": 0}` gets 400 `non_expiring_not_allowed`;
  `{"ttlSeconds": -5}` and `{"ttlSeconds": 30}` get 400 `invalid_ttl`.
- With `sat.allow_non_expiring: true`, an administrator creates a SAT with `ttlSeconds: 0`; its
  `expiresAt` is null. A token derived from that SAT without a lifetime expires after the SAT
  default.
- An administrator's `POST /api/v1/tokens` without a lifetime gets the same 90-day expiry as any
  user's.
- After the runtime maximum is lowered to 30 days, a new PAT with 60 days is refused and
  existing 90-day PATs are unchanged.

### R-AUTH-027 · Token creation
Priority: **MVP** · Covers: TOK-025, TOK-006, AUTHN-040, ADMAPI-039, IAM-037

**Endpoints:**

| Method and path | Creates | Caller |
|---|---|---|
| `POST /api/v1/tokens` | A PAT for the caller | user with a session (recent authentication, R-AUTH-011), or a PAT under the derivation rule |
| `POST /api/v1/service-accounts/{name}/tokens` | A SAT | administrator (session with recent authentication, or token with `admin: true`); owners from P1; a SAT of that account under the derivation rule |

**Derivation rule** (IAM-037: token creation by non-interactive callers). A token-authenticated
create call must satisfy all of the following:

- The new token has the presenting token's type and owner.
- Its expiry is no later than the presenting token's expiry, and it is never non-expiring, even
  when the presenting SAT is (R-AUTH-026).
- Its scope is no broader than the presenting token's scope. Each new grant (an element of
  `repositories` or, from P1, `builds`) must equal a presenting grant of the same part with a
  subset of its actions, and `inherit`, `admin` and `system` items may only be kept or dropped.
  When the presenting token is bound to endpoints, the new token must be bound to a non-empty
  subset of them ([endpoints.md](endpoints.md) R-EP-011).

This holds for `Bearer` and for Basic with a token as password. A token can never mint a
credential that outlives or outranks itself. The new token records the presenting token in
`derived_from`, and revoking the presenting token revokes it too (R-AUTH-029). Otherwise a thief
could mint a child from a stolen token before the leak is noticed and keep it after the stolen
token is revoked.

**Refusals.** The following answer 403 with the code shown:

| Caller or request | Code |
|---|---|
| A CIT | `ci_token_restricted` |
| The break-glass account | `break_glass_restricted` |
| The anonymous principal | `credentials_required` |
| A PAT for a different user: an `owner` field naming anyone but the caller. Administrators cannot either, because there is no impersonation (Q-AUTH-7); automation uses service accounts. No other path creates or rotates a PAT for another user (R-AUTH-030). | `forbidden` |

**Request:**

```json
{
  "description": "laptop npm",
  "ttlSeconds": 2592000,
  "scope": {"inherit": true},
  "selfRotatable": false
}
```

**Response 201** (the secret appears only here):

```json
{
  "id": "0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e11",
  "type": "pat",
  "token": "trv_pat_3kQ9…",
  "hint": "trv_pat_3kQ9",
  "owner": {"kind": "user", "name": "alice"},
  "description": "laptop npm",
  "scope": {"inherit": true, "admin": false, "repositories": [], "system": []},
  "createdAt": "2026-10-28T10:00:00Z",
  "expiresAt": "2026-11-27T10:00:00Z",
  "selfRotatable": false
}
```

**Errors:**

| Status | Code or cause |
|---|---|
| 400 | `invalid_scope`, `invalid_ttl`, `ttl_exceeds_maximum`, `non_expiring_not_allowed` (R-AUTH-026), `description_required` |
| 401 | Not authenticated, or `reauthentication_required` |
| 403 | The codes above, `derivation_exceeds_parent` (derivation rule violated), `non_expiring_restricted` (R-AUTH-026), and `pat_not_allowed` (R-AUTH-017) |
| 409 | `token_limit_reached` (R-AUTH-026) |

The creating transaction locks the owner's `principal` row (`SELECT … FOR UPDATE`) before it
counts the owner's active tokens against `max_active_per_principal` (R-AUTH-026), so concurrent
creates on different replicas cannot exceed the limit.

Each success writes `token.created` with ID, type, owner, scope, expiry and `createdVia`, never
the secret.

Acceptance criteria:

- `curl -s -X POST -H "Authorization: Bearer $PAT" -d '{"description":"ci","ttlSeconds":3600}' https://trove.test/api/v1/tokens`
  returns 201 when `$PAT` expires later than 1 h. With `ttlSeconds` beyond `$PAT`'s expiry it
  returns 403 `derivation_exceeds_parent`.
- `curl -u x:$PAT -X POST … /api/v1/tokens` behaves like the Bearer call, so Basic with a token
  counts as token authentication.
- A CIT gets 403 `ci_token_restricted`.
- An administrator's session with recent authentication posts `{"owner": "bob", "description":
  "x"}` to `POST /api/v1/tokens` and gets 403 `forbidden`; no token row is written.

### R-AUTH-028 · Listing and inspecting tokens
Priority: **MVP** · Covers: TOK-026, TOK-027, TOK-028, ADMAPI-043, ADMAPI-044

**List:** `GET /api/v1/tokens`, with these filters:

| Filter | Values or meaning |
|---|---|
| `owner` | `me`, a username, or `sa:<name>` |
| `type` | `pat`, `sat` or `cit` |
| `state` | `active` (default), `expired`, `revoked` or `all` |
| `expiresBefore`, `expiresAfter` | Expiry bounds |
| `lastUsedBefore` | Find stale tokens |
| `limit`, `cursor` | Paging per [api-and-ui.md](api-and-ui.md) |

**Visibility:**

- Non-administrators see only their own tokens. Their `owner` is forced to `me`; from P1, owned
  service accounts are added.
- Administrators, and tokens with `tokens:read`, see all tokens.

**Inspect:**

- `GET /api/v1/tokens/{id}` returns one token.
- `GET /api/v1/tokens/current` returns the token that authenticates the request (404
  `not_a_token` for sessions).
- `GET /api/v1/auth/whoami` returns `{"principal": {"kind", "name", "id"}, "credential":
  {"type", "tokenId", "scope", "expiresAt"}}` for any credential.

**Record fields:** `id`, `type`, `hint`, `owner`, `description`, `scope`, `createdAt`,
`createdBy`, `createdVia`, `expiresAt`, `lastUsedAt`, `lastUsedAddr`, `state`, `revokedAt`,
`revokedBy`, `revocationReason`, `rotatedFrom`, `selfRotatable`. CITs add `ciContext` as
defined by [gitlab-ci-oidc.md](gitlab-ci-oidc.md). Neither the secret nor its hash is ever
returned.

**Not visible.** A token the caller may not see answers 404, not 403, so that IDs cannot be
enumerated.

Acceptance criteria:

- `alice` listing tokens sees only her own. `GET /api/v1/tokens/{id-of-bob's-token}` with her
  PAT answers 404.
- `curl -H "Authorization: Bearer $PAT" https://trove.test/api/v1/tokens/current` returns that
  token's `expiresAt` and scope.

### R-AUTH-029 · Token revocation
Priority: **MVP** · Covers: TOK-030, TOK-031, TOK-032, ADMAPI-045, ADMAPI-046

**Endpoints:**

| Method and path | Effect | Caller | Status codes |
|---|---|---|---|
| `DELETE /api/v1/tokens/{id}` | Revoke one token | owner, owner of the service account (P1), administrator, `tokens:revoke` | 204, 404 |
| `DELETE /api/v1/tokens/current` | Revoke the presenting token, reason `owner` | any token: PAT, SAT or CIT | 204 |
| `DELETE /api/v1/users/me/tokens` | Revoke all own tokens | user | 204 |
| `DELETE /api/v1/users/{username}/tokens`, `DELETE /api/v1/service-accounts/{name}/tokens` | Revoke all tokens of a principal | administrator | 204, 404 |
| `POST /api/v1/tokens/revoke` | Revoke by value: `{"token": "trv_…"}` | anyone; no authentication needed | 202; 200 with `{"id", "owner"}` for administrators; 400 if the syntax check fails |

**Revocation by value.** The endpoint is open to unauthenticated callers (decided in Q-AUTH-6).
Possession of a live token already allows revoking it (`DELETE /api/v1/tokens/current`). The open
endpoint extends that to finders who cannot or should not use the token, including a
secret-scanning service. Through the rotation cascade below, it also lets the holder of a stale,
already rotated value revoke the live successor; that is deliberate. Its rules:

- The response does not reveal whether the token existed, except to administrators.
- It is rate-limited per R-AUTH-038.
- The reason is recorded as `leaked`, together with the caller's address.

**Common rules:**

- Revocation is in force on every replica within the revocation bound of R-AUTH-023
  (`auth.revocation_bound`), with or without Valkey. The revoking transaction records the change
  in the change area `auth`, the cascade below included.
- **Cascade.** Revoking a token MUST revoke, in the same transaction, every unrevoked token whose
  `derived_from` chain leads to it, with reason `parent_revoked`. A revocation with reason
  `leaked` (revocation by value) additionally follows `rotated_from` forward and revokes the
  rotation successors and their derived tokens, because a thief may have rotated the stolen token
  first.
- The forward cascade of a by-value revocation also runs when the presented token is already
  revoked or expired, as long as its row still exists (retention, R-AUTH-023). The presented
  token's own revocation fields stay unchanged. trove cannot tell a holder's rotation from a
  thief's, so a leaked predecessor always ends the chain; the legitimate holder creates a new
  token.
- Revoking an already revoked or expired token answers 204, so the operation is idempotent.
- Every revocation writes `token.revoked` with reason and actor.
- **Self-revocation.** `DELETE /api/v1/tokens/current` revokes the presenting token of every
  type, CI tokens included, with revocation reason `owner`; `revoked_by` is the token's owner
  principal. [gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-028 asserts this reason for the
  self-revocation that ends each job's `script` (Q-CI-7).
- CITs are revoked by administrators, by themselves through `current` (above), or by disabling or
  deleting their identity mapping or CI issuer ([gitlab-ci-oidc.md](gitlab-ci-oidc.md) R-CI-017).
- Creating a token never revokes another one.
- Token rows are removed only by the retention job (R-AUTH-023), or by deleting the owner.

Acceptance criteria:

- `curl -X POST -d '{"token":"'$PAT'"}' https://trove.test/api/v1/tokens/revoke` without
  credentials answers 202. `npm whoami` with `$PAT` gets 401 within 2 s. The audit trail shows
  `token.revoked` with reason `leaked`.
- `DELETE /api/v1/tokens/current` with a SAT makes that SAT unusable. The other SATs of the
  account keep working.
- `DELETE /api/v1/tokens/current` with a CIT answers 204, the CIT gets 401 on its next request,
  and the audit trail shows `token.revoked` with reason `owner`.
- A PAT derives a child PAT through `POST /api/v1/tokens`. Revoking the parent by value makes the
  child fail with 401 within the revocation bound, and the audit trail shows `token.revoked` with
  reason `parent_revoked` for the child.
- A SAT is rotated with `overlapSeconds: 0`, so the predecessor is already revoked with reason
  `rotated`. Posting the predecessor's value to `POST /api/v1/tokens/revoke` answers 202, and the
  successor fails with 401 within the revocation bound.

### R-AUTH-030 · Token rotation
Priority: **P1** · Covers: TOK-024

Rotation replaces a token's secret while keeping its description, scope and owner. It is how
holders renew long-lived credentials without the issuer. trove issues no refresh tokens and has
no refresh grant (decided in Q-AUTH-3); no endpoint returns a `refresh_token`.

Rotation is renewal, not a response to a leak. A token that may have leaked is revoked
(R-AUTH-029) and replaced by a newly created token. Rotation would keep the leaked value valid
during the overlap, and a revocation by value of the leaked value also ends its successors
(R-AUTH-029).

**Endpoints.** Rotation hands out a new secret, so its callers follow the creation rules of
R-AUTH-027, not the revocation rules of R-AUTH-029:

| Endpoint | Token | Callers |
|---|---|---|
| `POST /api/v1/tokens/{id}/rotate` | PAT | Only its owner: through a session with recent authentication (R-AUTH-011), or through another token of the owner under the derivation rule (R-AUTH-027), so the rotated token's scope and new expiry must not exceed the presenting token's (403 `derivation_exceeds_parent`) |
| `POST /api/v1/tokens/{id}/rotate` | SAT | Administrators (session with recent authentication, or token with `admin: true`), owners of the service account (P1), or a SAT of that account under the derivation rule |
| `POST /api/v1/tokens/current/rotate` | PAT or SAT | The token itself, only if it was created with `selfRotatable: true`. This is the one case in which a token extends its own lifetime; the holder opted in at creation. |

Nobody else may rotate. Administrators and holders of the system item `tokens:revoke` can revoke
another user's PAT but not rotate it, because the new secret would act as that user (Q-AUTH-7);
they get 403 `forbidden`. The break-glass account gets 403 `break_glass_restricted`.

The request body is `{"overlapSeconds": 0}`, with a maximum of 86400.

**The new token:**

- has the original TTL (`expires_at − created_at`), capped by the current maximum
  (R-AUTH-026);
- records `rotatedFrom` and `createdVia: rotation`;
- comes back in the R-AUTH-027 response shape.

**The predecessor** gets `expires_at = min(expires_at, now + overlap)`. With overlap 0 it is
revoked with reason `rotated`.

**Restrictions:**

- Expired or revoked tokens cannot be rotated (404). There is no grace period after expiry.
- CITs cannot be rotated.
- Every rotation writes `token.rotated`. A self-rotation also flags the owner's token list, so a
  thief perpetuating a stolen token through self-rotation is visible: the legitimate holder's
  copy stops working.

Acceptance criteria:

- Rotate a SAT with `overlapSeconds: 3600`. Both secrets work, and after one hour only the new
  one does.
- `POST /api/v1/tokens/current/rotate` with a token created without `selfRotatable` gets 403.
- An administrator's session with recent authentication gets 403 `forbidden` from
  `POST /api/v1/tokens/{id of alice's PAT}/rotate`; `DELETE /api/v1/tokens/{same id}` succeeds.
  A token whose scope is `{"system": ["tokens:revoke"]}` behaves the same way.
- `alice`'s PAT with scope `{"repositories": [{"repositories": ["npm-local"], "actions":
  ["read"]}]}` gets 403 `derivation_exceeds_parent` when it rotates her PAT with
  `{"inherit": true}`.

### R-AUTH-031 · Last-used tracking
Priority: **P1** · Covers: TOK-029

**What is recorded.** Tracking is always on and has no switch. Every successful authentication
by a token records `last_used_at` and `last_used_addr` (the client address as determined in
[architecture.md](architecture.md), which owns the trusted-proxy handling). Bearer, Basic and
NuGet API-key presentations count alike.

**Write strategy.** Writes are coarsened and batched:

- each replica aggregates uses in memory. Requests write this buffer and never read it, so it is
  per-replica state that no answer depends on (R-AUTH-042);
- it flushes every 60 s, and at shutdown before the pools close
  ([architecture.md](architecture.md) R-ARCH-024), with one `UPDATE … WHERE last_used_at IS NULL
  OR last_used_at < $ts − granularity`. The condition keeps each token's row at one change per
  `granularity`, whatever the number of replicas that saw it;
- updates are best effort. A failed flush is dropped and counted in a metric, and never fails a
  request. A replica killed without a drain loses at most one interval of uses.

**Use.** `lastUsedBefore` in R-AUTH-028 finds stale tokens. A token that was never used has
`lastUsedAt: null`.

Configuration:

```yaml
auth:
  tokens:
    last_used_granularity: 5m # default (proposed)
```

Acceptance criteria:

- After `npm install` with a PAT, the token's `lastUsedAt` is within the last 6 minutes. After
  100 further requests within one minute, spread over three `web` replicas, the token's row has
  changed at most once.
- A `web` pod deleted gracefully 10 s after the only use of a token writes that use before it
  exits.

### R-AUTH-032 · Token expiry notifications
Priority: **MVP** · Covers: IAM-038

**Daily task.** The task `token-expiry-notice` (R-AUTH-043), which runs once per slot on a
`worker` replica, finds active PATs and SATs whose `expires_at` falls within `expiry_notice` and
whose `expiry_notified_at` is null. For each one, in one transaction, it:

- sets `expiry_notified_at` with `UPDATE … WHERE id = $1 AND expiry_notified_at IS NULL`;
- writes the event `token.expiring` (owner, token ID, hint, description, `expiresAt`), only when
  that update changed the row, so two runs that overlap (a job resumed on another worker while
  its first claimant still runs) write one event.

The event goes to the audit trail and to webhook subscribers
([operations-and-observability.md](operations-and-observability.md)). Rotation creates a new
row, which starts unnotified.

**Other surfaces:**

- The UI shows the owner a banner listing their tokens that expire within the notice period.
  For service accounts, the banner goes to administrators and owners.
- `GET /api/v1/tokens?expiresBefore=` gives the same list.
- The gauge `trove_tokens_expiring{type}` counts tokens inside the notice period.
- Email notification (TOK-023) is deferred to P2; it needs the mail server of the operations
  page.

Configuration:

```yaml
auth:
  tokens:
    expiry_notice: 168h  # default (proposed): 7 days; runtime-adjustable (R-AUTH-040)
```

Acceptance criteria:

- A PAT expiring in 3 days produces exactly one `token.expiring` event across two task runs on
  two `worker` replicas, also when the first run's pod is deleted mid-run and the second worker
  resumes it.

## Client authentication

### R-AUTH-033 · Client authentication matrix
Priority: **MVP** · Covers: TOK-033, TOK-034, PKG-NPM-005, PKG-NPM-006, PKG-NPM-011, PKG-NUGET-017, PKG-NUGET-018, IAM-036, ADMAPI-055, FMT-013

Every client presents a trove token (R-AUTH-022). The table lists the supported forms; the
endpoint routes themselves belong to the format pages.

| Client | Configuration (example) | Wire form | Accepted |
|---|---|---|---|
| npm, pnpm, Yarn 1 | `//trove.example.com/api/npm/npm-all/:_authToken=${TROVE_TOKEN}` in `.npmrc` ([npmrc auth settings](https://docs.npmjs.com/cli/configuring-npm/npmrc#auth-related-configuration)) | `Authorization: Bearer <token>` | yes |
| npm, pnpm, Yarn 1 | `//trove.example.com/api/npm/npm-all/:_auth=<base64(any:token)>` | `Authorization: Basic …` | yes; the username is ignored |
| Yarn 2+ | `npmAuthToken: ${TROVE_TOKEN}` or `npmAuthIdent: "any:${TROVE_TOKEN}"` in `.yarnrc.yml` | Bearer or Basic | yes |
| npm interactive | `npm login --registry=https://trove.example.com/api/npm/npm-all/` | web login (R-AUTH-034) or legacy login (R-AUTH-035); stores `_authToken` | P1 |
| NuGet (dotnet, nuget.exe, Visual Studio) | `packageSourceCredentials` with `Username=<any>` and `ClearTextPassword=<token>` in `nuget.config` ([reference](https://learn.microsoft.com/en-us/nuget/reference/nuget-config-file#packagesourcecredentials)) | Basic after a 401 challenge ([authenticated feeds](https://learn.microsoft.com/en-us/nuget/consume-packages/consuming-packages-authenticated-feeds)) | yes |
| NuGet push, delete, relist | `dotnet nuget push … --api-key <token>` or `nuget setapikey <user>:<token>` | `X-NuGet-ApiKey` ([publish resource](https://learn.microsoft.com/en-us/nuget/api/package-publish-resource)) | yes; rules below |
| Generic (curl, CI scripts, other formats) | — | Bearer or Basic | yes |
| Browser UI | — | `__Host-trv_session` cookie | `/api/v1` all methods; `/files` `GET` and `HEAD`, and `PUT` and `DELETE` with CSRF (R-AUTH-012); package-protocol endpoints `GET` and `HEAD` only |

**NuGet API-key rule (PKG-NUGET-018).** The rule applies on the API-key endpoints:

- The value is examined first. If the value, or its part after the last `:`, is a well-formed
  trove token (prefix, length, checksum), that token is the credential and the
  `Authorization` header is ignored. Both forms are accepted: a bare token (what
  `dotnet nuget push --api-key` and a publish that passes a single value send) and
  `<any user>:<token>` (the `setapikey` convention). This settles the catalog's open point.
- If that token is invalid (unknown, expired, revoked, or its owner disabled), the request gets
  401. It does not fall back to the `Authorization` header (R-AUTH-001).
- Otherwise, for example the placeholder `az` that some setups pass, the header value is ignored
  and the `Authorization` header applies.
- With `auth.clients.nuget_api_key_header: false`, `X-NuGet-ApiKey` is never examined and only
  the `Authorization` header applies.

The NuGet clients send the API key and the source credentials independently, so both
conventions work. When a request carries both a token API key and Basic credentials, the API
key decides, even if the two tokens belong to different principals; the audit event names the
API key's owner. The username part of `user:token` is ignored, like the Basic username.

**Authentication styles across formats (FMT-013).** The authentication stage (R-AUTH-001) is the
same for every format, so no format parses credentials of its own. Each client style of FMT-013
maps to it as follows:

| Client style | What reaches trove | trove's rule |
|---|---|---|
| Token in the client configuration, per registry URL or per scope (npm, pnpm, Yarn) | Bearer, or Basic from a registry-keyed `_auth`, `username` plus `_password`, or `npmAuthIdent` | Accepted. npm 9 and later read only registry-keyed auth lines, so setup instructions never offer the unkeyed legacy `_auth`. A scope (`@acme:registry=…`) authenticates through the auth line of the registry URL it names. When each client attaches credentials, and the `always-auth` and `npmAlwaysAuth` settings that follow from it, are in R-NPM-004 ([formats/npm.md](formats/npm.md)). |
| Username and token per package source (NuGet) | Basic after a 401; the API key on push, delete and relist | Accepted, with the API-key rule above. Any non-empty username works, because trove ignores it. |
| `.netrc`, or credentials inside the repository URL (`https://user:token@host/…`) | Basic. The client turns them into an `Authorization` header; userinfo never appears in the request target ([RFC 9110](https://www.rfc-editor.org/rfc/rfc9110) §4.2.4) | Accepted on every package-protocol endpoint and on `/files/`. trove never reads credentials from the request path or query (R-AUTH-001). |
| A client that rejects certain usernames, for example email addresses | Basic | Any username works, because trove ignores it. |
| A client that cannot authenticate at all, or not for some operations | No credential | Served only as the anonymous principal (R-AUTH-037): both anonymous switches on, plus an anonymous `read` grant on exactly the repositories those clients use. trove has no per-repository switch that bypasses the authentication stage or the global anonymous switches. A format page that needs anonymous access for a client names the anonymous grant it needs. |
| A client that sends configured credentials only after a 401 (NuGet), on a repository that anonymous may read | No credential, so the request runs as anonymous | `requireAuthentication: true` on the repository (R-REPO-004 in [repositories-and-proxy.md](repositories-and-proxy.md), R-NUGET-012) challenges anyway, so reads are authorized and attributed per principal. |

FMT-013 is MVP for its npm and NuGet rows, which the first two rows and the last row settle. The
other rows of FMT-013 keep their format's priority and are specified on their format pages
under the rules of this table.

**`whoami` rendering.** The npm `whoami` endpoint ([formats/npm.md](formats/npm.md)) answers
`{"username": "<name>"}`:

| Principal | Rendered name |
|---|---|
| User | Username |
| Service account | `sa:<name>` |
| CI token | `ci:<CI issuer name>/<mapping name>`, the owner principal's reference (R-AUTH-016) |
| Anonymous | Gets 401 |

**Basic switch.** `auth.clients.basic.enabled` turns off Basic token presentation everywhere
(IAM-036, ADMAPI-055). This breaks NuGet restore and `_auth` users, so the default is on and
the settings response warns when it is off. Basic with a password is impossible regardless
(R-AUTH-014).

Configuration:

```yaml
auth:
  clients:
    basic:
      enabled: true           # default (proposed)
    nuget_api_key_header: true  # default (proposed)
```

Acceptance criteria:

- `npm whoami --registry=https://trove.test/api/npm/npm-all/` with `_authToken` prints
  `alice`. With `_auth=$(printf 'whatever:%s' "$PAT" | base64)` it prints `alice` as well.
- NuGet with credentials in `nuget.config`, all of which succeed:

  ```shell
  dotnet nuget add source https://trove.test/api/nuget/nuget-all/v3/index.json -n trove -u alice -p "$PAT" --store-password-in-clear-text
  dotnet restore
  dotnet nuget push Acme.Lib.1.0.0.nupkg -s trove --api-key "$PAT"
  ```

  Push with `--api-key az` and the credentials configured succeeds as well. Push with
  `--api-key "$REVOKED_PAT"` gets 401 even though valid credentials are configured.
- `curl -u ci:$SAT -T file.bin https://trove.test/files/generic-local/a/file.bin` deploys the
  file.
- With a `.netrc` entry `machine trove.test login any password <PAT>`, `curl --netrc
  https://trove.test/api/npm/npm-all/lodash` answers 200. `curl "https://any:$PAT@trove.test/files/generic-local/a/file.bin"`
  downloads the file. A request whose path or query contains the PAT but which has no
  `Authorization` header gets 401.
- `dotnet nuget push … --api-key "$PAT"` with valid source credentials of another user stores
  the package, and the deploy's audit event names the PAT's owner as actor.

### R-AUTH-034 · npm web login
Priority: **P1** · Covers: PKG-NPM-008, AUTHN-044

npm's default `npm login` opens a browser and polls for a token. The protocol is from
[npm-profile](https://github.com/npm/npm-profile) (verified against its `lib/index.js`) and
[npm login](https://docs.npmjs.com/cli/commands/npm-login). trove serves it on top of the
OIDC session.

**Flow:**

1. **Start.** `POST /api/npm/{repoKey}/-/v1/login` needs no credential and is rate-limited
   (R-AUTH-038). The repository must exist and have format npm; otherwise 404, which makes npm
   fall back to legacy login. trove creates a pending `cli_login` row (expiry
   `auth.cli_login.ttl`) and answers 200:

   ```json
   {"loginUrl": "https://trove.example.com/ui/cli-login/<publicId>",
    "doneUrl":  "https://trove.example.com/auth/cli/<pollSecret>"}
   ```

   Both URLs are absolute `https` URLs, as npm requires. `publicId` is 16 random bytes and
   `pollSecret` 32 random bytes, both base64url; only their hashes are stored. The `pollSecret`
   is known only to the requesting client.

2. **Approval.** The approval page needs a session. Without one, the UI starts the OIDC login
   with `return_to` set to the page. The page reads `GET /api/v1/auth/cli-logins/{publicId}` and
   shows:
   - the client kind and the registry URL;
   - the requesting address and user agent, and the request time;
   - the resulting token's lifetime and scope;
   - a warning to approve only a login the user just started;
   - an additional prominent warning if the browser's address differs from the requesting
     address;
   - the refused polls (address binding below): their count and the address and time of the last
     one, with the text "trove
     refused to hand the token to a poll from <address>, outside the network that started this
     login. If that was your own npm, your network uses several egress addresses: create a token
     in your profile and enter it at npm's password prompt, or ask an administrator to list the
     address range. If it was not you, deny this login." The page refreshes the record every 2 s
     while it is open, so a refusal before or after the decision appears without a reload.

   `POST …/approve`, with an optional narrower `scope` or shorter `ttlSeconds`, or `POST …/deny`
   needs CSRF and recent authentication. Each answers 204. A decision is final and is bound to
   the deciding user.

3. **Polling.** `GET /auth/cli/{pollSecret}` answers:

| State | Response |
|---|---|
| Pending | 202, body `{}`, `Retry-After: 2`. npm parses the body as JSON before checking the status, so the body must be JSON. |
| Approved | Mint the PAT (`createdVia: npm-web-login`, description `npm login from <addr> on <date>`), mark the row consumed, answer 200 `{"token": "trv_pat_…"}`. The secret is generated at this moment and is never stored. |
| Denied | 403 `{"error": "denied"}` |
| Expired, consumed or unknown | 404 `{"error": "not_found"}` |

**Polls on any replica.** Each poll reads the row from PostgreSQL; no replica keeps a login's state
(R-AUTH-042, Q-AUTH-12). An approved login is consumed with
`UPDATE cli_login SET status = 'consumed' WHERE id = $1 AND status = 'approved' RETURNING …`, in the
same transaction that inserts the PAT. Two polls of one `doneUrl` that reach two replicas at the
same moment therefore mint one token: the second finds no approved row and gets 404.

npm treats any 4xx answer, on the start call or on a poll, as "web login unsupported" and falls
back to the username and password prompt. There, a user can still type a PAT as password
(R-AUTH-035). trove documents this behavior; it cannot change it.

**Address binding (decided in Q-AUTH-8).** The `doneUrl` is a bearer URL: whoever polls it
first after the approval receives the PAT, and its path passes through every proxy log between
npm and trove. trove therefore releases the token only to a poll from the network that started
the login. The client address is the one of R-ARCH-006 in [architecture.md](architecture.md);
IPv4-mapped IPv6 addresses are unmapped before comparing. A poll *matches* `requested_addr`
when `auth.cli_login.poll_binding.mode` says so:

| Mode | A poll matches when |
|---|---|
| `network` (default) | Its address equals `requested_addr`, or both have the same address family and share the first `ipv4_prefix` bits (IPv4, default 24) or `ipv6_prefix` bits (IPv6, default 56), or both lie in the same entry of `equivalent_ranges` |
| `exact` | Its address equals `requested_addr`, or both lie in the same entry of `equivalent_ranges` |
| `off` | Always. The settings response (R-AUTH-040) warns that H-20 is then fully live. |

Rules:

- A poll that does not match MUST get 404 `{"error": "not_found"}`, whatever the row's state,
  and MUST NOT consume an approval. npm treats that 404 as "web login unsupported" and falls
  back to legacy login (R-AUTH-035), so a legitimate client whose poll leaves the network gets a
  password prompt where it can enter a PAT.
- Each refused poll increments `refused_poll_count` and records `last_refused_addr` and
  `last_refused_at` on the row, under the row's lock (`SELECT … FOR UPDATE`). The approval page
  shows it (step 2). trove writes `cli_login.poll_refused` (R-AUTH-041) in the same transaction
  when the refused address differs from the previous `last_refused_addr`, so concurrent refusals
  on several replicas write one event per new address, and no replica keeps a record of its own.
  The metric `trove_auth_cli_poll_refused_total` counts refusals, so operators see how often their
  network defeats the binding.
- `equivalent_ranges` lists CIDR blocks of the operator's egress pools whose addresses are not
  contiguous within the prefix. Startup MUST fail for an invalid entry, for an IPv4 entry wider
  than `/16`, and for an IPv6 entry wider than `/32`; switching the binding off takes `mode:
  off`. Startup MUST also fail for `ipv4_prefix` outside 16 to 32 or `ipv6_prefix` outside 32 to
  128.
- The binding is only as good as the client address. Behind an edge proxy (Ingress controller,
  Gateway, load balancer that terminates TCP), the address is the client's only when
  `server.trusted_proxies` lists that proxy (R-ARCH-006, R-HA-040). Otherwise every start and
  every poll carries a proxy address: the binding then admits every poll that passes the same
  proxy instance, attacker's included, and refuses legitimate polls that pass another instance.
  The settings response (R-AUTH-040) therefore warns when the mode is not `off` and
  `server.trusted_proxies` is empty. trove cannot detect the edge proxy itself, so this is a
  warning, not a startup error.
- The binding applies to `client_kind = npm` only. The device flow (R-AUTH-036) carries its
  device code in a POST body, not in a URL, and binds the browser through the typed user code.

trove's own logs redact the poll secret (R-AUTH-001); logs outside trove are H-20.

**Turning it off.** With `auth.cli_login.npm_web_login: false`, step 1 answers 404, so npm uses
legacy login.

Data model (shared with R-AUTH-036):

```sql
CREATE TABLE cli_login (
  id                 uuid        PRIMARY KEY,
  public_id_sha256   bytea       NOT NULL UNIQUE,
  poll_secret_sha256 bytea       NOT NULL UNIQUE,
  client_kind        text        NOT NULL CHECK (client_kind IN ('npm', 'device')),
  client_name        text,                         -- device flow: client_id given by the tool
  repository_key     text,                         -- npm: the registry the login started on
  user_code          text,                         -- device flow only
  requested_addr     inet,
  requested_ua       text,
  status             text        NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
  decided_by         uuid        REFERENCES principal (id) ON DELETE CASCADE,
  token_scope        jsonb,
  token_ttl          interval,
  token_self_rotatable boolean   NOT NULL DEFAULT false,  -- device flow: requested, approver may clear
  refused_poll_count integer     NOT NULL DEFAULT 0,  -- address binding, npm only
  last_refused_addr  inet,
  last_refused_at    timestamptz,
  last_polled_at     timestamptz,                  -- device flow: slow_down across replicas (R-AUTH-036)
  created_at         timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL
);
CREATE INDEX cli_login_expires ON cli_login (expires_at);  -- session-cleanup, pending-flow count (R-AUTH-038)
```

The table has the backup export class `transient` ([backup-and-restore.md](backup-and-restore.md)
R-BAK-004): a restore ends every command-line login in progress.

Configuration:

```yaml
auth:
  cli_login:
    ttl: 10m                 # default (proposed)
    npm_web_login: true      # default (proposed)
    token_ttl: 720h          # default (proposed): 30 days; capped by the PAT maximum
    poll_binding:            # Q-AUTH-8
      mode: network          # default (proposed): network | exact | off
      ipv4_prefix: 24        # default (proposed)
      ipv6_prefix: 56        # default (proposed)
      equivalent_ranges: []  # default (proposed); example: ["198.51.100.0/27", "203.0.113.64/28"]
```

Acceptance criteria:

- `npm login --registry=https://trove.test/api/npm/npm-all/` prints a URL. Opening it, logging
  in through the test IdP and approving lets the command finish. `~/.npmrc` then contains
  `//trove.test/api/npm/npm-all/:_authToken=trv_pat_`, and `npm whoami` prints the user.
- Polling the same `doneUrl` after success gives 404. A denied login makes npm stop polling.
- Two polls of one approved `doneUrl`, sent at the same moment to two different `web` replicas:
  exactly one gets 200 with a token, the other 404, and
  `SELECT count(*) FROM api_token WHERE created_via = 'npm-web-login'` grows by one.
- The login starts from `198.51.100.10`. After the approval, a poll of the `doneUrl` from
  `203.0.113.7` gets 404, and the next poll from `198.51.100.10` receives the token. The approval
  page shows the refused address, and one `cli_login.poll_refused` event is written. The request
  log shows the poll path as `/auth/cli/[REDACTED]`.
- With the default mode, a login started from `198.51.100.10` and polled from `198.51.100.77`
  completes. With `mode: exact` the same poll gets 404, and with
  `equivalent_ranges: ["198.51.100.0/25"]` it completes again.
- A login started over IPv6 from `2001:db8:0:1::5` completes when polled from `2001:db8:0:2::9`
  (same `/56`) and gets 404 when polled from `2001:db8:1::9`.
- `equivalent_ranges: ["10.0.0.0/8"]` makes startup fail with a message naming the entry.
- With `server.trusted_proxies: []` and the default mode, `GET /api/v1/settings/auth` carries the
  trusted-proxy warning; with the ingress controller's pod CIDR listed, it does not.
- Approving from a session older than `recent_auth_window` triggers the silent re-authentication
  first (R-AUTH-011).

### R-AUTH-035 · npm legacy login and logout
Priority: **P1** · Covers: PKG-NPM-009, PKG-NPM-010

**Legacy login.** `PUT /api/npm/{repoKey}/-/user/org.couchdb.user:{name}` carries
`{"name", "password", …}`. Users of `npm login --auth-type=legacy`, of Yarn 2+ `yarn npm login`
and of the web-login fallback type a username and, as password, an existing PAT or SAT:

| Password | Response |
|---|---|
| A valid PAT or SAT | 201 `{"ok": true, "id": "org.couchdb.user:<name>", "token": "<the same token>"}`. The client stores it as `_authToken`. No new token is minted, so legacy login cannot create credentials. `name` is used only for the audit event. |
| Anything else | 401 `{"ok": false, "error": "use an access token as password"}` |

Legacy login never answers 409 and never creates a user.

**Logout.** `DELETE /api/npm/{repoKey}/-/user/token/{token}` is what `npm logout` sends
([npm logout](https://docs.npmjs.com/cli/commands/npm-logout)):

- If the path token equals the token that authenticates the request, and `npm_logout_revokes` is
  true, the token is revoked with reason `npm-logout`.
- In every case the answer is 200 `{"ok": true}`. npm leaves the credential in `.npmrc` on any
  error, and a no-op answer reveals nothing about other tokens.
- The path contains the token, so the redaction of R-AUTH-001 applies.

Configuration:

```yaml
auth:
  clients:
    npm_legacy_login: true   # default (proposed)
    npm_logout_revokes: true # default (proposed); a shared PAT dies everywhere on one logout
```

Acceptance criteria:

- `npm login --auth-type=legacy --registry=…` with username `alice` and a PAT as password
  writes that PAT into `.npmrc`.
- `npm logout --registry=…` removes it from `.npmrc`, and the PAT gets 401 afterwards.

### R-AUTH-036 · Browser login handoff for other command-line tools
Priority: **P1** · Covers: AUTHN-044

Tools other than npm obtain a PAT through the OAuth 2.0 Device Authorization Grant
([RFC 8628](https://www.rfc-editor.org/rfc/rfc8628)). It shares `cli_login` and the approval
page with R-AUTH-034, but adds a user code that the user must type, which binds the browser to
the terminal they are looking at.

**Endpoints:**

| Method and path | Request | Response |
|---|---|---|
| `POST /api/v1/auth/device/authorize` | form fields `client_id=<tool name>` (free text, shown on the approval page), optional `scope` (JSON scope, R-AUTH-024), optional `self_rotatable=true` | 200 `{"device_code", "user_code": "WDJB-MJHT", "verification_uri": "<base>/ui/device", "verification_uri_complete": "<base>/ui/device?code=WDJB-MJHT", "expires_in": 600, "interval": 5}` |
| `POST /api/v1/auth/device/token` | `grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=…` | 200 `{"access_token": "trv_pat_…", "token_type": "Bearer", "expires_in": …}`; 400 `authorization_pending`, `slow_down`, `access_denied` or `expired_token` (§3.5) |

**Codes.** The user code has 8 characters from `BCDFGHJKLMNPQRSTVWXZ` (§6.1) and is compared
case-insensitively. The device code is the `pollSecret`.

**Wrong user codes.** §5.1 recommends rate-limiting user-code attempts. A wrong code does not name
a request, so attempts are counted per signed-in user, in the cluster-scoped limiter
`auth.device_code_failures` (R-AUTH-038). After `auth.rate_limit.device_code_failures` wrong codes
(5) within `auth.cli_login.ttl`, the page refuses further codes from that user with 429 until the
window ends, on every replica alike. While Valkey is unavailable, the limiter's per-replica share
applies ([architecture.md](architecture.md) R-ARCH-034).

**Polling pace.** `interval` is 5 s. Each poll reads and updates `last_polled_at` of its row in one
statement. A poll that arrives less than `interval` after the previous poll of the same device
code, on whichever replica, answers `slow_down`; §3.5 then makes the client add 5 s to its interval
for every later poll. An approved request is consumed with the conditional update of R-AUTH-034,
so it yields exactly one token however many replicas the polls reach.

**Approval.** The approval page requires the typed user code, a session, CSRF protection and
recent authentication.

**The issued token.** It is a PAT with `createdVia: device-login` and the lifetime
`auth.cli_login.token_ttl`, unless the approver chose a shorter one (R-AUTH-026). The token
response never contains a `refresh_token` (Q-AUTH-3). A tool renews its token in one of two
ways:

- by running the device flow again;
- by self-rotation (`POST /api/v1/tokens/current/rotate`, R-AUTH-030), if it asked for
  `self_rotatable=true`. The approval page shows that request, the approver may decline it, and
  the PAT records the outcome as `selfRotatable`.

Acceptance criteria:

- A test client using a standard RFC 8628 library (for example `golang.org/x/oauth2`
  `DeviceAuth`) obtains a PAT after the approval in the browser. The PAT then works with `curl`.
- Polling faster than `interval` gets `slow_down`. Two polls of one device code 1 s apart, sent to
  two different `web` replicas, get `authorization_pending` and then `slow_down`.
- Six wrong user codes typed by one user, each submitted to another replica, get 429 for the sixth.
- The approval polled at the same moment on two replicas yields one token, as in R-AUTH-034.
- The token response contains no `refresh_token`. A PAT from a request with
  `self_rotatable=true` that the approver kept can call `POST /api/v1/tokens/current/rotate`; one
  whose approver declined it gets 403.

## Anonymous access and abuse protection

### R-AUTH-037 · Anonymous access
Priority: **MVP** · Covers: IAM-005, AUTHN-036, PKG-NUGET-016

**The anonymous principal.** A built-in principal named `anonymous` (kind `anonymous`) always
exists. It cannot be deleted, cannot hold tokens or sessions, and cannot be a group member.

**The `authenticated` principal.** A second built-in principal, named `authenticated` (kind
`authenticated`), always exists:

- It cannot be deleted or renamed, holds no tokens or sessions, and cannot be a group member.
- It is part of the principal set of user sessions and personal access tokens only. It is never
  part of the principal set of service-account tokens, CI tokens or anonymous requests.
- The anonymous switches below do not affect it.

Its grant rules (`read` only, no roles, no ownership, not grantable by delegated managers or
project administrators) are in [authorization.md](authorization.md) R-AUTHZ-030.

**Enabling anonymous access.** Anonymous access is off by default. It takes both of these:

- the YAML ceiling `auth.anonymous.allowed` (the operator's decision; default true);
- the runtime switch `anonymousAccess` (the administrator's decision, R-AUTH-040; default
  false).

If the ceiling is false, the switch cannot be enabled and is treated as off.

**Effect when enabled.** A request with no credential at all runs as `anonymous`, with exactly
the grants that policies give that principal ([authorization.md](authorization.md)).

**Hard limits.** These hold regardless of policies:

- `anonymous` never passes unsafe methods on `/api/v1/…`;
- it never holds an administration role or system scope;
- it cannot call token, session, profile or settings endpoints (401).

**Challenges:**

- If `anonymous` lacks read access to the addressed repository, the answer is 401 with the
  challenge of R-AUTH-002, never 403 or 404. Clients then send their configured credentials.
- The same 401 is given for a repository key that does not exist, so unauthenticated callers
  cannot probe repository names.
- Inside a repository that `anonymous` may read, a missing package is 404.
- The repository field `requireAuthentication` (R-REPO-004 in
  [repositories-and-proxy.md](repositories-and-proxy.md); PKG-NUGET-016) overrides anonymous
  access for that repository: anonymous requests get the 401 challenge even where `anonymous`
  holds `read`. There is no per-repository switch in the other direction; anonymous access
  always needs both switches above (R-AUTH-033, FMT-013).

**Always unauthenticated.** These endpoints are always reachable without a credential:

- `GET /api/v1/auth/login-options`;
- `/auth/oidc/…`, `/auth/local/login`, `/auth/cli/…`;
- the device endpoints and `POST /api/v1/tokens/revoke`;
- the CI exchange (INV-10) and, from P1, the two npm trusted-publishing exchange routes
  (R-AUTH-001, [formats/npm.md](formats/npm.md) R-NPM-022);
- the npm login start;
- `:8081` operations;
- `GET` and `HEAD` of `/` (the redirect to the UI) and of the UI shell under `/ui/`: the static
  application carries no data, every
  screen reads through the management API, and an unauthenticated visitor must reach the sign-in
  screen (Q-AUTH-19);
- whatever [api-and-ui.md](api-and-ui.md) declares public, for example the OpenAPI document.

**Switching it off.** Turning anonymous access off takes effect on every replica within the
revocation bound of R-AUTH-023, because the runtime setting is a cached row invalidated through
the change area `auth`.

Acceptance criteria:

- With anonymous access off, `curl -si https://trove.test/api/npm/npm-all/lodash` gets 401.
- With anonymous access on and a policy granting `anonymous` read on `npm-all`, the same call
  gets 200. `curl -si https://trove.test/api/npm/no-such-repo/lodash` gets 401, not 404.
- `curl -X POST https://trove.test/api/v1/repositories` without credentials gets 401 even if
  a policy grants `anonymous` `manage`.

### R-AUTH-038 · Brute-force protection and rate limiting
Priority: **MVP** · Covers: IAM-042, ADMAPI-012, ADMAPI-050

**Threat model.** Token guessing is infeasible at 256 bits, and the checksum filters random
input before any database access. The limits below protect three things: the one password in
the system, the database from floods of well-formed fake tokens, and the endpoints that create
rows without authentication.

**Where the counters live (Q-AUTH-11).** Requests reach replicas at random and the replica count
changes with autoscaling, so every limit on callers on this page counts cluster-wide and gives
the same answer on every replica. The break-glass lockout counts in PostgreSQL; every other limit
on callers is a cluster-scoped limiter of [architecture.md](architecture.md) R-ARCH-034, counted in
Valkey. Only the Argon2id verification slots of R-AUTH-014 count per replica, because they protect
the replica's own memory, as `server.max_in_flight` does.

| Limiter | Bucket | Counts | Rate and burst |
|---|---|---|---|
| `auth.credential_failures` | client address | rejected credentials (count-only: checked before validation, counted after a rejection) | `failures_per_address` per minute |
| `auth.flow_starts` | client address | every request to the endpoints listed below | `starts_per_address` per minute |
| `auth.device_code_failures` | principal ID | wrong user codes (count-only, R-AUTH-036) | `device_code_failures` per `auth.cli_login.ttl` |

Each limiter is registered with the configured value as burst and that value divided by its window
as rate, so R-ARCH-034's window *W* is one minute, or `auth.cli_login.ttl` for wrong device codes.

While Valkey is unavailable, each limiter answers from per-replica buckets at the configured value
divided by the live `web` replicas (R-ARCH-034), so the cluster-wide budget stays close to the
configured one, and `trove_ratelimit_fallback{limiter}` is 1. A limiter never fails a request
because Valkey failed, and Valkey being unavailable never makes a replica unready (INV-12).

**Break-glass lockout (cluster-wide, in PostgreSQL).** Failures are counted in PostgreSQL, not in
Valkey: the lockout guards a credential, and a Valkey failover or flush must not hand a guesser a
fresh threshold (INV-3, R-ARCH-034):

- `lockout.threshold` failures within `lockout.window` lock the account until
  `now + lockout.duration`;
- each failure is counted with one `INSERT … ON CONFLICT (account_key) DO UPDATE` statement, so
  failures arriving on several replicas at the same moment are all counted;
- every attempt reads the row, and no replica caches it, so a lock or an unlock is in force on
  every replica at once;
- during the lock, the local login answers 429 with `Retry-After`, even for the correct
  password;
- locks expire on their own, so nobody can lock out the recovery account permanently;
- an administrator (`POST /api/v1/users/{breakGlassName}/unlock`, 204) or
  `trove admin break-glass unlock` clears a lock early;
- the user record shows `lockedUntil`, and `GET /api/v1/users?status=locked` lists the account
  (ADMAPI-050);
- each lock writes `auth.breakglass.locked`.

The per-node counting weakness of the catalog's suspension mechanism does not apply.

**Failed-credential throttle (cluster-wide):**

- More than `failures_per_address` rejected credentials from one client address within the
  limiter's one-minute window, counted on all replicas together, put that address into throttled
  mode until the windowed count falls below the limit.
- The throttle is consulted only for a credential that the replica cannot answer from its caches
  (R-AUTH-023 step 2). A cache hit costs no Valkey round trip.
- In throttled mode, a request whose credential is neither in this replica's cache nor marked as
  recently validated gets 429 with `Retry-After` and is not validated. A successful validation
  from PostgreSQL marks the credential in Valkey for `recent_token_window`, under a key derived
  from its secret hash ([architecture.md](architecture.md) R-ARCH-030). Tokens validated recently
  on any replica therefore keep working on every replica, so colleagues behind the same NAT are not
  cut off, whichever replica their requests reach.
- A marker only lets a credential be validated. Validation itself still reads PostgreSQL or the
  replica's own cache (R-AUTH-023), so a marker never authenticates anyone.
- While Valkey is unavailable, only the replica's own cache exempts a credential, and the
  limiter's per-replica share applies (above).
- Addresses in `exempt_cidrs` are never throttled.

**Unauthenticated endpoints that create rows or run the password hash.** These are limited to
`starts_per_address` per minute and address, cluster-wide:

- `/auth/oidc/*/start`;
- npm `-/v1/login`;
- `/api/v1/auth/device/authorize`;
- `POST /api/v1/tokens/revoke`;
- `POST /auth/local/login`, whose Argon2id verification runs for every attempt (R-AUTH-014).

In addition, more than `max_pending_flows` unexpired pending rows in `oidc_login_flow` and
`cli_login` together answer 503. Each `web` replica reads that count from PostgreSQL at most every
5 s, so the decision is at most 5 s behind the table on every replica.

**Client address.** It is determined per [architecture.md](architecture.md), with a trusted
proxy list. Without that list, forwarded headers are ignored, so a spoofed `X-Forwarded-For`
cannot dodge the limits.

Data model:

```sql
CREATE TABLE auth_lockout (
  account_key         text        PRIMARY KEY,   -- 'break-glass'
  failures            integer     NOT NULL DEFAULT 0,
  window_started_at   timestamptz NOT NULL,
  locked_until        timestamptz,
  password_changed_at timestamptz                -- start of the last rotation hold (R-AUTH-014)
);
```

Configuration:

```yaml
auth:
  break_glass:
    lockout:
      threshold: 5          # default (proposed)
      window: 15m           # default (proposed)
      duration: 15m         # default (proposed)
  rate_limit:
    failures_per_address: 300  # default (proposed); per minute and address, cluster-wide; per-replica share while Valkey is unavailable
    starts_per_address: 30     # default (proposed); per minute and address, cluster-wide; per-replica share while Valkey is unavailable
    device_code_failures: 5    # default (proposed); per user within auth.cli_login.ttl, cluster-wide; per-replica share while Valkey is unavailable (R-AUTH-036)
    recent_token_window: 10m   # default (proposed); throttle exemption of a credential validated on any replica
    max_pending_flows: 100000  # default (proposed)
    exempt_cidrs: []           # default (proposed)
```

Acceptance criteria:

- Five wrong break-glass passwords, spread across two replicas, lock the account on both. The
  correct password then gets 429 until `trove admin break-glass unlock` runs. Five wrong passwords
  sent at the same moment to three replicas lock it as well, and `valkey-cli FLUSHALL` does not
  unlock it.
- 400 requests with random well-formed tokens from one address within a minute, sent one after
  another, round robin over three `web` replicas, are validated at most 300 times; the rest get
  429. A PAT that was used
  successfully from that address on one replica within the previous 10 minutes still works during
  the burst on all three.
- With Valkey stopped, the same burst is validated at most 300 times across the three replicas,
  and `trove_ratelimit_fallback{limiter="auth.credential_failures"}` is 1 on each of them.
- 40 `GET /auth/oidc/corp/start` from one address within a minute, sent one after another, round
  robin over three replicas, get 429 from the 31st on.

## Provisioning

### R-AUTH-039 · SCIM 2.0 provisioning
Priority: **P1** · Covers: AUTHN-028, AUTHN-029, AUTHN-030, ADMAPI-035

trove implements the SCIM 2.0 service-provider subset that IdPs need for user and group
lifecycle ([RFC 7643](https://www.rfc-editor.org/rfc/rfc7643),
[RFC 7644](https://www.rfc-editor.org/rfc/rfc7644)). The base path is `/api/v1/scim/v2`.

**Supported:**

| Resource | Operations | Notes |
|---|---|---|
| `/ServiceProviderConfig`, `/ResourceTypes`, `/Schemas` | GET | Bulk, sort, ETag and change-password are advertised as unsupported |
| `/Users` | GET (filter `userName eq`, `externalId eq`; `startIndex`, `count`), POST | `id` is the principal UUID |
| `/Users/{id}` | GET, PUT, PATCH (`active`, `emails`, `name`, `displayName`, `userName`), DELETE | Renames are allowed when the new name is free, because policies reference IDs |
| `/Groups` | GET (filter `displayName eq`, `externalId eq`), POST | `displayName` must satisfy the group-name rule, otherwise 400 |
| `/Groups/{id}` | GET, PUT, PATCH (members add, remove, replace), DELETE | Memberships get source `scim` |

Media type `application/scim+json`. Errors use the SCIM error schema.

**Mapping:**

| SCIM attribute | trove attribute |
|---|---|
| `userName` | Username, after the R-AUTH-007 normalization |
| `externalId` | `scim_external_id` |
| Primary `emails` value | Email |
| `displayName`, or `name.formatted` | Display name |
| `active` | Status |

**Lifecycle effects (AUTHN-030):**

- `active: false` or DELETE disables or deletes the user as in R-AUTH-016, with
  `revokeTokens: true`.
- SCIM-provisioned users are exempt from dormancy, because the IdP now owns their lifecycle.

**Linking to OIDC logins.** SCIM users are created with a pending link (R-AUTH-007) for
`scim.loginProvider`. The first login completes the link through one of two settings:

- `linkBy: externalIdClaim`: the configured claim (for example Entra ID's `oid`) must equal the
  SCIM `externalId`;
- `linkBy: username`: the mapped username must equal `userName`.

**Connector token.** The IdP authenticates with a SAT of a dedicated service account:

- the account is bound to the administrator role or the narrower identity role of
  [authorization.md](authorization.md);
- the token's scope is `{"system": ["identities:read", "identities:write"]}`;
- under the derivation rule (R-AUTH-027), that token cannot mint other tokens with broader
  scope. Rotation follows R-AUTH-030.

Configuration:

```yaml
auth:
  scim:
    enabled: false               # default (proposed)
    login_provider: corp         # example
    link_by: externalIdClaim     # example: external_id_claim | username
    external_id_claim: oid       # example
```

Acceptance criteria:

- An Entra ID or Okta SCIM test run (the IdP's validator, or a recorded conversation replayed in
  CI) creates `bob`, adds him to `team-web` and sets `active: false`. Afterwards `bob`'s PAT
  gets 401, and the audit trail shows `user.disabled` with source `scim`.
- `GET /api/v1/scim/v2/Users?filter=userName%20eq%20%22bob%22` returns a ListResponse with
  `totalResults: 1`.

## Settings and audit

### R-AUTH-040 · Runtime authentication settings
Priority: **MVP** · Covers: ADMAPI-047, IAM-038, AUTHN-036

Values that administrators change at runtime live in one row in PostgreSQL. Everything else is
YAML (INV-15).

**Read:** `GET /api/v1/settings/auth` (administrator or `settings:read`) returns three groups:

- the effective runtime values;
- the YAML ceilings they are bound by;
- read-only status: providers with discovery state, break-glass enabled and locked, session
  timeouts, the client switches, the command-line login poll binding mode (R-AUTH-034, with a
  warning when it is `off`, and another when it is not `off` while `server.trusted_proxies` is
  empty), and the SCIM state.

**Change:** `PATCH /api/v1/settings/auth` (administrator, session with recent authentication or
token with `admin: true`) accepts:

```json
{
  "anonymousAccess": false,
  "tokens": {
    "pat": {"defaultTTLSeconds": 7776000, "maxTTLSeconds": 31536000},
    "sat": {"defaultTTLSeconds": 7776000, "maxTTLSeconds": 31536000},
    "expiryNoticeSeconds": 604800
  }
}
```

**Validation:**

- Each value must be at most its YAML ceiling, and each default at most its maximum. Otherwise
  400 `exceeds_ceiling` or 400 `default_above_maximum`.
- Enabling anonymous access while the ceiling forbids it answers 409 `anonymous_not_allowed`.
- Every change writes `settings.auth.changed` with the before and after values, and records
  itself in the change area `auth` in the same transaction, so every replica applies it within
  the revocation bound of R-AUTH-023.

The declarative configuration-as-code file (P1,
[operations-and-observability.md](operations-and-observability.md)) may set these values. It
uses the same validation.

Data model:

```sql
CREATE TABLE auth_settings (
  singleton            boolean     PRIMARY KEY DEFAULT true CHECK (singleton),
  anonymous_access     boolean     NOT NULL DEFAULT false,
  pat_default_ttl      interval,   -- NULL = YAML value
  pat_max_ttl          interval,
  sat_default_ttl      interval,
  sat_max_ttl          interval,
  expiry_notice        interval,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  updated_by           uuid        REFERENCES principal (id) ON DELETE SET NULL
);
```

Acceptance criteria:

- `PATCH` with `maxTTLSeconds` above the YAML ceiling gets 400 `exceeds_ceiling`.
- Switching `anonymousAccess` on takes effect for anonymous requests on every replica within
  the revocation bound, and one audit event records the change. Switching it off takes effect
  within the same bound with Valkey stopped.

### R-AUTH-041 · Authentication audit events
Priority: **MVP** · Covers: OPS-022, OPS-023

This page defines the events that authentication emits.
[operations-and-observability.md](operations-and-observability.md) owns their storage, query
API, retention and log emission (INV-16).

**Event fields.** Every event carries:

- `actor`: kind, name and ID; `admin-cli` for commands; `anonymous`;
- `action`: the event type below;
- `outcome`: `success` or `failure`, plus a `reason` code;
- `source`: client address, user agent and the request's trace ID;
- `subject`: the affected principal or token;
- `details`: JSON.

Secrets never appear in events: no token values, hashes, session secrets, passwords, ID tokens
or authorization codes.

**Event types:**

| Event | Emitted by | Persisted |
|---|---|---|
| `auth.login.succeeded`, `auth.login.failed` | R-AUTH-006, R-AUTH-007; reasons from those pages | yes |
| `auth.breakglass.login`, `auth.breakglass.locked` | R-AUTH-014, R-AUTH-038; severity `high` | yes |
| `auth.logout`, `auth.session.ended` (admin, evicted, principal disabled) | R-AUTH-010, R-AUTH-013 | yes; idle and absolute expiry are not recorded |
| `auth.csrf_failed` | R-AUTH-012 | yes, at most one per session and minute on each replica; a log-volume bound, not a count |
| `auth.credential.rejected` | R-AUTH-001 for malformed or unknown credentials | log stream and metric only, because of volume |
| `token.rejected` | R-AUTH-023 for a revoked or expired token whose row exists | yes, at most one per token and hour in the whole cluster (R-AUTH-023 step 4); signals a leak or a stale client |
| `token.created`, `token.rotated`, `token.revoked`, `token.expiring` | R-AUTH-027 to R-AUTH-032 | yes |
| `user.created`, `user.updated`, `user.renamed`, `user.rename_conflict`, `user.disabled`, `user.enabled`, `user.deleted`, `user.dormancy_warning` | R-AUTH-007, R-AUTH-016, R-AUTH-021, R-AUTH-039 | yes; `details.source` is `jit`, `admin`, `scim`, `dormancy` or `admin-cli` |
| `user.groups.synced` | R-AUTH-008, with added and removed lists | yes, only when something changed |
| `user.groups.claim_missing` | R-AUTH-008, with provider and reason (`absent`, `wrong_type`, `overage`); severity `warning` | yes, once per affected login |
| `group.created`, `group.updated`, `group.deleted`, `group.members.changed` | R-AUTH-018, R-AUTH-039 | yes |
| `serviceaccount.created`, `serviceaccount.updated`, `serviceaccount.disabled`, `serviceaccount.deleted` | R-AUTH-020 | yes |
| `cli_login.approved`, `cli_login.denied` | R-AUTH-034, R-AUTH-036 | yes |
| `cli_login.poll_refused` | R-AUTH-034 address binding, with the refused and the requesting address | yes, once per change of the refused address within a login, decided under the row's lock (R-AUTH-034) |
| `settings.auth.changed` | R-AUTH-040 | yes |

**Revocation journal (MVP).** A database restore to time *t* undoes every access removal made
after *t* (H-37 in [ha-and-helm.md](ha-and-helm.md)). The events below are therefore flagged
`journal: revocation` in the event registry
([operations-and-observability.md](operations-and-observability.md) R-OPS-014). The audit writer
copies each one into the revocation journal of [ha-and-helm.md](ha-and-helm.md) R-HA-054, which
is MVP. `trove admin post-restore` re-applies it after a platform restore (Q-HA-9), and
`trove backup restore` re-applies it after a restore of a trove backup
([backup-and-restore.md](backup-and-restore.md) R-BAK-024), with the backup's snapshot time as
*t*. An entry carries IDs and states, never a token value, hash or other secret.

| Event | The journal entry carries | Replay (R-HA-054) |
|---|---|---|
| `token.revoked` | Token ID and revocation reason | Revoke the token with the cascade of R-AUTH-029; reason `leaked` also repeats the forward cascade over rotation successors |
| `token.rotated` | The predecessor's token ID, its new `expires_at` and `overlapSeconds` (R-AUTH-030) | Set the predecessor's `expires_at` to the earlier of its current and its journaled value; revoke it when the overlap was 0 |
| `user.disabled`, `user.deleted` | Principal ID and username; for `user.disabled` also `disabled_reason` | Disable the user, with the journaled reason for `user.disabled` and reason `admin` for `user.deleted` |
| `serviceaccount.disabled`, `serviceaccount.deleted` | Principal ID and name | Disable the service account |
| `group.deleted` | Principal ID and name | Delete the group |
| `group.members.changed` | The group, and the removed memberships, each with its member and source (`manual`, `auto`, `scim`) | Remove each removed membership with its source |
| `user.groups.synced` | The user, and the removed memberships, each with its group and source `oidc:<providerId>` | Remove each removed membership with its source |
| `settings.auth.changed`, only when the change sets `anonymousAccess` | `anonymousAccess` before and after; the token lifetime fields are not part of the entry | Update rule with the comparator below |

- **Replay never deletes a user or service account.** A replayed deletion disables the principal,
  which already makes its tokens fail with `principal_disabled` (R-AUTH-016, R-AUTH-020), and
  keeps a mistaken or forged entry reversible. With reason `admin`, no OIDC login re-enables the
  user (R-AUTH-021 re-enables only `dormant`); an administrator deletes the user again.
- **Membership removals are the journaled part.** Every path that removes a membership writes one
  of the two membership events, including `PATCH /api/v1/users/{username}/groups` (as
  `group.members.changed`) and SCIM group changes. Added memberships are not part of the entry and
  are never replayed.
- **Comparator for the anonymous-access switch.** `narrows(current, after)` is true when
  `after.anonymousAccess` is `false` (off narrows on) or equals `current.anonymousAccess`, and
  false when `after` is `true` while `current` is `false`. Replay can therefore switch anonymous
  access off but never on: an entry that switched it on is skipped under case 2 of R-HA-054,
  whatever its journaled before-state says.
- **Not journaled.** The break-glass account's `enabled` state and the YAML ceiling
  `auth.anonymous.allowed` live in the configuration file, not in PostgreSQL, so a restore does not
  change them.

**Metrics.** Proposed names, which the operations page may rename:

- `trove_auth_attempts_total{method,outcome,reason}`
- `trove_auth_sessions_active`
- `trove_tokens_active{type}`
- `trove_tokens_expiring{type}`
- `trove_auth_cache_invalidations_total`
- `trove_auth_cache_bypassed_total{reason}` (`changes_stale`, R-AUTH-023)
- `trove_auth_ratelimited_total{limit}` (`credential_failures`, `flow_starts`,
  `device_code_failures`, `break_glass_lockout`, `break_glass_verifications`, `pending_flows`)
- `trove_auth_throttle_exempt_total{via}` (`cache`, `marker`; R-AUTH-038)
- `trove_auth_session_touch_failures_total` (R-AUTH-010)
- `trove_auth_lastused_flush_failures_total`
- `trove_auth_group_claim_missing_total{provider,reason}`
- `trove_auth_cli_poll_refused_total`

Acceptance criteria:

- The full acceptance suite of this page leaves no audit event containing `trv_`, `eyJ` (a
  JWT) or the break-glass password.
- A failed OIDC login with a wrong nonce produces exactly one `auth.login.failed` with
  `reason=nonce_mismatch` and the client address.

## Operation across replicas

### R-AUTH-042 · Authentication across replicas and roles
Priority: **MVP** · Covers: AUTHN-011, HA-001, HA-009

The Kubernetes Service hands every request to a random `web` replica, and a node drain can remove
any replica at any time (INV-12). Authentication therefore keeps nothing on a replica that a later
request needs (INV-18, [architecture.md](architecture.md) R-ARCH-032). This table is the complete
list of authentication state; a new kind needs a row here before it ships:

| State | System of record | What one replica keeps | Valkey's part | Valkey unavailable or emptied |
|---|---|---|---|---|
| Browser login flow: state, nonce, PKCE verifier, browser binding (R-AUTH-005) | `oidc_login_flow` | nothing | none | unaffected |
| npm web login and device login: approval, poll pacing, refused polls (R-AUTH-034, R-AUTH-036) | `cli_login` | nothing | none | unaffected |
| UI sessions, CSRF hashes, idle deadline (R-AUTH-010) | `ui_session` | cache entries (R-AUTH-023); per session the newest `last_seen_at` it has read, a lower bound of the row that only saves touch statements | change messages only | revocation bound kept by the change-log poll |
| Tokens, principal status, memberships, runtime settings | PostgreSQL | cache entries (R-AUTH-023) | change messages only | same |
| Credential lookups while the freshness gate bypasses the caches (R-AUTH-023) | PostgreSQL | a query shared by concurrent requests for at most `auth.revocation_bound`; nothing outlives it | none | unaffected |
| Break-glass lockout and rotation hold (R-AUTH-014, R-AUTH-038) | `auth_lockout` | nothing | none | unaffected |
| Failed-credential throttle, flow starts, wrong device codes (R-AUTH-038) | none: disposable counters | fallback buckets while Valkey is unavailable | the counters | per-replica shares of each limit |
| Throttle exemption of recently validated credentials (R-AUTH-038) | none | its own cache | markers | only its own cache exempts |
| Last-used times (R-AUTH-031, P1) | `api_token` | a write-only buffer until the next flush | none | unaffected |
| OIDC discovery documents and JWKS (R-AUTH-004) | the provider | its own copy | none | unaffected |
| Break-glass verifier and OIDC client secrets (R-AUTH-004, R-AUTH-014) | Kubernetes Secrets | read from the mounted files; the loaded break-glass file content, to notice a rotation | none | unaffected |
| Argon2id verification slots (R-AUTH-014) | none | the number of verifications running | none | unaffected; it protects the replica's own memory |
| Count of pending flows (R-AUTH-038) | PostgreSQL | a copy at most 5 s old | none | unaffected |
| De-duplication of `auth.csrf_failed` events (R-AUTH-041) | none | the minute of the last event per session | none | unaffected; it bounds log volume only |

Rules:
- **Every step on any replica.** A step of a multi-request flow reads its state from PostgreSQL and
  changes it with a conditional statement, so two replicas serving two steps of one flow at the
  same moment cannot both succeed: a flow row is consumed once (R-AUTH-006 step 1), an approval
  mints one token (R-AUTH-034, R-AUTH-036), a first login links its identity once (R-AUTH-007),
  logins of one user and the session and token limits run under the principal's row lock
  (R-AUTH-006 step 10, R-AUTH-010, R-AUTH-027), a session's deadlines are decided from its row
  (R-AUTH-010), and a break-glass rotation opens one hold (R-AUTH-014).
- **Valkey never decides who a caller is** (Q-AUTH-9, Q-AUTH-12). No session, token, flow, approval
  or lookup result is read from Valkey. What Valkey holds can make trove answer 429 or run one more
  PostgreSQL lookup; it never turns a rejection into an acceptance and never keeps a revoked
  credential alive ([architecture.md](architecture.md) R-ARCH-030, H-49). A Valkey outage never
  makes a replica unready (INV-12).
- **Roles** (INV-2). The routes of this page are served by the client listener, which runs on
  `web` replicas (and on `all` in the local profile). Only those replicas load the OIDC providers
  and the break-glass password, and the chart mounts the OIDC client-secret and break-glass Secrets
  into `web` pods only. `worker` replicas run the scheduled tasks of R-AUTH-043. `trove admin`
  (R-AUTH-015) runs in any pod and writes through the change log.
- **Background loops on `web` replicas** ([architecture.md](architecture.md) R-ARCH-001): the
  discovery refresh and JWKS refetch (R-AUTH-004), the re-read of the client-secret and break-glass
  files with the rotation check of R-AUTH-014 (R-AUTH-004, R-AUTH-014), the last-used flush
  (R-AUTH-031, P1) and the pending-flow count (R-AUTH-038). Every iteration carries a deadline, and
  none holds a database connection across an outbound call or a Valkey call.
- **Drain.** Every request of this page is short; the longest, the OIDC callback, is bounded by the
  three 10 s timeouts of R-AUTH-004 and R-AUTH-006 and finishes within the default drain of
  [architecture.md](architecture.md) R-ARCH-024. No flow needs a hand-over, and no rotation or
  limit depends on the replica that started it. A replica killed without a drain loses the
  callbacks it was serving, whose users start the login again, and one interval of last-used
  times (P1).

Acceptance criteria:
- In the `scatter` profile ([architecture.md](architecture.md) R-ARCH-032: three `web` replicas,
  every request to another replica), each of these completes: 50 browser logins, a
  re-authentication (R-AUTH-011), a logout, an npm web login, a device login, a token revocation
  followed by a request with the revoked token (401 on every replica), and a break-glass lockout.
- The same suite passes with Valkey stopped for its whole duration, and every replica stays ready
  with `"degraded":["valkey"]`.
- `valkey-cli FLUSHALL` in the middle of the suite makes no login, session or token request fail
  and revives no revoked token.
- The drain test of [architecture.md](architecture.md) R-ARCH-024, with a loop of 20 browser logins
  and 20 npm web logins added, completes every login.

### R-AUTH-043 · Scheduled authentication tasks
Priority: **MVP** · Covers: trove-only

The background work of this page runs as maintenance tasks of the job queue
([architecture.md](architecture.md) R-ARCH-016) on `worker` replicas, never on `web` replicas
(INV-2). The scheduler enqueues one job per slot, and the queue gives each job one claimant, so a
slot runs once in the cluster whatever the number of `worker` replicas. Each task works in batches
of at most 1,000 rows per transaction, is idempotent, and resumes on another worker when a drain
releases its job.

| Task | Does | Default schedule (proposed) | Owning requirement |
|---|---|---|---|
| `session-cleanup` | Deletes `oidc_login_flow` and `cli_login` rows past `expires_at`, and `ui_session` rows 30 days after their end | `*/10 * * * *` | R-AUTH-005, R-AUTH-010, R-AUTH-034 |
| `token-cleanup` | Deletes `api_token` rows `retention_after_end` after expiry or revocation | `40 3 * * *` | R-AUTH-023 |
| `token-expiry-notice` | Writes `token.expiring` for tokens entering the notice period | `0 6 * * *` | R-AUTH-032 |
| `user-dormancy` (P1) | Disables dormant users and emits the dormancy warnings | `30 4 * * *` | R-AUTH-021 |

The schedules are keys of `maintenance.tasks.<name>`
([operations-and-observability.md](operations-and-observability.md) R-OPS-019). Expired flow rows
that wait for the next cleanup are never served: every read checks `expires_at`, and the
pending-flow count of R-AUTH-038 counts unexpired rows only. A task whose run overlaps another run
of itself, because a job was resumed while its first claimant still ran, changes every row with a
conditional statement and writes events only for rows it changed (R-AUTH-021, R-AUTH-032).

Configuration:

```yaml
maintenance:
  tasks:
    session-cleanup: {enabled: true, schedule: "*/10 * * * *"}      # default (proposed)
    token-cleanup: {enabled: true, schedule: "40 3 * * *"}          # default (proposed)
    token-expiry-notice: {enabled: true, schedule: "0 6 * * *"}     # default (proposed)
    user-dormancy: {enabled: true, schedule: "30 4 * * *"}          # default (proposed); P1, R-AUTH-021
```

Acceptance criteria:
- With three `worker` replicas, one day of schedules produces exactly one job per slot of each
  task, and no `web` replica's instance ID appears in `job.locked_by` for them.
- A `worker` pod deleted gracefully during `token-cleanup` releases the job; another worker finishes
  it, and every row past retention is gone afterwards.

## Deferred

Catalog features of this area with priority P2 or Later, and trove-only extensions that are
not scheduled:

- IAM-012 (P2): "remember me" persistent login. The absolute session timeout plus IdP single
  sign-on cover the need.
- IAM-017 (P2): groups imported from a directory realm. OIDC group sync and SCIM replace it.
- IAM-035 (P2): encrypted client passwords. Not applicable, because there are no client
  passwords, only tokens.
- IAM-039 (P2): TOTP second factor. For trove it is relevant only to break-glass (H-17).
- IAM-041 (P2): password expiration. Break-glass rotation is a Secret rotation (R-AUTH-014).
- IAM-050 (P2): SSH authentication.
- AUTHN-004 (P2): fallback from a directory to the internal password. No internal passwords.
- AUTHN-010 (P2): preset provider types (Google, GitHub, Cloud Foundry UAA). The generic OIDC
  type covers OIDC-capable IdPs.
- AUTHN-012 (P2): binding an internal account to an OAuth identity.
- AUTHN-013 (P2): automatic pairing of accounts by email. Deliberately not done; see R-AUTH-007.
- AUTHN-015 to AUTHN-021 (P2): SAML 2.0 service-provider login, configurations, encryption,
  group attributes, auto-redirect, single logout and directory group lookup.
- AUTHN-022 to AUTHN-027 (P2): LDAP and Active Directory login and group synchronization.
- AUTHN-031 (P2): trusted reverse-proxy header login.
- AUTHN-032, AUTHN-033 (P2): Crowd and Jira connector.
- AUTHN-034 (P2): mutual TLS client authentication.
- AUTHN-035 (P2): one-time-password second factor.
- AUTHN-041 (P2): SSH public keys on the profile.
- AUTHN-042 (P2): password self-service and reset. No passwords.
- AUTHN-043 (P2): legacy per-user API key. Tokens in every header position replace it.
- TOK-003 (P2): self-contained signed token format. trove tokens are opaque (INV-8).
- TOK-011, TOK-013 (P2): project-role and project-membership scopes. They come with projects.
- TOK-016 (P2): audience restriction. It is meaningful only with several trusting instances.
- TOK-020, TOK-021, TOK-022 (P2): revocability and persistence thresholds and the
  force-revocable flag. Not applicable, because every trove token is stored and revocable.
- TOK-023 (P2): email notification before token expiry. It needs the mail server; events and the
  UI banner are MVP (R-AUTH-032).
- TOK-035 (P2): a companion command-line client with token handling. R-AUTH-036 serves any such
  client.
- TOK-036 to TOK-038 (P2): token-signing key pairs, custom certificates, key rotation. Not
  applicable to opaque tokens.
- TOK-039, TOK-040 (Later): cross-instance token trust and trust links.
- TOK-042 (Later): pairing tokens.
- TOK-044, TOK-046, TOK-047 (Later): legacy API keys and older or secondary token operations.
- ADMAPI-010, ADMAPI-011, ADMAPI-051, ADMAPI-052, ADMAPI-054 (P2): password change, expiry,
  encrypted-password and complexity operations. No passwords.
- ADMAPI-032 to ADMAPI-034, ADMAPI-036 (P2), ADMAPI-037 (Later): SAML, directory,
  reverse-proxy and Crowd configuration operations.
- ADMAPI-049 (Later): legacy API-key operations.
- ADMAPI-056 (P2): trust-root certificates of the token issuer.
- PKG-NPM-012 (P2): npm one-time-password challenge on writes. The second factor belongs to the
  IdP.
- PKG-NPM-014 (P2): `npm token` and `npm profile` endpoints. The trove token API covers the
  need; these routes answer 404 per [formats/npm.md](formats/npm.md).
- trove-only (P2): OIDC back-channel and front-channel logout
  ([Back-Channel Logout 1.0](https://openid.net/specs/openid-connect-backchannel-1_0.html)). The
  `idp_sid` column is prepared.
- trove-only (P2): `private_key_jwt` client authentication at the IdP token endpoint.
- trove-only (P2): an optional `acr`/`amr` requirement per provider (H-18).

Not planned (Out of scope in the catalog): IAM-011, IAM-043, IAM-044, AUTHN-014, TOK-043,
TOK-045, TOK-048.

Handed to other pages, not deferred:

- [authorization.md](authorization.md): IAM-006, IAM-007, IAM-008 (administration and
  scan-related roles); IAM-019 to IAM-033 (permission policies); IAM-034 (anonymous grants by
  delegated managers); IAM-051 to IAM-068 (projects, roles and environments, P2 and Later).
- [gitlab-ci-oidc.md](gitlab-ci-oidc.md): IAM-040 (hiding global identity mappings from project
  administrators, P2).
- No page in the ownership table claims IAM-046 to IAM-049 (signing key pairs, external secret
  managers, JAR signing keystore, trusted public keys; P2 and Later). They are package and
  secret infrastructure, not identity.
- No page in the ownership table claims IAM-045 (encryption of stored configuration secrets). It
  concerns upstream credentials, so [repositories-and-proxy.md](repositories-and-proxy.md) or
  [architecture.md](architecture.md) has to take it. This page stores no such secret: the OIDC
  client secret stays in a mounted file.

## Security notes

- **The IdP is trusted for identity and, through group sync, possibly for administrator
  rights.** A rule that maps an IdP group to a group bound to the administrator role
  (R-AUTH-008) makes the IdP's group administrators trove administrators. Keep such rules
  exact (`match`, not `match_regex`), and review them like the role bindings themselves.
- **No linking by email or username.** Returning identities are found only by (`iss`, `sub`).
  First-login conflicts fail instead of merging (R-AUTH-007). This closes account takeover
  through a second IdP, a recycled email address or a user-editable `preferred_username`.
  Where an IdP lets users edit `preferred_username`, take the username from an immutable claim
  instead.
- **Plain SHA-256 is enough for token hashing.** The 256-bit random part makes the hash
  preimage-resistant without salt or stretching. The checksum only serves scanners
  (R-AUTH-022, R-AUTH-023).
- **The Basic username is not an identity claim.** Audit and `whoami` always show the token's
  owner, never the presented username (R-AUTH-001, R-AUTH-033).
- **Tokens in URLs.** Only npm logout puts a token into a path. Logs redact the token pattern
  everywhere, and query-string tokens are refused (R-AUTH-001, R-AUTH-035).
- **Revocation latency is a bounded promise**: ≤ `auth.revocation_bound` (2 s) on every replica,
  with or without Valkey. A replica that cannot prove the bound answers from PostgreSQL or with
  503, never from a stale cache (R-AUTH-023, Q-AUTH-10). Incident runbooks can rely on it.
- **Valkey is outside the authentication trust path.** No session, token, login flow, approval or
  lookup result is read from Valkey (Q-AUTH-9, Q-AUTH-12, R-AUTH-042). Someone who can write under
  trove's Valkey prefix can make trove refuse callers with 429, lift the cluster-wide throttle, or
  cause extra PostgreSQL lookups; they cannot authenticate anyone or keep a revoked credential
  alive ([architecture.md](architecture.md) H-49). The break-glass lockout lives in PostgreSQL, so
  a Valkey flush does not reset it (R-AUTH-038).
- **Anonymous access fails toward 401.** Probing unknown repository keys reveals nothing, and
  anonymous callers can never write through the management API, whatever a policy says
  (R-AUTH-037).
- **The break-glass account** is password-only and administrator by construction. It is
  disabled by configuration in normal operation, restricted (no tokens), loud in the audit
  trail, and locked out cluster-wide on guessing (R-AUTH-014, R-AUTH-038). Its Secret is mounted
  into `web` pods only (R-AUTH-042). A password rotation ends its sessions through PostgreSQL, so a
  drain of the replica that noticed it does not undo the end (R-AUTH-014, Q-AUTH-14). Residual
  risks: H-17, H-52.
- **Credential-minting needs a recent IdP round trip** (R-AUTH-011), and token-authenticated
  minting is bounded by the derivation rule (R-AUTH-027). Rotation counts as minting: it follows
  the same rules, and nobody but the owner can rotate a PAT (R-AUTH-030). A hijacked session or a
  narrow leaked token therefore cannot be turned into a broad, long-lived credential.

## Decisions

Each question below was decided by the development agent from the listed options; the owner reviews the choices at the walkthrough.

### Q-AUTH-1 · Where login provider configuration lives

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Configuration file only | The whole provider entry (connection, secret reference, claim mapping, gates, group rules) lives in the YAML file; the API shows it read-only | Login works before any administrator exists; the client secret never enters the database; every change is validated at startup before it serves anyone and goes through the same review as the rest of the deployment (Helm values, GitOps) | Every change needs a rollout of the replicas; no UI editing; during the rollout, a login started on a new replica can fail on an old one and must be retried |
| (b) PostgreSQL and API | Providers are created and edited through the API and UI, like the CI identity mappings | UI-editable; no rollout | The client secret must be sealed in the database (R-ARCH-021 key ring); an edit reaches every replica at once, so one wrong edit can lock out every OIDC administrator, and recovery then depends on break-glass |
| (c) File defines, API overrides | The file holds the entry, the database may override fields | Bootstrap from the file, convenience in the UI | Two sources of truth: the file no longer tells the operator what is in effect |
| (d) Split by concern | Connection and secret in the file; claim mapping, gates and group rules in the database | Group rules, the part that changes when teams change, become UI-editable without a rollout | The most security-sensitive part (which IdP group becomes administrator) moves to the less reviewed place; one provider is defined in two places |

**Choice:** (a) — login providers, including their claim mapping, gates and group rules, are configured only in the YAML file.

**Rationale:** Login is bootstrap-critical. It has to work before any administrator exists, and
it must not be breakable through the API it protects. (a) is the only option in which a provider
edit passes review and startup validation before it serves anyone. An entry that fails
validation keeps the new replica from becoming ready, the rolling update stalls with
`maxUnavailable: 0`, and the previous replicas keep serving logins (R-HA-020). A valid but wrong
entry, such as a mistyped client ID, is undone with `helm rollback`, which needs no working
login. In (b) and (d), a wrong edit reaches every replica within seconds, and undoing it needs the
API it just broke, so recovery depends on break-glass. Group rules decide
who becomes administrator (security notes), so they belong in the reviewed, versioned file,
which rules out (d). The usual argument for (d) is that new teams need new rules, and
`match_regex` rules with `$1` expansion (R-AUTH-008) already cover new teams without an edit.
(b) would also need one more sealed secret. (c) leaves the operator unable to tell from the file
what is in effect. The cost of (a) is a rollout per change, which a Helm upgrade performs without
downtime (R-HA-020); a login that straddles the rollout is retried once.
[operations-and-observability.md](operations-and-observability.md) Q-OPS-6 decided the same for
configuration documents. The development agent would revisit this if operators report frequent
group-rule changes that `match_regex` cannot express. Then only the group rules would move to the
database (option d), behind recent authentication.

**Applied in the spec:** R-AUTH-004 states that the whole provider entry is file-only, that no
API or configuration-as-code document creates or changes a provider, and that a change takes
effect with a rollout. R-AUTH-007 now names the provider keys in their YAML spelling
(`public_issuer`, `login_requires_groups`, `match_regex`) in its prose and acceptance criteria.

### Q-AUTH-2 · Session cookie name

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Keep `trv_session`, `trv_csrf`, `trv_oidc_binding` | The INV-8 names; trove sets `Secure`, `Path=/` and no `Domain` by convention | No change to INV-8 | A sibling subdomain or a plain-HTTP response can still plant or shadow a cookie of the same name (cookie tossing) |
| (b) `__Host-` for session and CSRF only | `__Host-trv_session` and `__Host-trv_csrf`; the binding cookie keeps `Path=/auth/oidc/` without a prefix | The browser enforces the flags of the two long-lived cookies | The login-CSRF defense of R-AUTH-005 still rests on a cookie a sibling subdomain can plant |
| (c) `__Host-` for all three | As (b), plus `__Host-trv_oidc_binding`, whose path widens to `/` because the prefix requires it | The browser enforces host-only, `Secure` and `Path=/` for every trove cookie, including the one that stops login CSRF | The binding cookie is sent on every request during its 10 minutes; INV-8 changes |

**Choice:** (c) — all three cookies carry the `__Host-` prefix: `__Host-trv_session`,
`__Host-trv_csrf` and `__Host-trv_oidc_binding` (`Path=/`).

**Rationale:** The prefix turns a convention into a browser-enforced guarantee
([draft-ietf-httpbis-rfc6265bis](https://datatracker.ietf.org/doc/draft-ietf-httpbis-rfc6265bis/),
cookie name prefixes). Without it, a host under the same parent domain can set
`Domain=<parent>` cookies with a longer path that shadow trove's. (b) protects the session but
leaves the login-CSRF defense open. The attacker starts a login in their own browser, logs in at
the IdP, and stops before the callback. They plant their `trv_oidc_binding` value in the victim's
browser from a sibling host and send the victim the callback URL, and the victim ends up logged in
as the attacker ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.7). Only (c) closes that.
Widening the binding cookie's path costs a few bytes per request for 10 minutes: the cookie is
`HttpOnly` and useless without its flow row. Renaming now, before any code exists, costs three strings; renaming
later would log out every user. The owner's `trv_` naming stays inside each name. The cookies
already carried `Secure`, so the prefix adds no new requirement on test setups. This changes
INV-8. The development agent would revisit it only if trove had to share a session across
subdomains, which the one-origin design (R-ARCH-007) rules out.

**Applied in the spec:** Overview table and diagram, R-AUTH-001 (selection and log redaction,
which now includes the binding cookie), R-AUTH-005 (binding cookie name and `Path=/`),
R-AUTH-006 step 3, R-AUTH-010 (cookie line, the prefix rule, a cookie-tossing acceptance test),
R-AUTH-012 (CSRF cookie and Angular configuration), R-AUTH-013 (clearing needs `Secure` and
`Path=/`, or the browser keeps the cookie), and R-AUTH-033 (browser row). INV-8 must be amended,
and the start-up cookie-prefix check of the optional content origin (R-ARCH-029) is now always
satisfied; see the cross-page consequences.

### Q-AUTH-3 · Refresh tokens or rotation

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Rotation only | Holders renew a token through R-AUTH-030 (rotate, optionally self-rotate) or by logging in again; no refresh grant | No fourth secret type and no prefix outside INV-8; serves package managers, which present static tokens anyway | A device-login tool keeps a long-lived PAT instead of a short access token plus a refresh token |
| (b) Refresh tokens for device-login clients | The device token endpoint also returns a refresh token; tools use the OAuth refresh grant | Short-lived access tokens for tools that can refresh | A new token type and prefix, refresh-token reuse detection ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.14.2), and only P1 device-login tools could use it |

**Choice:** (a) — trove has no refresh tokens; rotation (R-AUTH-030) and repeated login are the
renewal paths.

**Rationale:** None of the MVP clients can run a refresh grant. npm, pnpm, Yarn, dotnet and
nuget.exe present a static token from a configuration file. A refresh token would serve only
the P1 device-login tools of R-AUTH-036. Those tools rerun the device flow, or self-rotate when
they asked for it and the approver allowed it. A refresh token is optional in the token response
([RFC 6749](https://www.rfc-editor.org/rfc/rfc6749) §5.1), so omitting it stays standard. (b)
would add a fourth secret type with its own reuse detection for no current client. The door
stays open: a new type gets a new prefix (ADR-019). Rotation is safe as the only renewal path
only if it cannot mint more than creation could, so R-AUTH-030 now applies the creation rules to
it (Q-AUTH-7). The development agent would revisit this if a first-party command-line client
(TOK-035, P2) needs short access tokens with unattended renewal.

**Applied in the spec:** R-AUTH-030 states that no refresh tokens exist and no endpoint returns
a `refresh_token`. R-AUTH-036 states that the device token response never contains one, names
the issued token's lifetime, adds the optional `self_rotatable=true` request that the approver
may decline (stored in `cli_login.token_self_rotatable`), and gains an acceptance criterion for
both.

### Q-AUTH-4 · Missing group claim

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `clear` | Remove the provider's synced memberships, continue the login, write a warning event | Fails closed: a broken mapper removes rights, never keeps them; the user can still log in with direct grants and manage tokens | A user whose administrator group comes from the claim loses it until the mapper is fixed |
| (b) `keep` | Leave the memberships unchanged | Nobody loses rights through an IdP hiccup | Stale rights survive removals at the IdP for as long as the claim stays missing; fails open |
| (c) `deny` | Fail the login with `groups_claim_missing` | Loudest signal | A mapper misconfiguration locks everyone out of the UI, including the people who must fix it; IdPs that omit the claim for users without groups lock those users out for good |
| (d) `clear` for an absent claim, `deny` for an overage marker | An absent claim usually means "no groups"; an overage means "has groups, too many to list" | Users with many groups get an explicit error instead of an empty UI | Locks out exactly the users with many groups, often administrators and leads; two behaviors for one setting |

**Choice:** (a) — `on_missing_claim` defaults to `clear`, with the warning event
`user.groups.claim_missing` naming the reason; `keep` and `deny` remain per-provider settings.

**Rationale:** Group sync decides rights, and the safe direction for an unknown group set is
fewer rights. (b) fails open. (c) and (d) turn a mapper error into a lock-out of the very
people who have to fix it. IdPs also commonly omit an empty group claim instead of sending `[]`,
which (c) would treat as an error (not verified per IdP in this repo). (a) keeps everyone able to
log in with their direct grants and makes the cause visible in two places. Administrators see the
event with `absent`, `wrong_type` or `overage`, and the metric counts it. The affected user sees
the reason on the profile (`groupClaimMissing`), which matters most when that user is the
administrator who just lost the administrator group and can no longer read the audit log. That
gives the explicit signal (d) wants without its lock-out. An administrator who loses rights this
way recovers through break-glass (R-AUTH-014), which exists for exactly that. The development
agent would revisit the default if overage turns out to be the normal case for a supported IdP.
Then resolving that IdP's group overage would be specified, not a change of the default.

**Applied in the spec:** R-AUTH-008 makes `clear` the default and adds the warning event, the
metric, the `groupClaimMissing` field on the user's own record, and acceptance criteria (absent
claim, overage marker, field cleared by the next complete login). R-AUTH-017 shows the notice on
the profile. R-AUTH-041 adds `user.groups.claim_missing` and the metric.

### Q-AUTH-5 · Dormancy default

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) On, 90 days | Users without an interactive login for 90 days are disabled with reason `dormant`; their next OIDC login re-enables them | Bounds the offboarding gap without SCIM (H-16) to 90 days; self-healing for active people | A user who only uses npm with a long-lived PAT must sign in to the UI once per quarter |
| (b) Off | No job; offboarding without SCIM relies on administrators disabling users | No surprise 401 for npm-only users | A departed person's PAT lives up to the PAT maximum of 365 days unless someone remembers to disable them |
| (c) No job; cap the PAT maximum at 90 days instead | Tokens die on their own, and renewing them needs a session | No disable state | Every `.npmrc`, CI variable and NuGet source must be updated every 90 days, which is more churn than one sign-in; shorter caps also hit users who follow the process |

**Choice:** (a) — dormancy is on by default with 90 days (P1, with R-AUTH-021).

**Rationale:** trove's identity truth is the IdP, and without SCIM a person deactivated there
keeps working tokens. Failing to offboard is the costlier error. 90 days matches the default
PAT lifetime (R-AUTH-026), and creating a token needs a session, so users who follow the
default cadence sign in anyway. Only holders of long-lived PATs who never open the UI hit the
rule, and one sign-in fixes it. The 401 says so where the client shows it: npm prints the
`error` text of R-NPM-005, while NuGet clients show only the status, so their users learn it in
the UI. SCIM users, service accounts and CI tokens are exempt, so automation is never disabled by
it. (b) leaves the gap of H-16 open for a year per token. (c) moves the cost from one sign-in to
rewriting every client configuration. Dormancy is P1 like SCIM, so in the MVP the gap is closed
only by an administrator disabling the user; H-16 says so. The development agent would revisit
this when SCIM (P1) is in wide use. Dormancy then stays on as a backstop for users that SCIM does
not manage.

**Applied in the spec:** R-AUTH-021 states the default, its alignment with the PAT default, and
the hint in the 401 text, per endpoint family (problem `detail` on `/api/v1`, `error` and
`message` on npm endpoints). The configuration comment no longer calls the matter open. H-16
states that dormancy is P1.

### Q-AUTH-6 · Unauthenticated revoke-by-value

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Open to anyone | `POST /api/v1/tokens/revoke` takes a token value without authentication, rate-limited, and answers 202 whether or not the token existed | Anyone who finds a leaked token, including a secret-scanning service, kills it at once | An unauthenticated endpoint to rate-limit; with the rotation cascade, a stale predecessor value can revoke a live successor |
| (b) Administrators only | Only administrators revoke by value | Matches the catalog | A leaked token lives until an administrator acts; finders outside the organization cannot help |
| (c) Any authenticated principal | Any user, service account or CI token may revoke by value | No anonymous endpoint | External finders and scanning services have no account; no security gain, because revoking needs the value anyway |
| (d) Open, with a time-limited forward cascade | As (a), but the cascade to rotation successors runs only if the presented token was rotated less than a configurable time ago (for example 24 h) | A stale value found years later cannot end a live chain | A thief who rotated the stolen token long before the leak is found keeps the chain alive, which is the case the cascade exists for; one more setting |

**Choice:** (a) — revocation by value is open to unauthenticated callers, rate-limited, without
an existence oracle, and its forward cascade has no time limit.

**Rationale:** The endpoint adds exactly one capability beyond possession of a live token.
Because a by-value revocation follows the rotation chain forward (R-AUTH-029), the holder of a
stale, already rotated value can revoke its live successor. That is deliberate: trove cannot
tell a holder's rotation from a thief's, and a leaked predecessor must end the chain. The
cascade must therefore also run for already revoked or expired predecessors, which R-AUTH-029
states. (d) limits that side effect, but it fails in the case the cascade exists for: a leak
found late, after the thief has rotated. The legitimate holder of a chain ended this way loses
nothing they could keep safely, and creates a new token. R-AUTH-030 now says that rotation is
renewal and never the response to a leak. Apart from that, the endpoint reveals nothing (202 for
every well-formed value) and needs the secret. Flooding is bounded by the checksum filter and
the per-address limit (R-AUTH-038). (b) leaves leaked tokens alive for hours or days, and (c)
excludes exactly the finders who are not users. The development agent would revisit this if the
endpoint is abused for denial of service against rotated tokens. Then (d) would be the fallback,
and the endpoint would stay open.

**Applied in the spec:** R-AUTH-029 restates the reason for the open endpoint, makes the forward
cascade explicit for already revoked or expired predecessors, and adds an acceptance criterion
for it. R-AUTH-030 states that a leaked token is revoked and replaced, not rotated.

### Q-AUTH-7 · Administrators issuing tokens for users

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) No | A PAT is always created by its owner; automation uses service accounts (R-AUTH-020) | Audit attribution stays honest: every action under a PAT was enabled by that person; no impersonation path | A migration that wants per-user tokens needs each user to create one |
| (b) Yes, audited | Administrators create PATs owned by other users | Convenient for migrations and onboarding | It is impersonation: the token acts with the user's group rights and shows the user as actor, while the user never saw the secret |
| (c) Prepared by administrators, claimed by the user | An administrator defines description, scope and lifetime; the secret is minted only when the user claims it in their own session with recent authentication | Administrators can hand out a vetted scope during onboarding; only the user ever sees the secret | Saves the user only the choice of scope and lifetime, which the token dialog already offers; a second table and workflow for little gain |

**Choice:** (a) — no path gives anyone but the owner a secret for a PAT: administrators can neither
create PATs for other users (R-AUTH-027 answers 403 `forbidden`) nor rotate them (R-AUTH-030).

**Rationale:** A PAT acts with its owner's rights at use time, so an administrator-minted PAT
lets the administrator act as that person in every audit record. That breaks the attribution
INV-16 relies on. Rotation returns a fresh secret too, so letting administrators or holders of
`tokens:revoke` rotate another user's PAT, as the earlier rule "anyone who may revoke may rotate"
did, was the same impersonation through a side door. A narrow `tokens:revoke` token could even
have been turned into an administrator's full-power token that way. R-AUTH-030 now applies the
creation rules to rotation. The migration case does not need (b): tokens from another system
cannot be imported anyway, because they do not match the `trv_` format. Automation gets service
accounts with their own grants, and from P1 named owners. Users get their own tokens through the
UI or npm login. (c) is safe but duplicates the token dialog. The development agent would revisit
this, and build (c), if onboarding with administrator-chosen scopes becomes a real need.

**Applied in the spec:** R-AUTH-027 names the `owner` field case in its refusal table and adds an
acceptance criterion for an administrator's attempt. R-AUTH-030 replaces "anyone who may revoke
the token" with per-type callers: a PAT only by its owner, a SAT by administrators, owners (P1) or
a SAT of the account, token-authenticated rotation under the derivation rule. It adds 403
`forbidden` for administrators and `tokens:revoke` holders on another user's PAT, and two
acceptance criteria.

### Q-AUTH-8 · Address binding of the npm web-login poll

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exact address, on | A poll must come from exactly `requested_addr` | Strongest binding | Every multi-egress network, including address changes within one provider block, loses web login |
| (b) No binding | The first poll after approval receives the token, from anywhere | Web login works on every network | H-20 fully live: any real-time reader of a proxy or ingress log anywhere can collect the PAT |
| (c) Exact address plus operator-listed egress ranges | As (a), but addresses in one listed CIDR count as one | Fixes known pools without weakening the default | Every affected operator must find and list their pools before web login works |
| (d) Network binding plus operator-listed ranges | A poll matches when it comes from the same IPv4 `/24` or IPv6 `/56` as the start, or from the same listed range; `exact` and `off` stay selectable; refused polls are shown on the approval page and audited | Fixes the common multi-egress case out of the box; keeps outside log readers out; refusals become visible to the user and to operators | A log reader inside the same `/24` or `/56` is not stopped; address-family changes and non-contiguous pools still need a range or fall back |
| (e) Web login off by default | `npm_web_login: false` by default; `npm login` falls back to the legacy prompt, where the user enters a PAT created in the UI (R-AUTH-035) | No poll secret in any URL, so H-20 is not live at all; no binding code in the default path | Every npm user copies a token from the UI into a terminal prompt; the browser hand-off of PKG-NPM-008 is gone by default; a UI token defaults to 90 days instead of the 30 days of `cli_login.token_ttl`; the binding question returns as soon as an operator switches web login on |

**Choice:** (d) — `poll_binding.mode: network` by default (`ipv4_prefix: 24`,
`ipv6_prefix: 56`, `equivalent_ranges: []`), with refused polls shown on the approval page,
audited and counted, and a settings warning when the client address cannot be trusted.

**Rationale:** The page previously specified (a) as `bind_poll_address: true`. Behind NAT or
egress proxies with several egress addresses, the poll was refused, and npm treated that 404 as
"web login unsupported". It fell back to the legacy prompt, which only works with a PAT copied
from the UI. The attacker of H-20 reads proxy or ingress logs. If that reader shares the user's
egress, no binding mode stops them, exact match included; H-20 already says so. A reader outside
the user's network, for example through a breached log pipeline, would additionally need an
address in the user's `/24` or `/56` while the 10-minute flow is open. That is possible for a
neighbour in the same ISP or cloud block, which H-20 records, but not for log readers in general.
Network binding therefore keeps almost all of the protection of (a). It also accepts the address
changes that egress proxy farms and NAT pools produce within one allocation, and IPv6 privacy
addresses within one site. (b) gives up the protection although the remaining cost of keeping it
is a prefix comparison; weakening a control to save effort is not a trade trove makes. (c) is
secure but makes web login fail by default exactly where it is most wanted, corporate networks.
(e) removes the risk by removing the feature. It replaces a residual that needs a log reader
inside the user's network within ten minutes with a manual copy step for every user and
longer-lived tokens. It stays one switch away (`npm_web_login: false`) for operators whose access
logs are widely readable.

Every mode depends on the client address of R-ARCH-006. Behind an edge proxy with
`server.trusted_proxies` empty, all polls carry the proxy's address, and the binding can no
longer tell the user's network from anyone else's. trove cannot detect that state, so
R-AUTH-040 warns about it instead of failing. The refused-poll notice turns a silent fallback
into an explanation for the user and a warning when someone else polls, and the metric tells
operators whether they need a range.

Not verified, and this is the gap: how often real egress pools are contiguous within a `/24` or
`/56`, and how Yarn's `--web-login` reports a refused poll (npm falls back to the prompt, per
npm-profile). `equivalent_ranges` covers pools that are not contiguous, and the metric will show
how often that happens. The development agent would revisit the default if the refusal metric
shows frequent refusals in the field, or if npm adds a binding mechanism to web login that does
not rely on addresses.

**Applied in the spec:** R-AUTH-034 replaces `bind_poll_address` by `poll_binding` (`mode`,
`ipv4_prefix`, `ipv6_prefix`, `equivalent_ranges`) with a match table and startup validation. It
adds the refused-poll notice to the approval page and three columns to `cli_login`, limits the
binding to npm logins, states the dependency on `server.trusted_proxies`, and rewrites the
acceptance criteria, including one for the trusted-proxy warning. R-AUTH-040 warns when the mode
is `off`, and when it is not `off` while `server.trusted_proxies` is empty. R-AUTH-041 adds
`cli_login.poll_refused` and the metric. H-20 describes the residual risk of network binding and
of an untrusted client address.

### Q-AUTH-9 · Shared Valkey cache for session and token lookups

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

INV-3 lets Valkey hold caches shared across replicas. Each `web` replica fills its own session,
token and principal caches from PostgreSQL (R-AUTH-023). With n replicas and random distribution,
one credential in continuous use is therefore looked up by up to n replicas per `cache_ttl`; a
second level in Valkey would bring that down to about one.
[architecture.md](architecture.md) Q-ARCH-16 decided that no positive authentication result is
read from Valkey. This question checks that rule against its cost on this page.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) In-process only | Each replica caches; Valkey carries only the change messages of area `auth` | Valkey can never authenticate anyone; a cache hit needs no network round trip; nothing to distribute | Lookups grow with replicas × credentials in use: 20 replicas and 1,000 credentials in continuous use cost at most about 1,300 indexed lookups per second |
| (b) Valkey second level, plain entries | Replicas read validation results from Valkey before PostgreSQL | About one lookup per credential and `cache_ttl` in the cluster | Whoever can write under the prefix can store an entry for a secret hash of their choosing and then authenticate with that secret as any principal. Violates Q-ARCH-16. |
| (c) Valkey second level, sealed entries | Entries carry an HMAC under a key Valkey never sees | Forged entries are rejected | A writer can still put back an older sealed entry of a token revoked since. Binding each entry to the current epoch of area `auth` prevents that, but then every revocation, including each CI job's closing self-revocation, invalidates every entry. One more key to distribute and rotate; a miss costs a Valkey round trip and then still a PostgreSQL query. |
| (d) Sessions in Valkey | Valkey is the session store; PostgreSQL keeps no session rows | No session writes on PostgreSQL | A failover with asynchronous replication loses sessions or brings back ended ones. INV-3 forbids Valkey as the only copy of state whose loss costs correctness. The session list of R-AUTH-010 and the restore rule of [backup-and-restore.md](backup-and-restore.md) R-BAK-022 lose their source. |

**Choice:** (a) — sessions, tokens, principals and runtime settings are cached in each replica's
memory only; Valkey carries their change messages and nothing else.

**Rationale:**
- (b) hands authentication to anyone who can write to Valkey, and (d) makes a security decision
  depend on asynchronous replication. Both conflict with INV-3 and Q-ARCH-16.
- (c) closes forgery but not replay unless each entry is bound to the `auth` epoch, and with that
  binding every revocation empties the shared level. What it saves is small: a lookup by a unique
  32-byte key on an indexed column, through the main pool and the pooler
  ([architecture.md](architecture.md) R-ARCH-019).
- The cost of (a) grows with replicas × credentials in use, not with requests. The reference budget
  already counts 0 queries per request while caches are valid
  ([architecture.md](architecture.md) R-ARCH-027).
- Revisit if profiling at the reference load shows credential lookups above 10 % of PostgreSQL
  CPU. The first step is a longer `cache_ttl`, up to its 60 s ceiling, which costs no revocation
  latency since Q-AUTH-10; (c) with epoch binding is the second.

**Applied in the spec:** R-AUTH-023 names the four in-process caches and states that no entry is
read from or written to Valkey; R-AUTH-010 refers to it for sessions; R-AUTH-042 lists the state;
the security notes state that Valkey is outside the authentication trust path.

### Q-AUTH-10 · Revocation bound when a replica cannot read the change log

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Until 2026-10-28, R-AUTH-023 promised 2 s while `LISTEN/NOTIFY` delivered and `cache_ttl` (15 s)
otherwise. Since [architecture.md](architecture.md) Q-ARCH-15, changes reach replicas through a
change log in PostgreSQL that every replica polls every second, with Valkey as a fast path that
can only evict early. Valkey's state therefore no longer affects the bound. What remains is a
replica that cannot poll, because PostgreSQL is unreachable from it or the poll has stalled.
Readiness removes such a replica only after `cluster.epoch_max_staleness`, which must be at least
the PostgreSQL readiness window of about 15 s ([ha-and-helm.md](ha-and-helm.md) R-HA-008).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Serve cache hits until `cache_ttl` | The earlier fallback | Cached credentials keep working through short PostgreSQL trouble | A token revoked through another replica stays usable on a cut-off replica for up to 15 s; the promise depends on the failure mode |
| (b) Fail closed at the revocation bound | A replica answers from its auth caches only while its last completed change-log poll started less than `auth.revocation_bound` (2 s) ago; otherwise it asks PostgreSQL, and a failed lookup gives 503 | One bound in every failure mode; the same principle as authorization's fail-closed rule ([authorization.md](authorization.md) R-AUTHZ-028) | During a PostgreSQL switchover, requests that would have been cache hits wait in the retry loop or get 503 |
| (c) Rely on readiness | Serve until the `invalidation` check removes the replica | No extra code | A gap of at least 15 s, and an edge proxy that still routes to the replica keeps getting answers |
| (d) Revocation hints in Valkey | Every hit checks a deny-list in Valkey | Fast | A Valkey outage or flush removes the hints, so the bound depends on Valkey again (INV-3); one round trip per request |

**Choice:** (b) — the auth caches answer only while the replica's view of the change log is younger
than `auth.revocation_bound` (2 s by default); otherwise the lookup goes to PostgreSQL, and a
failed lookup gives 503, never a cached acceptance.

**Rationale:**
- Revocation is the promise incident runbooks rely on. (b) is the only option under which it holds
  in every failure mode: Valkey down, failed over, flushed, partitioned from one replica or
  forging messages, and a replica cut off from PostgreSQL.
- The availability cost is small. Almost every request needs PostgreSQL anyway, for the item or
  package lookup ([architecture.md](architecture.md) R-ARCH-027), so a replica that cannot reach
  it does little useful work with a valid cache. Switchovers shorter than the retry budget are
  ridden out by the retry loop for the credential lookup as for the item query
  ([ha-and-helm.md](ha-and-helm.md) R-HA-009).
- The load cost is bounded. When a slow PostgreSQL delays the poll, the gate can trip on every
  replica at once. Concurrent lookups of one credential then share one query that is younger than
  the bound (R-AUTH-023), so the extra load grows with replicas × credentials in use, not with the
  request rate.
- The check needs no clock comparison. A poll reads every change committed before it started
  ([READ COMMITTED](https://www.postgresql.org/docs/current/transaction-iso.html#XACT-READ-COMMITTED)),
  and the replica measures the poll's age on its own monotonic clock.
- (c) leaves a gap of 15 s or more; (d) puts correctness into Valkey.
- Revisit if the gate trips in normal operation, that is if
  `trove_auth_cache_bypassed_total{reason="changes_stale"}` rises without a database incident.
  The poll's latency, not the bound, is then the problem to fix.

**Applied in the spec:** R-AUTH-023 (freshness gate, stated as stricter than the `invalidation`
readiness check at `cluster.epoch_max_staleness`; lookups of one credential shared while the
gate is closed, so a gate that trips everywhere does not multiply PostgreSQL's load; the bound in
every failure mode; the key `auth.revocation_bound` with its start-up check; acceptance criteria with Valkey stopped, the
Pub/Sub connection killed, forged messages, a failover, and PostgreSQL cut off); R-AUTH-010
(sessions follow the gate); R-AUTH-015, R-AUTH-018, R-AUTH-029, R-AUTH-037 and R-AUTH-040 name the
bound; the security notes. Cross-page: [architecture.md](architecture.md) R-ARCH-018 provides
`cluster.ChangesFreshWithin(d)`.

### Q-AUTH-11 · Brute-force counters and the failed-credential throttle across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-038 counted rejected credentials and flow starts per replica in memory, and accepted that
"the effective cluster-wide limit is this value multiplied by the number of replicas". It exempted
credentials found in the replica's own positive cache. With the load spread at random over a
replica count that autoscaling changes, both broke. The limit moved with the replica count, and
whether a colleague behind a throttled NAT got 429 depended on the replica a request reached
(INV-18). [architecture.md](architecture.md) Q-ARCH-17 decided cluster-scoped counters in Valkey,
with per-replica shares as the fallback, for every class that limits callers.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Per replica, in memory | The earlier rule | No round trip | The budget is multiplied by the replica count; a guesser gets a fresh throttle per replica; the exemption depends on the replica |
| (b) Cluster-wide counters in Valkey, exemption by the local cache only | Limiters of R-ARCH-034 | One budget on every replica | A colleague's token cached on one replica gets 429 on the others, so the answer still depends on the replica |
| (c) As (b), plus a Valkey marker for credentials validated on any replica in the last 10 minutes; the break-glass lockout stays in PostgreSQL | A successful validation from PostgreSQL sets a short-lived marker keyed by a hash of the secret hash; a throttled address may still validate a cached or marked credential | The same answer on every replica; a marker never authenticates, because validation still reads PostgreSQL or the own cache; Valkey round trips only on cache misses | One small Valkey key per credential in use; markers lost in a failover cost colleagues 429s, and only while an attack throttles their address |
| (d) (c) with the break-glass lockout in Valkey too | One store for every counter | Simpler | A failover or flush resets the count and gives a guesser a fresh threshold against the one password; INV-3 keeps state whose loss costs correctness in PostgreSQL |
| (e) Every counter in PostgreSQL | Counter rows | Durable | A write per rejected credential on the system of record, during exactly the flood the throttle exists to absorb |

**Choice:** (c) — the failed-credential throttle, the flow-start limit and the wrong-device-code
limit count cluster-wide in Valkey through R-ARCH-034, with per-replica shares while Valkey is
unavailable. Credentials validated on any replica within `recent_token_window` stay exempt through
a Valkey marker. The break-glass lockout stays in PostgreSQL, counted atomically and never cached.

**Rationale:**
- A limit that changes with the replica count is not a limit an operator can set, and a throttle
  whose exemption depends on the replica gives two answers to one request (INV-18). (c) fixes both.
- The marker stays inside Q-ARCH-16's trust line. It can lift the throttle for one credential and
  so cause one PostgreSQL lookup; it never accepts a credential. A writer under the prefix gains
  nothing by forging markers that deleting the counter would not give them as well
  ([architecture.md](architecture.md) H-49).
- The lockout guards a password that a person chose, and its count must survive Valkey's loss;
  (d) would weaken it for a little simplicity. (e) moves the flood onto the database.
- The wrong-device-code limit may live in Valkey, unlike the lockout. It guards a code of 20^8
  values that lives for 10 minutes and is typed by a signed-in user. A failover that resets the
  count gives that user five more guesses, which does not matter at that size.
- The throttle is consulted only on cache misses, so legitimate traffic, which hits the caches,
  pays no Valkey round trip ([architecture.md](architecture.md) R-ARCH-027).
- Revisit if markers show up in Valkey's memory (one key of under 100 bytes per credential in use)
  or if the miss path's round trip shows up in P-2.

**Applied in the spec:** R-AUTH-038 (the limiter table, each limiter's burst and window, cluster
scope and fallback, the marker, the local login under the per-address limit, the atomic and
uncached lockout, the configuration comments, acceptance criteria with and without Valkey); R-AUTH-036 (wrong user codes counted per
user, cluster-wide); R-AUTH-023 step 2. Cross-page: [architecture.md](architecture.md) R-ARCH-034
(a count-only form for limits that count failures), R-ARCH-030 (the marker key), H-49 and
R-ARCH-027.

### Q-AUTH-12 · Where multi-request login flows keep their state

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

Three flows span several requests: the browser login (start, IdP, callback), the npm web login
(start, approval, polls) and the device login (authorize, approval, polls). Under random
distribution each step may reach another replica, and INV-18 allows PostgreSQL or Valkey for the
state such a protocol keeps.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) PostgreSQL rows | `oidc_login_flow` and `cli_login`; every poll reads the row; poll pacing and refused polls are columns of the row | Unaffected by Valkey's loss; conditional updates make every step single-use across replicas; the approval, which mints a credential, is recorded where audit and restore see it | One read per poll (every 2 s for npm, 5 s for the device flow) and a cleanup task |
| (b) Valkey keys that expire with the flow | Flow state as keys with a TTL | No cleanup task; poll reads leave PostgreSQL | The browser flow holds the PKCE verifier in clear and the command-line flows hold the approval that mints a PAT: a writer under the prefix could approve a pending login for an account of its choosing. A failover loses flows in progress. Valkey would decide access ([architecture.md](architecture.md) Q-ARCH-16). |
| (c) Rows in PostgreSQL, poll pacing and refusal counts in Valkey | A hybrid | A few writes less | Two stores for one flow, for the gain of one small write per poll |
| (d) Session affinity on `/auth/` | The edge routes a flow's requests to one replica, the assumption behind AUTHN-011 | No shared state | INV-12 forbids affinity, and a drain would break every flow pinned to the drained replica |

**Choice:** (a) — every multi-request login flow keeps its state in PostgreSQL, changes it with
conditional statements, and keeps nothing in a replica's memory or in Valkey.

**Rationale:**
- The flows carry secrets and decisions that mint credentials, so their state belongs where Valkey
  cannot change it (Q-ARCH-16), and where a Valkey failover cannot lose it (INV-3).
- The load is small. 100 logins pending at the same time cost at most 50 poll reads per second, each
  a lookup by a unique hash.
- Conditional statements make concurrent steps on different replicas safe: the callback consumes
  its flow once, and an approval mints one token.
- (d) is ruled out by INV-12 and by the drain requirement.
- Revisit if poll reads ever show up in PostgreSQL's load; (c) would then move the pacing column.

**Applied in the spec:** R-AUTH-005 (no state outside PostgreSQL; the `scatter` acceptance
criterion); R-AUTH-006 (the flow delete commits before the outbound calls, user and session in
one transaction after them); R-AUTH-034 (conditional consume; two concurrent polls mint one
token); R-AUTH-036 (`last_polled_at` makes `slow_down` work across replicas); R-AUTH-042; the
`session-cleanup` task of R-AUTH-043.

### Q-AUTH-13 · Idle-timeout tracking across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-010 wrote `last_seen_at` at most once per 60 s per session and checked the idle timeout
against the session cache of the replica a request reached. With n replicas, each with its own
view, a replica that had not seen a session for an hour could end it as idle while the user
worked through the others.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Conditional touch; deadline decided from the row | A replica sends a conditional `UPDATE` of `last_seen_at` when a request arrives 60 s or more after the newest `last_seen_at` it has read; a cached entry whose idle deadline has passed is re-read before the session ends | At most one row change per session and minute whatever the replica count; every replica ends a session at the same moment | A session can end up to 60 s before its idle timeout after the last request |
| (b) Write on every request | `last_seen_at` on every authenticated UI request | Exact | One write per UI request on the system of record |
| (c) Sliding expiry key in Valkey | `SET … PX <idle_timeout>` per request decides idleness | Exact and cheap | A writer under the prefix could keep a stolen session alive, and a flush either ends every session or switches idle expiry off; session lifetime would depend on Valkey (Q-ARCH-16, INV-3) |
| (d) Each replica's own view | The earlier behaviour | No extra reads | Answers differ by replica (INV-18) |

**Choice:** (a) — `last_seen_at` is touched conditionally at most once per minute and session, and
idle expiry is decided from the row, never from one replica's view.

**Rationale:**
- (a) gives every replica the same answer at the cost of one write per active session and minute.
  (b) multiplies writes by the request rate, (c) hands a security property to Valkey, and (d) breaks
  INV-18.
- A 60 s imprecision on a timeout of an hour is below what a user notices. It never lengthens a
  session beyond `idle_timeout`, so the timeout stays an upper bound an operator can rely on; it
  can shorten a session by at most 60 s.
- The replica decides when to touch from the newest `last_seen_at` it has read, not from the time
  of its own last touch. That value is a lower bound of the row, so no touch the row needs is
  skipped; deciding from the own last touch would let the row fall up to 120 s behind and end
  sessions that early. The value only saves statements and never decides that a session has
  ended, so it is not state a later request depends on.
- Revisit if an installation needs idle timeouts below 5 minutes; the touch interval would then
  become a fraction of `idle_timeout`.

**Applied in the spec:** R-AUTH-010 (deadlines decided from the row, the conditional touch that
returns the row's value, the end window of `idle_timeout` − 60 s to `idle_timeout`, the principal
row lock for `max_per_user`, acceptance criteria across replicas); R-AUTH-011 (both deadlines from
the row); R-AUTH-041 (`trove_auth_session_touch_failures_total`); R-AUTH-042 (the kept value as
per-replica state).

### Q-AUTH-14 · Break-glass password rotation across replicas

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

A rotated break-glass Secret reaches each node on that node's kubelet schedule, within the 2 minutes
of [ha-and-helm.md](ha-and-helm.md) R-HA-030, so replicas hold the old and the new password side by
side for a while. Ending the sessions that the old password opened must therefore not depend on one
replica staying alive: a node drain can remove any replica at any time (INV-12).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Each replica ends the sessions when it notices the change | Every replica that sees its file change ends every break-glass session created before that moment | No extra state | A session that a replica still holding the old file opens is ended only if that replica, or another still-old one, later notices the change; a drain that removes it first leaves the session alive until its absolute timeout (1 h by default), and the old password keeps working on lagging replicas until each switches |
| (b) Rotation hold recorded in PostgreSQL | The first replica that notices the change records `password_changed_at` in the lockout row and ends every earlier break-glass session in the same transaction; every replica refuses the local login for `rotation_hold` (2 min) | The end is one committed transaction, independent of the noticing replica; no replica, old file or new, opens a session while replicas disagree; the check rides on the lockout row every attempt reads anyway | Break-glass login is unavailable for 2 minutes after a rotation; a kubelet slower than the hold, or a rollout that replaces every pod before any notices, escapes it (H-52) |
| (c) Verifier fingerprint in PostgreSQL | Each session records a fingerprint of the password file it was opened with; replicas accept only sessions with the newest fingerprint | Also catches a rotation that no running replica notices | A fingerprint of a plaintext password file is an offline-guessable verifier in the database unless it is keyed; keying it with the stored-secret key ring makes it change on key rotation and differ between replicas during step 2 of that rotation ([architecture.md](architecture.md) R-ARCH-021). "The password is never stored in PostgreSQL" would hold only in letter. |
| (d) No automatic end | The operator runs `trove admin break-glass end-sessions` after rotating | Nothing to build | A manual step forgotten in exactly the incident in which the rotation matters |

**Choice:** (b) — the first replica that notices a changed break-glass password opens a rotation
hold in PostgreSQL and ends every earlier break-glass session in the same transaction; every
replica refuses the local login with 503 `break_glass_rotating` until the hold ends.

**Rationale:**
- (a) depends on the replicas that held the old file surviving until they notice, which a drain
  does not guarantee. (b) makes the end one transaction, so it holds whichever replica disappears.
- The hold closes the window in which a replica still holding the old file accepts the old
  password, for as long as the kubelet keeps the 2 minutes of R-HA-030. Refusing the new password
  too keeps the rule simple and costs a 2-minute wait on a rare operator action; `Retry-After`
  says how long.
- (c) would catch more, but it puts a password-derived verifier into the database, which
  R-AUTH-014 keeps free of the password, or ties it to the key ring's own rotation. (d) is the fallback the operator keeps for
  H-52.
- Revisit if the chart starts rolling the pods on Secret changes, which makes the case of H-52 the
  normal one; then (c) with a fingerprint keyed by a dedicated key, not the key ring, would be
  weighed.

**Applied in the spec:** R-AUTH-014 (rotation coordinated in PostgreSQL, the 503 during the hold,
`rotation_hold`, acceptance criteria with the noticing replica force-deleted; also the per-request
rejection of break-glass sessions when the account is disabled, and the Argon2id verification
slots); R-AUTH-015 (`status` shows the hold); R-AUTH-038 (`password_changed_at` in
`auth_lockout`); R-AUTH-042; H-52. Cross-page: [architecture.md](architecture.md) R-ARCH-030
lists the rotation hold with the break-glass lockout in PostgreSQL.

### Q-AUTH-15 · Token lineage pointers without a foreign key

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`api_token` names two other tokens: `rotated_from` (the predecessor of a rotation, R-AUTH-030) and
`derived_from` (the parent of a token created under the derivation rule, R-AUTH-027). The sketch in
R-AUTH-023 declared both as `REFERENCES api_token (id) ON DELETE SET NULL`. The table registry of
[backup-and-restore.md](backup-and-restore.md) R-BAK-004 allows one cycle-breaking column per table,
and a self-referencing foreign key is a cycle.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Foreign key on one column, plain `uuid` on the other | One `CycleBreak` column, the other lineage pointer has no integrity | Integrity for one pointer | Two pointers of one kind treated differently for no reason; a restore must fill one column afterwards |
| (b) Both are plain `uuid` columns (chosen) | A pointer may name a row that the retention task has deleted | No restore ordering, no cycle; the lineage cascades of R-AUTH-029 read the pointers with a partial index and never need the parent row to exist | A pointer to a deleted token stays instead of turning `NULL` |
| (c) Extend the registry to several cycle-breaking columns | `CycleBreak` names a list | Keeps both foreign keys | A change of R-BAK-004 and of the restore algorithm for two pointers that need no integrity |

**Choice:** (b).

**Rationale:** `token-cleanup` deletes rows after `retention_after_end`, oldest first, so a lineage
pointer to a deleted parent is the normal end state of a long chain, and `SET NULL` would only erase
the evidence that there was a parent. The cascades of revocation walk from a parent to its children by
the pointer, never from a child to a parent that must exist. The development agent would revisit it if
a requirement needs to read a deleted parent's fields through the child.

**Applied in the spec:** R-AUTH-023 data model (`rotated_from`, `derived_from` are plain `uuid`);
migration `00007_identity.sql` indexes both columns where they are set.

### Q-AUTH-16 · Principal integrity enforced in the database

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-016 to R-AUTH-020 and R-AUTHZ-010 state rules that a foreign key cannot express: the
built-in principals `anonymous` and `authenticated` exist once and cannot be deleted or renamed;
only users and service accounts are group members; only users hold sessions; a role is bound to a
user, a group or a service account only; each name follows the rule of its kind.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Application checks only | Every writer validates | Nothing in the schema | `trove admin`, journal replay, SCIM and the API are separate writers; one forgotten check is a built-in principal deleted or a role bound to `authenticated` |
| (b) `CHECK` per kind and guard triggers (chosen) | `CHECK` on `principal.name` by kind, a unique index on the built-in kinds, a trigger that refuses changes to the two built-in rows, and one generic trigger `principal_kind_guard` that names the kinds a principal column may reference (SQLSTATE `AI001`) | Every writer, present and future, meets the same rules; the cost is one primary-key lookup per insert of a membership, a token, a session or a binding | The rules are in two places (API validation answers with the specified 4xx before it gets there) |
| (c) A `kind` column in every subtype table with a composite foreign key | Declarative | No triggers | Changes the sketched tables of R-AUTH-016 to R-AUTH-020, and PostgreSQL does not allow the generated column the pattern needs |

**Choice:** (b). The built-in principals have the fixed identifiers
`00000000-0000-0000-0000-000000000001` (`anonymous`) and `00000000-0000-0000-0000-000000000002`
(`authenticated`), seeded by the migration, so code and the revocation journal name them without a
lookup.

**Rationale:** these are authorization-relevant invariants; a violation is a privilege error, not a
cosmetic one. The triggers fire on row writes only: the `TRUNCATE` of a restore (R-BAK-021) is not
blocked, and the seeded built-in rows are loaded back from the backup.

**Applied in the spec:** R-AUTH-016 data model (a paragraph under the sketch: name rule per kind, the
built-in rows with their identifiers, the guard triggers).

### Q-AUTH-17 · The command-line login table ships with the login state

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`cli_login` belongs to the P1 command-line logins (R-AUTH-034, R-AUTH-036). Two MVP rules read it
before those requirements exist: `max_pending_flows` counts the unexpired rows of `oidc_login_flow`
and `cli_login` together (R-AUTH-038), and `session-cleanup` deletes the expired rows of both
(R-AUTH-043). R-AUTH-015 and R-BAK-004 already assign the table its export class `transient`.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Create it with the P1 work | The MVP count and the cleanup have no table to read | Nothing unused in M1 | The MVP queries need a special case for a table that does not exist, or a migration in the middle of the P1 work changes the pending-flow count |
| (b) Create it with the login state of M1 (chosen) | The table exists empty until R-AUTH-034 ships | The count and the cleanup are written once; the sketch of the specification is the schema | An empty table in M1 |

**Choice:** (b), in `00008_login.sql`, with the columns of R-AUTH-034 unchanged.

**Applied in the spec:** none; the sketch of R-AUTH-034 is the schema, created by `00008_login.sql`.

### Q-AUTH-18 · The journal entry of `settings.auth.changed`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-041 flags `settings.auth.changed` for the revocation journal "only when the change sets
`anonymousAccess`". The registry rule of R-OPS-014 makes an entry of a type that one page owns alone
`required`: an event written without an entry fails its transaction. The two sentences do not fit
together for a change of a token lifetime.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) `journal_entry: optional` | The producer supplies an entry only for a change of `anonymousAccess` | Matches the sentence of R-AUTH-041 | R-OPS-014 allows `optional` only for the shared types `package.published` and `package.deleted`; a producer bug could drop a narrowing without failing |
| (b) Always supply the entry (chosen) | Every change carries `anonymousAccess` before and after; the type is `required` | One rule for all producers; the replay comparator treats an unchanged value as a no-op | A lifetime-only change writes a journal row that replay skips |

**Choice:** (b).

**Rationale:** the comparator `narrows(current, after)` already returns true when the value is
unchanged, so replay does nothing for such an entry. A journal row per settings change costs nothing
at the rate administrators change settings, and a type that cannot be written without its entry
cannot lose a narrowing silently. Revisit if a second producer of `settings.auth.changed` appears.

**Applied in the spec:** none beyond this block; `internal/audit/events_identity.go` registers the
type as `required` and `token.SettingsStore.Update` always supplies the entry.

### Q-AUTH-19 · Which unauthenticated requests the stage lets through

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-001 item 4 makes a request without a credential anonymous "if R-AUTH-037 allows it", and
R-AUTH-037 lists the endpoints that are always reachable, "whatever api-and-ui.md declares public".
The management API declares each operation as public, authenticated or permission-bound
(R-API-008) and checks it per route; the stage runs before the route is known to the handlers.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The stage answers 401 whenever the anonymous switches are off | Simplest reading of item 4 | One rule | `GET /api/v1/system/ping` and the OpenAPI document, declared public, are unreachable with the default settings |
| (b) The stage lets every management request through as anonymous | The per-operation check decides | Public operations work | The switch no longer applies to the repository reads under `/api/v1/` that the files area adds; the stage reports an anonymous principal where R-AUTH-037 says "off" |
| (c) The stage asks which operation the request matches (chosen) | `EndpointRules.PublicRoute` is filled from the OpenAPI document: an operation declared `public`, or public by a switch, is reachable without a credential; the fixed list of R-AUTH-037 (`/auth/`, login options, device endpoints, revocation by value, the CI exchange, the npm login start) is reachable as well; every other request follows the switches | The switch means what R-AUTH-037 says; public operations work whatever the switch says | The stage depends on the route table through the composition root |

**Choice:** (c). Anonymous never passes an unsafe method on `/api/v1/` unless the route is in the
fixed list (the hard limit of R-AUTH-037). A request to an exchange route
(`POST /api/v1/auth/oidc/token` and the two npm exchange routes) reaches the handler as anonymous
without its `Authorization` header being read (R-AUTH-001).

**Rationale:** (c) keeps both promises of the specification. The M0 bridge
`system.bootstrap_anonymous_read` opens reads of the management API to anonymous callers on the
default endpoint; the composition root passes it as a route predicate until the authorization area
replaces it. Revisit when the authorization area decides anonymous reads of repository content under
`/api/v1/`: it must then read `anonymousAccess` itself.

**Applied in the spec:** R-AUTH-037 "Always unauthenticated" names `GET` and `HEAD` of `/` and of the
UI shell under `/ui/`, on an endpoint that admits anonymous requests: an unauthenticated visitor must load the application to reach the sign-in screen
(api-and-ui.md, sign-in criteria), and the shell holds no data. Code: `auth.EndpointRules.PublicRoute`,
`app.publicRoute`, `auth.alwaysOpen`.

### Q-AUTH-20 · Where the failed-credential throttle trips

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-038 puts an address into throttled mode when it has "more than `failures_per_address`" rejected
credentials in the window, and its acceptance criterion says that 400 requests with random
well-formed tokens are "validated at most 300 times". With the count-only limiter of R-ARCH-034 as
written (`Exceeded` is true when the weighted count *exceeds* the burst), 300 counted failures leave
the address open, and the 301st request is validated: 301 validations.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Exceeded above the burst | As R-ARCH-034 words it | Matches "more than" | The acceptance criterion fails by one |
| (b) Exceeded at the burst (chosen) | The 301st request is the first refused | Matches "at most 300 times"; also true of the per-replica shares | One request fewer than "more than" |

**Choice:** (b). The `auth.LocalLimiter` of `internal/auth` implements it; the cluster limiter of the
API area (`Exceeded` of R-ARCH-034) must use the same comparison for E1.8 to hold across replicas.
Every rejection of a presented credential is counted: a malformed value, an unknown token, a revoked
or expired one. A disabled owner and a storage failure are not.

**Rationale:** a limit documented as 300 must admit 300. Revisit when the cluster limiter lands and
its own test pins the comparison.

**Applied in the spec:** R-ARCH-034's count-only form reads "reports whether … reaches burst", and
`cluster.Limiter.Exceeded` compares with `>=`; its unit test pins the comparison.

### Q-AUTH-21 · What the login flow hands to the provisioner

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

`principal.LoginClaims` carried the claim values and the group-claim values. The provisioner has no
provider configuration, yet `jit`, `sync_username`, `create_groups`, `on_missing_claim` and the issuer
decide what it writes (R-AUTH-007, R-AUTH-008).

| Option | What it means | For | Against |
|---|---|---|---|
| (a) The provisioner reads the provider configuration | It imports the login area's types | Login passes less | The principal package would import the OIDC configuration, against the layer order of `m1-contracts.md` |
| (b) The login flow passes the policy with the claims (chosen) | `LoginClaims` gains `Issuer`, `EmailVerified`, `GroupSync`, `ClaimState`, `KeepOnMissing`, `CreateGroups`, `JIT`, `SyncUsername` and `Source`; `Groups` are the trove group names the mapping rules produced | The package stays below the login area; the unit of work is one call | The login flow applies the gates that need raw claims (`require_verified_email`, `login_requires_groups`, `on_missing_claim: deny`) before it calls |

**Choice:** (b). The refusals are the errors `ErrUserDisabled`, `ErrUsernameTaken`,
`ErrNotProvisioned` and `ErrUsernameInvalid`, which the login flow maps to its reasons. The
provisioner links the identity in `user_identity` (the table of `00008_login`) and completes a pending
link with a conditional update; two first logins of one identity on different replicas end with one
user, the loser retries once.

**Rationale:** the contract file lists the seam, not the policy; the additions are fields, so the
callers that fill only the first fields still compile. Revisit if the login area prefers to pass the
provider's configuration object through an interface.

**Applied in the spec:** none; `internal/auth/principal/principal.go`.

### Q-AUTH-22 · The payload kinds of the change area `auth`

Status: **Decided by the development agent** — selected from the options below; the owner reviews it at the walkthrough.

R-AUTH-023 says a change "names the token ID with the hash of the ended credential, the session ID or
the principal ID". The area is shared by tokens, principals, memberships, settings (this area) and
sessions (login), and a change message is limited to 1 KiB.

| Option | What it means | For | Against |
|---|---|---|---|
| (a) Every change flushes the four caches | Nothing to design | Always correct | Each CI job's closing self-revocation empties every replica's token cache |
| (b) Kinds with a precise reaction (chosen) | `token` (IDs and hashes), `principal` and `membership` (IDs), `group`, `settings`, `session`, `all`; a payload that does not fit becomes `all` | A revocation evicts one entry; a membership change touches only the principal cache | A cache must know the kinds; an unknown kind flushes |

**Choice:** (b). `token` evicts the named hashes from `auth_tokens`; `principal` flushes `auth_tokens`
(the cached owner status) and evicts the named principals; `membership` evicts the named principals
only; `group` and `all` flush; `settings` flushes `auth_settings`; `session` is the login area's and
the identity caches ignore it; any other kind flushes every cache of the area (the coarse reaction is
always safe). The hash in a payload is the SHA-256 of a 256-bit secret: it is the database's lookup
key and cannot be inverted or used to authenticate.

**Rationale:** the revocation bound does not depend on the precision, only the load does.

**Applied in the spec:** none; `docs/developer/identity.md` has the table.

## What this does not cover

### H-15 · Consent phishing of command-line browser login

npm's web login (R-AUTH-034) shows no code that ties the browser to the terminal. Someone who
starts a login and sends the `loginUrl` to a victim receives the victim's PAT if the victim
approves. The approval page shows the requesting address and user agent and warns when the
address differs, and R-AUTH-036's device flow requires a typed user code. Both reduce the risk
but do not remove it; RFC 8628 §5.4 describes the same residual risk for device codes. Users
must still recognise a login they did not start.

### H-16 · IdP-side deactivation without SCIM

Without SCIM (R-AUTH-039), disabling or removing a person at the IdP does not reach trove:

- open sessions live until their idle or absolute timeout (at most 12 h by default);
- group memberships keep their last synced state;
- PATs keep working.

The PATs keep working until one of three things happens: an administrator disables the user,
dormancy (R-AUTH-021, P1) triggers after 90 days, or the token expires. Until P1, only the first
and the last apply. Back-channel logout is deferred. Installations that need offboarding within
hours must use SCIM or disable users in trove as part of their leaver process.

### H-17 · Break-glass account is single-factor

The break-glass login uses a password only. It is protected by Secret handling, cluster-wide
lockout, short sessions, no token capability and high-severity audit events, and it is meant to
be disabled outside recovery. A TOTP or WebAuthn second factor (IAM-039, P2) is not specified.
Anyone who can read the Kubernetes Secret and reach the login page while the account is
enabled is administrator.

### H-18 · Authentication strength at the IdP is not checked

trove accepts any successful IdP authentication. It neither requests nor validates `acr` or
`amr`, and it does not force fresh credential entry. Whether a user passed MFA is entirely the
IdP's policy. An IdP that allows password-only login for the trove client therefore yields
password-only access to trove, including token minting.

### H-19 · Tokens are bearer credentials

trove tokens are not sender-constrained. That would need DPoP or mutual-TLS binding
([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.10.1), which package managers do not
support. A leaked PAT or SAT is usable from anywhere until it expires or is revoked. Scope
narrowing (R-AUTH-024), lifetime caps (R-AUTH-026), last-used data (R-AUTH-031) and open
revoke-by-value (R-AUTH-029) limit the damage; they do not prevent use.

### H-20 · Command-line login secrets in URLs outside trove

Component: npm web login (R-AUTH-034). Subject: the poll secret in the `doneUrl` path. Dormant
until R-AUTH-034 (P1) is enabled. npm requires the secret as part of a URL, so it passes through
every reverse proxy, ingress controller and egress proxy between the client and trove, and their
access logs are outside the redaction of R-AUTH-001. Address binding (R-AUTH-034, Q-AUTH-8)
stops a log reader who polls from outside the network that started the login: by default
outside its IPv4 `/24` or IPv6 `/56`, or outside an operator-listed range. It does not stop one
inside that network: behind the same NAT gateway, on a shared build host or terminal server, in
the same egress proxy farm, or a neighbour in the same `/24` or `/56` of an ISP or cloud address
block. Such a reader collects the PAT after the user approves; the refused-poll notice on the
approval page does not fire, because the reader's poll matches. With `poll_binding.mode: off` the
binding stops nobody. Behind an edge proxy with `server.trusted_proxies` empty, it stops nobody
who polls through the same proxy instance, because every poll then carries the proxy's address
(R-AUTH-034 rules, R-AUTH-040 warning). What makes it real: access logs that record request
paths and that people other than the user can read while the 10-minute flow is open, for
example through log streaming or shared dashboards.

### H-52 · A break-glass rotation that no running replica notices

Component: break-glass password rotation (R-AUTH-014, Q-AUTH-14). Subject: break-glass sessions
opened with the old password, and the old password itself. Live in the MVP whenever the break-glass
account is enabled. The rotation hold opens when a running `web` replica sees its mounted file
change. Two cases escape it:

- Every `web` pod that held the old file is replaced before any of them sees the new one, for
  example by a rollout started together with the Secret change. No hold opens, and sessions opened
  before the rotation run until their absolute timeout (1 h by default).
- A node's kubelet delivers the Secret later than `rotation_hold`. The replica on that node accepts
  the old password after the hold, until it notices the change itself and opens a new hold. If it
  is drained before that, the sessions it opened with the old password run until their absolute
  timeout.

What makes it real: someone who knows the old password, which is usually why it was rotated, and
reaches the login page during one of these windows. An operator who rotates because of a suspected
leak runs `trove admin break-glass end-sessions` after the rotation has reached every pod
(R-AUTH-015); it ends every break-glass session whatever file the replicas hold.
