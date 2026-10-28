# Identity and authentication

Status: specification — not implemented

This page specifies how trove establishes who is calling. It covers the browser login of humans
through OpenID Connect, the server-side UI session, the break-glass administrator, and the
principals trove knows: users, groups, service accounts and the anonymous user. It also covers
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
| UI session | user, break-glass administrator | OIDC login (R-AUTH-006), local form (R-AUTH-014) | `trv_session` cookie | idle 1 h, absolute 12 h |
| `trv_pat_…` | user | UI or API (R-AUTH-027), npm web login (R-AUTH-034), device login (R-AUTH-036) | Bearer, Basic password, NuGet API key | default 90 days, maximum 365 days |
| `trv_sat_…` | service account | administrator API or `trove admin` (R-AUTH-015, R-AUTH-027) | same | default 90 days, maximum 365 days |
| `trv_cit_…` | CI identity mapping | CI exchange ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)) | same | at most 6 h |

```text
 browser ──/auth/oidc/{p}/start──► IdP ──code──► /auth/oidc/{p}/callback ──► ui_session ──► trv_session cookie
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
3. **Session cookie** `trv_session`. It is accepted on `/api/v1/…` for every method, with the
   CSRF rules of R-AUTH-012. On `/files/…` it is accepted for `GET` and `HEAD`, and for `PUT` and
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
- Tokens in query parameters (`?access_token=`) MUST NOT be accepted
  ([RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) §2.3 discourages them; they leak into logs).
- The CI exchange endpoint `POST /api/v1/auth/oidc/token` (INV-10) takes a GitLab ID token as
  request input. That ID token is not a credential for this stage, and the endpoint itself is
  unauthenticated.
- A storage outage during validation MUST answer 503, not 401, so clients do not discard or
  re-prompt for working credentials.
- The request log and every other log MUST redact the following before writing: the
  `Authorization` header, the `X-NuGet-ApiKey` header, the `trv_session` and `trv_csrf`
  cookies, every substring matching the token pattern of R-AUTH-022 in paths and query
  strings, and the path segment after `/auth/cli/`. npm logout carries the token in the path
  (R-AUTH-035), and the npm web-login poll URL carries the poll secret there (R-AUTH-034).

Acceptance criteria:

- Run `curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $PAT" https://trove.test/api/v1/auth/whoami`.
  It prints `200`. With a `trv_session` cookie of another user added to the same request,
  `whoami` still names the token owner.
- Use a valid ID token issued by the configured IdP for trove's client as `Bearer`. The
  request gets 401 with `error="invalid_token"`, and no token-table query is executed (asserted
  in an integration test through a query counter).
- Send an expired PAT on a repository that anonymous may read. The request gets 401, not the
  anonymous response.
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

- Login providers are configured in the YAML file (INV-15), not in PostgreSQL; see Q-AUTH-1.
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
  - Each replica fetches `<issuer>/.well-known/openid-configuration`. The `issuer` in the
    document MUST be identical to the configured issuer (§4.3), or the provider is unusable.
  - The document is refreshed every `discovery_refresh`.
  - JWKS are fetched from `jwks_uri` and cached. An ID token with an unknown `kid` triggers at
    most one JWKS refetch per provider and replica per 60 s.
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

**Browser binding.** The flow is bound to the browser by the cookie `trv_oidc_binding`, whose
value is 32 random bytes:

- attributes `Secure; HttpOnly; SameSite=Lax; Path=/auth/oidc/; Max-Age=600`;
- an existing valid value is reused, so parallel logins in several tabs do not overwrite each
  other.

This is the CSRF defense of [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.7.

**Flow state.** The flow state lives in PostgreSQL, so the callback may land on any replica and
the load balancer needs no session affinity (AUTHN-011). A cleanup job deletes expired rows.

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

API:

| Method and path | Response |
|---|---|
| `GET /auth/oidc/{providerId}/start?return_to=/ui/…[&purpose=reauth]` | 302 to the authorization endpoint; 404 for an unknown provider; 503 `provider_unavailable` if discovery has not succeeded. `purpose=reauth` needs a live session and records it in `reauth_session_id` (R-AUTH-011). |

Acceptance criteria:

- The Location of `/auth/oidc/corp/start` meets all of the following:
  - it carries `code_challenge_method=S256`, a 43-character `code_challenge`, and `state` and
    `nonce` of 43 characters each;
  - the response sets `trv_oidc_binding` with `Secure`, `HttpOnly` and `SameSite=Lax`.
- Run with two replicas behind a round-robin Service and no affinity. 50 consecutive logins
  against the test IdP all succeed.
- `return_to=https://evil.test/` and `return_to=//evil.test` both end the login at `/ui/`.

### R-AUTH-006 · Callback handling and ID-token validation
Priority: **MVP** · Covers: AUTHN-009

`GET /auth/oidc/{providerId}/callback` MUST perform these steps in order. Any failure ends the
login with an error page (HTTP 400, or 502 for IdP errors) and the audit event
`auth.login.failed` with a reason code. No session is created.

**Validating the response (steps 1–5):**

1. Delete and return the flow row whose `state_sha256` matches the `state` parameter (`DELETE …
   RETURNING`). This makes the flow single-use. Missing or expired gives `flow_expired`.
2. The flow's provider MUST equal the path's provider. Otherwise `provider_mismatch`.
3. The SHA-256 of the `trv_oidc_binding` cookie MUST equal the flow's binding. Otherwise
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
   token:
   - The response's `sub` MUST equal the ID token's `sub`
     ([OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html) §5.3.2).
     Otherwise `userinfo_mismatch`.
   - Only JSON responses are supported.
   - For each mapped claim, the ID-token value wins and userinfo fills the gaps.
9. The IdP's access token and refresh token MUST be discarded after step 8. They are never
   stored and never accepted as credentials (INV-8).
10. Continue with R-AUTH-007 (user resolution), R-AUTH-008 (group sync) and R-AUTH-010 (session).
    Then redirect with 302 to the flow's `return_to`.

Acceptance criteria:

- Replaying a callback URL that already succeeded gives 400 `flow_expired`.
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
| Username | `preferred_username` | The value goes through Unicode NFC, then lower case, then `username_prefix` is prepended. The result must match `^[a-z0-9][a-z0-9._@+-]{0,127}$` and must not be reserved (`anonymous`, `system`, the break-glass username). Otherwise the login fails with `username_invalid`. |
| Email | `email` | Stored together with `email_verified`. It is never used for matching. |
| Display name | `name` | Free text, at most 256 characters. |
| Groups | `groups` | See R-AUTH-008. |

Claim names may be dotted paths into nested JSON objects, for example `realm_access.roles`.

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

**Providers with open registration.** A provider entry sets `publicIssuer: true` when anyone can
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
  startup fail with a message naming the provider. With `loginRequiresGroups: [acme]` and the
  group rule `matchRegex: "acme/trove-(.+)"` it starts; with the rule
  `matchRegex: "trove-(.+)"` it fails again.

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

- with `createGroups: false`, it is skipped;
- otherwise it is created with origin `oidc`.

**Writing the memberships.** In one transaction, trove sets the user's memberships whose
source is `oidc:<providerId>` to exactly the target set. Memberships with source `manual`,
`auto` or `scim` are never touched. Afterwards a cache invalidation is published (`NOTIFY`,
INV-12) and, if anything changed, a `user.groups.synced` event is written with the added and
removed groups.

**Missing claim.** The behavior follows `on_missing_claim`:

| Value | Behavior |
|---|---|
| `clear` (default, proposed) | Remove all `oidc:<providerId>` memberships and continue. Fails closed. See Q-AUTH-4. |
| `keep` | Leave the memberships unchanged. |
| `deny` | Fail the login with `groups_claim_missing`. |

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
          on_missing_claim: clear                # default (proposed): clear | keep | deny
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
- The IdP omits the claim. With the default, all `oidc:corp` memberships are removed and the
  login succeeds. With `deny`, the login fails with `groups_claim_missing`.

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
Set-Cookie: trv_session=<secret>; Secure; HttpOnly; SameSite=Lax; Path=/
```

- It has no `Domain`, `Max-Age` or `Expires` attribute, so it lives for the browser session and
  is host-only.
- `SameSite=Lax` rather than `Strict` is deliberate. The first navigation after the cross-site
  IdP redirect must carry the cookie.
- The cookie name is fixed by INV-8. See Q-AUTH-2 for the `__Host-` prefix.

**Validation.** Lookup follows the token path of R-AUTH-023: a per-replica cache with
`NOTIFY` invalidation and the same latency bound. The principal must still be active.

**Concurrency.** A user may hold up to `max_per_user` concurrent sessions. Creating one more ends
the oldest with reason `evicted`.

**Housekeeping.** `last_seen_at` is written at most once per 60 s per session. Ended and expired
rows are deleted 30 days after their end.

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

- After login the browser holds `trv_session` with `Secure`, `HttpOnly`, `SameSite=Lax`,
  `Path=/` and no `Domain`. The database holds only its SHA-256.
- Ending a session through the API makes the next request with that cookie fail with 401
  `session_expired` within the latency bound of R-AUTH-023. This holds on every replica.
- A 21st login of the same user ends the oldest session.

### R-AUTH-011 · Session timeouts and recent authentication
Priority: **MVP** · Covers: AUTHN-038

**Timeouts.** A session ends at whichever comes first: `last_seen_at + idle_timeout` or
`absolute_expires_at`. Break-glass sessions use their own shorter values (R-AUTH-014). Startup
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
Set-Cookie: trv_csrf=<token>; Secure; SameSite=Strict; Path=/
```

The cookie is not `HttpOnly`. The Angular `HttpClient` XSRF support reads it, configured with
`withXsrfConfiguration({cookieName: 'trv_csrf', headerName: 'X-CSRF-Token'})`
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
- `curl -X DELETE -b "trv_session=$S" https://trove.test/api/v1/users/me/sessions` without
  the header gets 403.
- `curl -X PUT -b "trv_session=$S" -T x.bin https://trove.test/files/generic-local/x.bin`
  without the header gets 403 `csrf_failed`. A `PUT` on `/api/npm/npm-local/…` carrying only the
  cookie gets 401.

### R-AUTH-013 · Logout and RP-initiated logout
Priority: **MVP** · Covers: AUTHN-038

**Logout.** `POST /auth/logout` (cookie plus CSRF) does the following:

- ends the session (reason `logout`) and publishes the invalidation;
- clears `trv_session` and `trv_csrf` with `Max-Age=0`;
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
- After logout, the old cookie gets 401 on every replica within the latency bound.

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

**Hashing.** Verification uses Argon2id ([RFC 9106](https://www.rfc-editor.org/rfc/rfc9106)) with
at least `m=65536` (KiB), `t=3` and `p=4`, which is the second recommended option of §4.

**Password strength.** A plaintext password MUST have at least 20 characters and MUST NOT appear
in an embedded list of the 10,000 most common passwords. Otherwise the replica refuses to start
(IAM-069). The strength of a supplied hash cannot be checked; the replica logs a warning.

**Password rotation.** The file is re-read when its modification time changes. A changed
password ends all break-glass sessions (reason `password_changed`).

**Disabled account.** If `enabled` is false, the local login is refused with 403
`local_login_disabled`. Startup ends any remaining break-glass sessions. With `enabled` true and
no readable password file, the replica refuses to start.

**Login.** The local login is `POST /auth/local/login` with `{"username": "...", "password":
"..."}`:

- The request must pass the origin check of R-AUTH-012.
- The answer is 204 with the session cookies, or 401 `invalid_credentials`.
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
```

Acceptance criteria:

- `curl -u admin:$BREAKGLASS https://trove.test/api/v1/auth/whoami` gets 401. The local form
  with the same password succeeds.
- Start a replica with a 12-character plaintext password. It exits with a non-zero status and
  names the strength rule.
- `POST /api/v1/tokens` from a break-glass session gets 403 `break_glass_restricted`.
- Rotate the Secret. Within 60 s the old password fails on every replica, and existing
  break-glass sessions are ended.

### R-AUTH-015 · Bootstrap and recovery commands
Priority: **MVP** · Covers: TOK-041, IAM-010

The `trove admin` subcommands (INV-2) give an operator who can run commands in a replica pod
(`kubectl exec`) the bootstrap and recovery operations. They use that replica's configuration
and database connection; someone with that access already holds the database credentials.

| Command | Effect |
|---|---|
| `trove admin service-account create <name> [--description …] [--admin]` | Creates a service account. `--admin` binds the system-administrator role ([authorization.md](authorization.md)). |
| `trove admin token create --service-account <name> [--ttl 15m] [--scope-file scope.json]` | Creates a SAT and prints it once to stdout. The TTL defaults to 15 min; the maximum is the SAT maximum (R-AUTH-026). |
| `trove admin token revoke --id <uuid>` or `--value-stdin` | Revokes a token (R-AUTH-029). |
| `trove admin break-glass status` or `unlock` or `end-sessions` | Shows the lockout state and sessions, clears a lockout, or ends break-glass sessions. |
| `trove admin user disable <username> [--keep-tokens]` | Disables a user (R-AUTH-016). |
| `trove admin sessions end --user <username>` or `--all` | Ends sessions. |

This replaces the catalog's file-drop bootstrap token (TOK-041). There is no request file and no
token file on disk. The token reaches only the operator's terminal or the Kubernetes Job that
ran the command; how such a Job feeds a Secret is up to [ha-and-helm.md](ha-and-helm.md).

Every command writes an audit event with actor `admin-cli`, the pod name and the OS user. Token
values are never logged.

Acceptance criteria:

- On a fresh installation, the following lets `$T` create a repository through
  `POST /api/v1/repositories`:

  ```shell
  kubectl exec deploy/trove -- trove admin service-account create bootstrap --admin
  T=$(kubectl exec deploy/trove -- trove admin token create --service-account bootstrap)
  ```

  After 15 minutes `$T` gets 401.
- `trove admin break-glass unlock` clears an active lockout immediately, on all replicas.

## Principals

### R-AUTH-016 · User accounts
Priority: **MVP** · Covers: IAM-001, IAM-003, IAM-004, IAM-009, ADMAPI-004, ADMAPI-005, ADMAPI-006, ADMAPI-007, ADMAPI-008, ADMAPI-009

**Principal table.** All principal kinds share one table, so
[authorization.md](authorization.md) can bind policies to `principal.id` uniformly. The
`ci_mapping` subtype is owned by [gitlab-ci-oidc.md](gitlab-ci-oidc.md).

**User record:**

- Users have no password; only the break-glass account has one, and it lives outside the
  database.
- Status is `active` or `disabled`. The break-glass lockout of R-AUTH-038 appears separately as
  `lockedUntil`.
- `isAdmin` and `adminVia` are read-only. They are derived from the role bindings of
  [authorization.md](authorization.md).
- Users are addressed by username in paths. The record also returns the stable `id`.

**The catalog's per-user options map to trove as follows:**

| Catalog option | trove |
|---|---|
| UI access disabled | Covered by service accounts (R-AUTH-020) |
| Internal password disabled | Not applicable: no internal passwords |
| Profile updatable | Not needed: profile data comes from the IdP |

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
  kind        text        NOT NULL CHECK (kind IN ('user', 'group', 'service_account', 'anonymous', 'ci_mapping')),
  name        text        NOT NULL,
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
  disabled_at               timestamptz
);
CREATE UNIQUE INDEX app_user_one_break_glass ON app_user (is_break_glass) WHERE is_break_glass;
```

API (administrator unless stated; read operations also accept the system scope
`identities:read`, R-AUTH-025):

| Method and path | Request | Response | Status codes |
|---|---|---|---|
| `GET /api/v1/users?q=&status=&origin=&group=&admin=&limit=&cursor=` | — | Page of users (paging per [api-and-ui.md](api-and-ui.md)) | 200 |
| `GET /api/v1/users/{username}` | — | User record (below) | 200, 404 |
| `GET /api/v1/users/me` | — | Own record; any authenticated user | 200, 401 |
| `POST /api/v1/users` | `{"username", "email", "displayName", "identities": [{"provider": "corp", "subject": null}]}` | User with origin `admin`; `subject: null` is a pending link (R-AUTH-007) | 201, 400, 409 |
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
  - her open UI session gets 401 within the latency bound;
  - re-enabling her does not make the old PAT valid again.
- Deleting `alice` and logging in again creates a user with a different `id`. Policies bound to
  the old principal do not apply.
- `POST /api/v1/users` with an existing username gets 409.

### R-AUTH-017 · Self-service profile
Priority: **MVP** · Covers: AUTHN-039, AUTHN-006, AUTHN-040

**The profile.** Every persisted OIDC user has a profile; the screen is in
[api-and-ui.md](api-and-ui.md). It shows:

- the identity from the IdP, read-only;
- groups with their source;
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

**Other principals.** Service accounts and the anonymous principal have no profile. The
break-glass account sees its profile without the tokens section.

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
`sa:<name>`. The anonymous principal cannot be a member.

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

**Effect of changes.** Every membership change publishes a cache invalidation. Tokens and
sessions see the new membership within the latency bound of R-AUTH-023.

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

- A daily singleton job (advisory-lock lease, INV-12) disables every active user with origin
  `jit` or `admin` whose `last_interactive_auth_at` (or `created_at` if they never logged in)
  is older than `dormancy_period`. The disable uses reason `dormant` and keeps the tokens.
- Users managed by SCIM and the break-glass account are exempt.
- While a user is disabled, their tokens fail with `principal_disabled`.
- A successful OIDC login re-enables a dormant user automatically, because the IdP has just
  vouched for the account. A user disabled by an administrator stays disabled.
- `user.dormancy_warning` is emitted `dormancy_warning` before the deadline. Webhooks can carry
  it ([operations-and-observability.md](operations-and-observability.md)), and email is P2.
- `dormancyPeriod: 0` switches the job off.

Configuration:

```yaml
auth:
  users:
    dormancy_period: 2160h   # default (proposed): 90 days; 0 disables; see Q-AUTH-5
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
2. The per-replica cache, keyed by the hash. On a miss, `SELECT … FROM api_token WHERE
   secret_sha256 = $1`.
3. The token is rejected unless all of the following hold:
   - `revoked_at IS NULL`;
   - `expires_at IS NULL OR expires_at > now()`;
   - the owner principal has `status = 'active'`.

   The type in the prefix must equal the row's `type`.
4. A use of a revoked or expired token whose row still exists writes `token.rejected` with the
   reason, at most once per token per hour (R-AUTH-041).

**Cache.** The cache holds positive results only, for at most `cache_ttl`. It is invalidated
through `LISTEN/NOTIFY` (INV-12) on channel `trove_auth`; the payload names the token,
session or principal that changed. Revocation, rotation, principal disable or delete,
membership changes and settings changes all notify. When the `LISTEN` connection drops, the
replica empties the cache and bypasses it until it has re-subscribed.

**Revocation latency.** A revoked token or ended session MUST be rejected by every replica
within 2 s while notifications flow, and never later than `cache_ttl` (15 s).

**Retention.** A singleton cleanup job deletes token rows `retention_after_end` after expiry or
revocation. A deleted row means an unknown token. The audit trail keeps the history.

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
  rotated_from       uuid        REFERENCES api_token (id) ON DELETE SET NULL,
  derived_from       uuid        REFERENCES api_token (id) ON DELETE SET NULL,  -- R-AUTH-027 derivation rule
  last_used_at       timestamptz,
  last_used_addr     inet,
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
  tokens:
    cache_ttl: 15s                # default (proposed); upper bound 60s
    retention_after_end: 2160h    # default (proposed): 90 days
```

Acceptance criteria:

- Revoke a token through replica A while replica B serves a request loop with it. B rejects the
  token within 2 s.
- With `LISTEN` artificially blocked on B, B rejects the token within 15 s.
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
      "include": ["@acme/**"],
      "exclude": [],
      "actions": ["read", "deploy"]
    }
  ],
  "system": ["identities:read"]
}
```

| Field | Meaning |
|---|---|
| `inherit` | `true` means all repository grants of the owner, evaluated at use time. This is the catalog's user-permissions scope (TOK-008) and gives identity tokens (TOK-005). `repositories` must then be empty. |
| `admin` | `true` lets the token exercise system administration if, and only if, the owner holds the administrator role at use time (TOK-009). An administrator's token without `admin: true` acts as a non-administrator, so administrator power is always an explicit choice. |
| `repositories` | Restricted grants with the same resource selector as a policy grant: repository keys or patterns, include and exclude patterns, actions from `read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage` (INV-9). Pattern syntax and matching are defined by [authorization.md](authorization.md). |
| `system` | Items from the closed list of R-AUTH-025 (P1). |

**Validation and defaults:**

- At most 50 grants, and at most 16 KiB of serialized JSON. Unknown actions or fields answer
  400 `invalid_scope`.
- Scopes are immutable after creation. Rotation keeps the scope; changing it means a new token.
- The default for PATs and SATs is `{"inherit": true}`. CIT scopes are produced by the identity
  mappings of [gitlab-ci-oidc.md](gitlab-ci-oidc.md) and use the same structure.

**Deliberate departure.** The catalog's resource scope (TOK-012) grants rights independently of
any policy. trove does not allow that. A scope that names actions the owner does not hold
simply yields nothing for them, because a token can never be a back door around policies.

Acceptance criteria:

- `alice` may read and deploy `npm-local`. Her token with scope
  `{"repositories": [{"repositories": ["npm-local"], "actions": ["read"]}]}` can install from
  `npm-local`, and `npm publish` with it gets 403.
- An administrator's PAT created with the default scope gets 403 on
  `POST /api/v1/repositories`. One created with `{"inherit": true, "admin": true}` succeeds.
- `alice`'s group loses deploy on `npm-local`. Within the latency bound her `inherit` token can
  no longer publish.

### R-AUTH-025 · System scope items
Priority: **P1** · Covers: TOK-014, ADMAPI-041

`system` restricts a token to named administration capabilities, without the blanket
`admin: true`.

**Effectiveness.** An item is effective only if the owner holds a role that grants that
capability ([authorization.md](authorization.md): the administrator role, or a narrower role if
that page defines one). `admin: true` implies every item.

**Item list (v1):**

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
| SAT | 2160 h | 8760 h | only if `sat.allowNonExpiring` is true (default false) |
| CIT | set by [gitlab-ci-oidc.md](gitlab-ci-oidc.md) | 6 h (hard ceiling) | never |

**Requests:**

- A request gives `ttlSeconds` or `expiresAt` (RFC 3339), or neither, which applies the default.
  Giving both answers 400.
- A lifetime above the maximum answers 400 `ttl_exceeds_maximum`. trove does not silently
  clamp it.
- For a SAT, `ttlSeconds: 0` requests a non-expiring token. It is accepted only when allowed,
  and otherwise answers 400.

**Who the caps bind.** The caps bind administrators as well. This departs from the catalog,
where administrators are uncapped, so that the security setting means what it says.

**YAML ceilings and runtime values.** The YAML values are ceilings. Administrators may lower the
default and the maximum at runtime (R-AUTH-040), but never raise them above the YAML values.
Lowering the maximum does not shorten existing tokens. `GET /api/v1/tokens?expiresAfter=` finds
them for revocation.

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
- Its expiry is no later than the presenting token's expiry.
- Its scope is no broader than the presenting token's scope. Each new grant must equal a
  presenting grant with a subset of its actions, and `inherit`, `admin` and `system` items may
  only be kept or dropped.

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
| A PAT for a different user. Administrators cannot either, because there is no impersonation; automation uses service accounts. | `forbidden` |

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
| 400 | `invalid_scope`, `ttl_exceeds_maximum`, `description_required` |
| 401 | Not authenticated, or `reauthentication_required` |
| 403 | The codes above, `derivation_exceeds_parent` (derivation rule violated), and `pat_not_allowed` (R-AUTH-017) |
| 409 | `token_limit_reached` (R-AUTH-026) |

Each success writes `token.created` with ID, type, owner, scope, expiry and `createdVia`, never
the secret.

Acceptance criteria:

- `curl -s -X POST -H "Authorization: Bearer $PAT" -d '{"description":"ci","ttlSeconds":3600}' https://trove.test/api/v1/tokens`
  returns 201 when `$PAT` expires later than 1 h. With `ttlSeconds` beyond `$PAT`'s expiry it
  returns 403 `derivation_exceeds_parent`.
- `curl -u x:$PAT -X POST … /api/v1/tokens` behaves like the Bearer call, so Basic with a token
  counts as token authentication.
- A CIT gets 403 `ci_token_restricted`.

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
| `DELETE /api/v1/tokens/current` | Revoke the presenting token | any token | 204 |
| `DELETE /api/v1/users/me/tokens` | Revoke all own tokens | user | 204 |
| `DELETE /api/v1/users/{username}/tokens`, `DELETE /api/v1/service-accounts/{name}/tokens` | Revoke all tokens of a principal | administrator | 204, 404 |
| `POST /api/v1/tokens/revoke` | Revoke by value: `{"token": "trv_…"}` | anyone; no authentication needed | 202; 200 with `{"id", "owner"}` for administrators; 400 if the syntax check fails |

**Revocation by value.** The endpoint is open because possession of a token already allows
revoking it (`DELETE /api/v1/tokens/current`). Opening it costs nothing and lets anyone who
finds a leaked token kill it, including a secret-scanning service (Q-AUTH-6). Its rules:

- The response does not reveal whether the token existed, except to administrators.
- It is rate-limited per R-AUTH-038.
- The reason is recorded as `leaked`, together with the caller's address.

**Common rules:**

- Revocation is immediate within the latency bound of R-AUTH-023.
- **Cascade.** Revoking a token MUST revoke, in the same transaction, every unrevoked token whose
  `derived_from` chain leads to it, with reason `parent_revoked`. A revocation with reason
  `leaked` (revocation by value) additionally follows `rotated_from` forward and revokes the
  rotation successors and their derived tokens, because a thief may have rotated the stolen token
  first.
- Revoking an already revoked or expired token answers 204, so the operation is idempotent.
- Every revocation writes `token.revoked` with reason and actor.
- CITs are revoked by administrators, by themselves through `current`, or by deleting their
  identity mapping ([gitlab-ci-oidc.md](gitlab-ci-oidc.md)).
- Creating a token never revokes another one.
- Token rows are removed only by the retention job (R-AUTH-023), or by deleting the owner.

Acceptance criteria:

- `curl -X POST -d '{"token":"'$PAT'"}' https://trove.test/api/v1/tokens/revoke` without
  credentials answers 202. `npm whoami` with `$PAT` gets 401 within 2 s. The audit trail shows
  `token.revoked` with reason `leaked`.
- `DELETE /api/v1/tokens/current` with a SAT makes that SAT unusable. The other SATs of the
  account keep working.
- A PAT derives a child PAT through `POST /api/v1/tokens`. Revoking the parent by value makes the
  child fail with 401 within the latency bound, and the audit trail shows `token.revoked` with
  reason `parent_revoked` for the child.

### R-AUTH-030 · Token rotation
Priority: **P1** · Covers: TOK-024

Rotation replaces a token's secret while keeping its description, scope and owner. It is how
holders renew long-lived credentials without the issuer; refresh tokens are not issued
(Q-AUTH-3).

**Endpoints:**

| Endpoint | Callers |
|---|---|
| `POST /api/v1/tokens/{id}/rotate` | Anyone who may revoke the token (R-AUTH-029); a session needs recent authentication |
| `POST /api/v1/tokens/current/rotate` | The token itself, only if it was created with `selfRotatable: true` |

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

### R-AUTH-031 · Last-used tracking
Priority: **P1** · Covers: TOK-029

**What is recorded.** Tracking is always on and has no switch. Every successful authentication
by a token records `last_used_at` and `last_used_addr` (the client address as determined in
[architecture.md](architecture.md), which owns the trusted-proxy handling). Bearer, Basic and
NuGet API-key presentations count alike.

**Write strategy.** Writes are coarsened and batched:

- each replica aggregates uses in memory;
- it flushes every 60 s with one `UPDATE … WHERE last_used_at IS NULL OR last_used_at <
  $ts − granularity`;
- updates are best effort. A failed flush is dropped and counted in a metric, and never fails a
  request.

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
  100 further requests within one minute, the database has seen at most one update for it.

### R-AUTH-032 · Token expiry notifications
Priority: **MVP** · Covers: IAM-038

**Daily job.** A daily singleton job finds active PATs and SATs whose `expires_at` falls within
`expiry_notice` and whose `expiry_notified_at` is null. For each one it:

- writes the event `token.expiring` (owner, token ID, hint, description, `expiresAt`);
- sets `expiry_notified_at`.

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

- A PAT expiring in 3 days produces exactly one `token.expiring` event across two job runs and
  two replicas.

## Client authentication

### R-AUTH-033 · Client authentication matrix
Priority: **MVP** · Covers: TOK-033, TOK-034, PKG-NPM-005, PKG-NPM-006, PKG-NPM-011, PKG-NUGET-017, PKG-NUGET-018, IAM-036, ADMAPI-055

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
| Browser UI | — | `trv_session` cookie | `/api/v1` all methods; `/files` `GET` and `HEAD`, and `PUT` and `DELETE` with CSRF (R-AUTH-012); package-protocol endpoints `GET` and `HEAD` only |

**NuGet API-key rule.** The rule applies on the API-key endpoints:

- The value is examined first. If the value, or its part after the last `:`, is a well-formed
  trove token (prefix, length, checksum), that token is the credential and the
  `Authorization` header is ignored.
- Otherwise, for example the placeholder `az` that some setups pass, the header value is ignored
  and the `Authorization` header applies.

The NuGet clients send the API key and the source credentials independently, so both
conventions work. The username part of `user:token` is ignored, like the Basic username.

**`whoami` rendering.** The npm `whoami` endpoint ([formats/npm.md](formats/npm.md)) answers
`{"username": "<name>"}`:

| Principal | Rendered name |
|---|---|
| User | Username |
| Service account | `sa:<name>` |
| CI token | `ci:<mapping name>` |
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

### R-AUTH-034 · npm web login
Priority: **P1** · Covers: PKG-NPM-008, AUTHN-044

npm's default `npm login` opens a browser and polls for a token. The protocol is from
[npm-profile](https://github.com/npm/npm-profile) (verified against its `lib/index.js`) and
[npm login](https://docs.npmjs.com/cli/commands/npm-login). trove serves it on top of the
OIDC session.

**Flow:**

1. **Start.** `POST /api/npm/{repoKey}/-/v1/login` needs no credential and is rate-limited
   (R-AUTH-038). The repository must exist and have format npm; otherwise 404, which makes npm
   fall back to legacy login. trove creates a pending `cli_login` row (expiry `cliLogin.ttl`)
   and answers 200:

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
     address.

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

npm treats any 4xx answer, on the start call or on a poll, as "web login unsupported" and falls
back to the username and password prompt. There, a user can still type a PAT as password
(R-AUTH-035). trove documents this behavior; it cannot change it.

**Address binding.** The `doneUrl` is a bearer URL: whoever polls it first after the approval
receives the PAT, and its path passes through every proxy log between npm and trove. With
`cliLogin.bindPollAddress: true`, a poll whose client address (R-ARCH-006 in
[architecture.md](architecture.md)) differs from `requested_addr` MUST get 404 and MUST NOT consume
the approval. The legitimate client keeps polling from its own address; if its address changed
in between, npm falls back to legacy login (R-AUTH-035). trove's own logs redact the poll secret
(R-AUTH-001); logs outside trove are H-20.

**Turning it off.** With `npmWebLogin: false`, step 1 answers 404, so npm uses legacy login.

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
  created_at         timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL
);
```

Configuration:

```yaml
auth:
  cli_login:
    ttl: 10m                 # default (proposed)
    npm_web_login: true      # default (proposed)
    token_ttl: 720h          # default (proposed): 30 days; capped by the PAT maximum
    bind_poll_address: true  # default (proposed)
```

Acceptance criteria:

- `npm login --registry=https://trove.test/api/npm/npm-all/` prints a URL. Opening it, logging
  in through the test IdP and approving lets the command finish. `~/.npmrc` then contains
  `//trove.test/api/npm/npm-all/:_authToken=trv_pat_`, and `npm whoami` prints the user.
- Polling the same `doneUrl` after success gives 404. A denied login makes npm stop polling.
- After the approval, a poll of the `doneUrl` from a second client address gets 404, and the next
  poll from the starting address receives the token. The request log shows the poll path as
  `/auth/cli/[REDACTED]`.
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
| `POST /api/v1/auth/device/authorize` | form fields `client_id=<tool name>` (free text, shown on the approval page), optional `scope` (JSON scope, R-AUTH-024) | 200 `{"device_code", "user_code": "WDJB-MJHT", "verification_uri": "<base>/ui/device", "verification_uri_complete": "<base>/ui/device?code=WDJB-MJHT", "expires_in": 600, "interval": 5}` |
| `POST /api/v1/auth/device/token` | `grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=…` | 200 `{"access_token": "trv_pat_…", "token_type": "Bearer", "expires_in": …}`; 400 `authorization_pending`, `slow_down`, `access_denied` or `expired_token` (§3.5) |

**Codes.** The user code has 8 characters from `BCDFGHJKLMNPQRSTVWXZ` (§6.1) and is compared
case-insensitively. Entering a wrong code 5 times invalidates the request. The device code is
the `pollSecret`.

**Approval.** The approval page requires the typed user code, a session, CSRF protection and
recent authentication.

Acceptance criteria:

- A test client using a standard RFC 8628 library (for example `golang.org/x/oauth2`
  `DeviceAuth`) obtains a PAT after the approval in the browser. The PAT then works with `curl`.
- Polling faster than `interval` gets `slow_down`.

## Anonymous access and abuse protection

### R-AUTH-037 · Anonymous access
Priority: **MVP** · Covers: IAM-005, AUTHN-036, PKG-NUGET-016

**The anonymous principal.** A built-in principal named `anonymous` (kind `anonymous`) always
exists. It cannot be deleted, cannot hold tokens or sessions, and cannot be a group member.

**Enabling it.** Anonymous access is off by default. It takes both of these:

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
- A per-repository "require authentication" flag, where
  [repositories-and-proxy.md](repositories-and-proxy.md) or a format page defines one
  (PKG-NUGET-016), overrides anonymous access for that repository.

**Always unauthenticated.** These endpoints are always reachable without a credential:

- `GET /api/v1/auth/login-options`;
- `/auth/oidc/…`, `/auth/local/login`, `/auth/cli/…`;
- the device endpoints and `POST /api/v1/tokens/revoke`;
- the CI exchange (INV-10);
- the npm login start;
- `:8081` operations;
- whatever [api-and-ui.md](api-and-ui.md) declares public, for example the OpenAPI document.

**Switching it off.** Turning anonymous access off takes effect on every replica within the
latency bound.

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

**Break-glass lockout (cluster-wide).** Failures are counted in PostgreSQL:

- `lockout.threshold` failures within `lockout.window` lock the account until
  `now + lockout.duration`;
- during the lock, the local login answers 429 with `Retry-After`, even for the correct
  password;
- locks expire on their own, so nobody can lock out the recovery account permanently;
- an administrator (`POST /api/v1/users/{breakGlassName}/unlock`, 204) or
  `trove admin break-glass unlock` clears a lock early;
- the user record shows `lockedUntil`, and `GET /api/v1/users?status=locked` lists the account
  (ADMAPI-050);
- each lock writes `auth.breakglass.locked`.

The per-node counting weakness of the catalog's suspension mechanism does not apply.

**Failed-credential throttle (per replica, in memory):**

- More than `failures_per_address` rejected credentials from one client address within 1 minute
  switch that address into throttled mode for the rest of the window.
- In throttled mode, requests that carry a credential not already in the positive validation
  cache get 429 with `Retry-After` and are not validated. Tokens already validated recently keep
  working, so colleagues behind the same NAT are not cut off.
- Addresses in `exempt_cidrs` are never throttled.
- The effective cluster-wide limit is this value multiplied by the number of replicas, by
  design.

**Unauthenticated row-creating endpoints.** These are limited to `starts_per_address` per minute
and address:

- `/auth/oidc/*/start`;
- npm `-/v1/login`;
- `/api/v1/auth/device/authorize`;
- `POST /api/v1/tokens/revoke`.

In addition, more than `max_pending_flows` pending rows in `oidc_login_flow` and `cli_login`
together answer 503.

**Client address.** It is determined per [architecture.md](architecture.md), with a trusted
proxy list. Without that list, forwarded headers are ignored, so a spoofed `X-Forwarded-For`
cannot dodge the limits.

Data model:

```sql
CREATE TABLE auth_lockout (
  account_key       text        PRIMARY KEY,   -- 'break-glass'
  failures          integer     NOT NULL DEFAULT 0,
  window_started_at timestamptz NOT NULL,
  locked_until      timestamptz
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
    failures_per_address: 300  # default (proposed); per replica and minute
    starts_per_address: 30  # default (proposed); per replica and minute
    max_pending_flows: 100000  # default (proposed)
    exempt_cidrs: []        # default (proposed)
```

Acceptance criteria:

- Five wrong break-glass passwords, spread across two replicas, lock the account on both. The
  correct password then gets 429 until `trove admin break-glass unlock` runs.
- 400 requests with random well-formed tokens from one address within a minute yield 429 after
  the 300th. A PAT that was used successfully from that address before the burst still works
  during it.

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
  timeouts, the client switches, and the SCIM state.

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
- Every change writes `settings.auth.changed` with the before and after values, and publishes a
  cache invalidation.

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
  the latency bound, and one audit event records the change.

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
| `auth.csrf_failed` | R-AUTH-012 | yes, at most one per session and minute |
| `auth.credential.rejected` | R-AUTH-001 for malformed or unknown credentials | log stream and metric only, because of volume |
| `token.rejected` | R-AUTH-023 for a revoked or expired token whose row exists | yes, at most one per token and hour; signals a leak or a stale client |
| `token.created`, `token.rotated`, `token.revoked`, `token.expiring` | R-AUTH-027 to R-AUTH-032 | yes |
| `user.created`, `user.updated`, `user.renamed`, `user.rename_conflict`, `user.disabled`, `user.enabled`, `user.deleted`, `user.dormancy_warning` | R-AUTH-007, R-AUTH-016, R-AUTH-021, R-AUTH-039 | yes; `details.source` is `jit`, `admin`, `scim`, `dormancy` or `admin-cli` |
| `user.groups.synced` | R-AUTH-008, with added and removed lists | yes, only when something changed |
| `group.created`, `group.updated`, `group.deleted`, `group.members.changed` | R-AUTH-018, R-AUTH-039 | yes |
| `serviceaccount.created`, `serviceaccount.updated`, `serviceaccount.disabled`, `serviceaccount.deleted` | R-AUTH-020 | yes |
| `cli_login.approved`, `cli_login.denied` | R-AUTH-034, R-AUTH-036 | yes |
| `settings.auth.changed` | R-AUTH-040 | yes |

**Metrics.** Proposed names, which the operations page may rename:

- `trove_auth_attempts_total{method,outcome,reason}`
- `trove_auth_sessions_active`
- `trove_tokens_active{type}`
- `trove_tokens_expiring{type}`
- `trove_auth_cache_invalidations_total`
- `trove_auth_ratelimited_total{limit}`
- `trove_auth_lastused_flush_failures_total`

Acceptance criteria:

- The full acceptance suite of this page leaves no audit event containing `trv_`, `eyJ` (a
  JWT) or the break-glass password.
- A failed OIDC login with a wrong nonce produces exactly one `auth.login.failed` with
  `reason=nonce_mismatch` and the client address.

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
- **Revocation latency is a bounded promise**: ≤ 2 s with notifications, ≤ 15 s worst case
  (R-AUTH-023). Incident runbooks can rely on it.
- **Anonymous access fails toward 401.** Probing unknown repository keys reveals nothing, and
  anonymous callers can never write through the management API, whatever a policy says
  (R-AUTH-037).
- **The break-glass account** is password-only and administrator by construction. It is
  disabled by configuration in normal operation, restricted (no tokens), loud in the audit
  trail, and locked out cluster-wide on guessing (R-AUTH-014, R-AUTH-038). Residual risk: H-17.
- **Credential-minting needs a recent IdP round trip** (R-AUTH-011), and token-authenticated
  minting is bounded by the derivation rule (R-AUTH-027). A hijacked session or a narrow leaked
  token therefore cannot be turned into a broad, long-lived credential.

## Open questions

### Q-AUTH-1 · Where login provider configuration lives

The question: are login providers YAML-configured (R-AUTH-004), or managed in PostgreSQL through
the API and UI like the CI identity mappings?

| Option | Assessment |
|---|---|
| (a) YAML only | Secret in a mounted file; no lock-out through a bad API edit; changes need a rollout |
| (b) PostgreSQL and API | UI-editable; the client secret must be stored encrypted (IAM-045); a wrong edit can lock out every OIDC administrator |
| (c) YAML defines, API overrides | Two sources of truth |

**Recommendation: (a).** Login is bootstrap-critical, and the secret then never enters the
database. Revisit if operators ask for UI editing.

### Q-AUTH-2 · Session cookie name

The question: should the session cookie use the `__Host-` prefix (`__Host-trv_session`)
instead of INV-8's `trv_session`?

The prefix makes the browser enforce `Secure`, `Path=/` and no `Domain`
([draft-ietf-httpbis-rfc6265bis](https://datatracker.ietf.org/doc/draft-ietf-httpbis-rfc6265bis/)).
This blocks a sibling subdomain from planting or shadowing the cookie.

| Option | Assessment |
|---|---|
| (a) Keep `trv_session` | The invariant's name; R-AUTH-010 sets the same flags by convention |
| (b) Amend INV-8 to `__Host-trv_session` (and `__Host-trv_csrf`) | The browser enforces the flags |

**Recommendation: (b).** It costs nothing and turns a convention into a browser-enforced
guarantee. This page keeps (a) until the ADR changes INV-8.

### Q-AUTH-3 · Refresh tokens or rotation

The question: should trove implement the OAuth refresh grant of TOK-024, with a refresh-token
type, or only rotation (R-AUTH-030)?

| Option | Assessment |
|---|---|
| (a) Rotation only | No fourth secret type; no prefix outside INV-8; package clients cannot use refresh anyway |
| (b) Refresh tokens for device-login clients | Short access tokens with renewal; needs a new prefix and refresh-token reuse detection ([RFC 9700](https://www.rfc-editor.org/rfc/rfc9700) §4.14) |

**Recommendation: (a).** Package managers present static tokens, and the device-login clients of
R-AUTH-036 can rotate.

### Q-AUTH-4 · Missing group claim

The question: when the configured group claim is absent or overflowed at login, does sync
clear, keep or deny (R-AUTH-008)?

| Option | Assessment |
|---|---|
| (a) `clear` | Fails closed; an IdP misconfiguration removes rights, not adds them; a user whose administrator group comes from the claim loses it until fixed (break-glass covers) |
| (b) `keep` | Stale rights survive IdP-side removals |
| (c) `deny` | Loudest, but a mapper misconfiguration locks everyone out of the UI |

**Recommendation: (a) as default**, with the warning event. The setting stays per provider.

### Q-AUTH-5 · Dormancy default

The question: should dormant-user deactivation (R-AUTH-021) be on by default (90 days) or off?

| Option | Assessment |
|---|---|
| (a) 90 days | Bounds the offboarding gap without SCIM (H-16); users who only use npm must open the UI once per quarter |
| (b) Off | No surprise for such users; offboarding without SCIM depends on manual disabling |

**Recommendation: (a).** trove's identity truth is the IdP. A quarterly silent login is cheap,
and failing to offboard is the costlier error.

### Q-AUTH-6 · Unauthenticated revoke-by-value

The question: should `POST /api/v1/tokens/revoke` accept unauthenticated calls (R-AUTH-029)?

| Option | Assessment |
|---|---|
| (a) Open | Anyone, including a secret-scanning service, can kill a leaked token; possession already implies the ability to revoke |
| (b) Administrators only | Matches the catalog; leaked tokens live until an administrator acts |

**Recommendation: (a).** It adds no capability beyond what token possession grants, and it
shortens leak exposure. Rate-limiting covers the abuse case.

### Q-AUTH-7 · Administrators issuing tokens for users

The question: should administrators be able to create PATs whose owner is another user, as the
catalog allows?

| Option | Assessment |
|---|---|
| (a) No (R-AUTH-027) | Automation uses service accounts; a user's PAT is always created by that user, so audit attribution stays honest |
| (b) Yes, audited | Convenient for migrations, but it is impersonation |

**Recommendation: (a).** A migration path from another system can create service accounts
instead.

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
dormancy (R-AUTH-021) triggers after 90 days, or the token expires. Back-channel logout is
deferred. Installations that need offboarding within hours must use SCIM or disable users in
trove as part of their leaver process.

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
access logs are outside the redaction of R-AUTH-001. Address binding (R-AUTH-034) stops a log
reader who polls from another address. It does not stop one who shares the user's client address:
a NAT gateway, a shared build host or a terminal server. Such a reader collects the PAT after the
user approves. What makes it real: access logs that keep request paths for longer than the
10-minute flow lifetime and are readable by people other than the user.
