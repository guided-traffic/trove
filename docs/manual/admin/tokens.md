# Managing tokens

How administrators oversee the tokens of an installation: the Tokens screen, the tokens of users and service accounts, scopes, lifetime limits and their defaults, binding tokens to endpoints and the credentials each endpoint accepts, revocation and how fast it takes effect, the audit events of tokens, and the protection against guessing credentials.

## Token types

| Type | Prefix | Owner | Created by |
|---|---|---|---|
| Personal access token (`pat`) | `trv_pat_` | A user | The user, under **Personal tokens**; nobody creates one for somebody else |
| Service-account token (`sat`) | `trv_sat_` | A service account | An administrator, on the service account's screen or with the command line |
| CI token (`cit`) | `trv_cit_` | A CI pipeline | Not built yet: no route issues one, see [Known limitations](limitations.md) |

A token acts as its owner and its scope can only narrow the owner's permissions. trove stores the SHA-256 of a token, never the value; no event, log line or answer except the one that creates it holds the value. [Personal access tokens](../user/access-tokens.md) is the users' view of the same mechanisms.

## The Tokens screen

**Administration › Identity › Tokens** opens **Token administration**, the list of every token of the installation. It needs the permission to read tokens (the roles admin, security-admin and auditor); revoking needs the permission to revoke tokens (admin and security-admin).

The filter panel narrows the list:

| Filter | Values |
|---|---|
| **Owner** | `me`, a user name, or `sa:<name>` for a service account |
| **Type** | **Any**, **Personal (pat)**, **Service account (sat)**, **Pipeline (cit)** |
| **State** | **Active** (the default), **Expired**, **Revoked**, **All** |
| **Expires before** | A date |
| **Not used for (days)** | A number of days |

**Not used for (days)** lists the tokens whose last use is older than that many days, and the tokens that were never used. trove records the last use of a token (time and client address) when the token authenticates a request, in batches: each replica flushes every 60 seconds and at shutdown, and a token's row changes at most once per `auth.tokens.last_used_granularity` (default `5m`), so the time can be a few minutes behind and a replica that is killed loses at most one interval of uses. A token that has not been used since this was introduced shows no last use.

To revoke, choose **Revoke** on a row, or tick active tokens and choose **Revoke selected**; both ask for confirmation. **Revoke by value** takes a leaked token: paste it into **Token value** and choose **Revoke token**. The field is cleared whatever the answer. An administrator is told whose token was revoked. Anyone else, and anyone who pastes a value that is not a token, gets the same neutral message that the request was accepted: it does not say whether the value was a token, so the screen cannot be used to test guesses.

## Tokens of users and service accounts

- **A user's screen** (**Administration › Identity › Users**) lists the user's tokens in the section **Tokens**, with **Revoke** and **Revoke all tokens**. Nobody creates or rotates a personal token for another user. Disabling a user offers **Also revoke all tokens of this user**; without it the tokens remain but answer `401` `principal_disabled` while the user is disabled. Deleting a user removes the tokens.
- **A service account's screen** (**Administration › Identity › Service accounts**) has a section **Tokens** with **Create token**, **Rotate**, **Revoke** and **Revoke all tokens**. The form is the one of personal tokens, plus the lifetime **Never expires** (see [Lifetime limits and defaults](#lifetime-limits-and-defaults)). The section needs the permission to read tokens. Creating and rotating a service-account token needs the administrator role and a recent sign-in, or a token of the account itself; the buttons **Create token** and **Rotate** therefore appear for the administrator role only, not for a holder of the permission to change identities alone (for example a security administrator), whom the server would refuse. Disabling the account offers **Also revoke all tokens of this account**; deleting it removes its tokens.

A rotation creates a new value with the same scope and gives the old one an overlap of **Not at all (revoked now)**, **One hour** or **One day** (the API accepts up to 86,400 seconds).

### From the command line

The `trove` binary creates and revokes service-account tokens without the UI, with the installation's configuration (`--config`) and its own database connection. It records the actor `admin-cli` in the audit trail.

```shell
trove admin service-account create ci-builds --description "CI builds"   # example
trove admin token create --service-account ci-builds --ttl 720h --scope-file scope.json
trove admin token revoke --id 0192f1d2-7c4e-7a51-9b0c-3f2e4d5c6b7a          # example ID
trove admin token revoke --value-stdin < leaked.txt
```

`token create` prints only the value, so `T=$(trove admin token create …)` captures it; its lifetime is 15 minutes unless `--ttl` says otherwise, at most the service-account maximum. Without `--scope-file` the token has all repository permissions of the account.

## Scopes

A scope is a ceiling on the owner's permissions; it never grants anything. Its members in the API (`POST /api/v1/tokens`, `POST /api/v1/service-accounts/<name>/tokens`) and in a scope file:

| Member | Meaning |
|---|---|
| `inherit` | All repository permissions of the owner; excludes `repositories` |
| `repositories` | Up to 50 grants, each with `repositories` (keys or patterns), optional `packages` and `paths` (`include` and `exclude` patterns) and `actions` |
| `admin` | The token exercises the owner's system roles, as long as the owner holds the administrator role when the token is used |
| `system` | System capabilities the token may exercise, of those the owner's roles grant |
| `endpoints` | Up to 16 endpoint names the token is valid on; empty means unbound |

A scope document holds at most 16 KiB; an unknown member, action, capability or endpoint answers `400` `invalid_scope`. The form of the UI offers `inherit` (**All permissions of the owner**) or one grant with repositories, package patterns and actions; it sets neither `admin`, `system` nor `paths`, and `endpoints` only through the field **Endpoints** (see [Binding tokens to endpoints](#binding-tokens-to-endpoints)). A token from the form therefore never exercises a system role.

A token can create another token of the same type and owner through the API. The new one has a scope no broader, an expiry no later and, if its parent is bound to endpoints, a subset of them (`403` `derivation_exceeds_parent` otherwise); it is revoked with its parent.

## Lifetime limits and defaults

**Administration › System › Settings** opens **Authentication settings**. Its section **Token lifetimes** holds, for **Personal tokens** and **Service account tokens**, the **Default lifetime (days)** and the **Maximum lifetime (days)**, and under **Expiry notice** the period **Notify this many days before a token expires**. Values are in days, fractions allowed, at least 60 seconds. Each field shows its ceiling from the configuration, which it cannot exceed (`exceeds_ceiling`), and a default cannot exceed its maximum (`default_above_maximum`). **Save settings** needs the permission to change settings and a recent sign-in; the change applies on every replica and is recorded as `settings.auth.changed`.

The ceilings and the other token limits are configuration keys:

```yaml
auth:
  revocation_bound: 2s                  # default; how long a revocation may take to reach every replica
  tokens:
    cache_ttl: 15s                      # default
    retention_after_end: 2160h          # default; a token row is deleted this long after expiry or revocation
    expiry_notice: 168h                 # default; ceiling of the expiry notice
    pat:
      default_ttl: 2160h                # default
      max_ttl: 8760h                    # default; ceiling of the runtime maximum
      allowed_groups: []                # default: every user may create personal tokens
    sat:
      default_ttl: 2160h                # default
      max_ttl: 8760h                    # default
      allow_non_expiring: false         # default
    max_active_per_principal:
      pat: 50                           # default
      sat: 20                           # default
ui:
  client_setup:
    token_env_var: TROVE_TOKEN          # default; the variable the snippets and guides read the token from
    token_ttl: 720h                     # default; lifetime of a token created on the Client setup page
```

What a create request may ask for:

- A lifetime of at least 60 seconds, as `ttlSeconds` or `expiresAt`, not both (`400` `invalid_ttl`). Without one, the default of the type applies.
- Nothing above the effective maximum: the request is refused with `400` `ttl_exceeds_maximum`, never shortened.
- A non-expiring token (`ttlSeconds: 0`) only for a service account, only while `allow_non_expiring` is true (`400` `non_expiring_not_allowed`), and only by an administrator (`403` `non_expiring_restricted`).
- At most the number of active tokens of `max_active_per_principal` (`409` `token_limit_reached`). With `allowed_groups` set, only members of those groups create personal tokens (`403` `pat_not_allowed`).

The [configuration reference](https://github.com/guided-traffic/trove#configuration-keys) lists every key; [Configuration](configuration.md) explains how to set them.

## Binding tokens to endpoints

Each endpoint decides which credentials it accepts, under `server.endpoints.<name>.auth`:

| Key | Default endpoint (`default`) | Further endpoint |
|---|---|---|
| `token_types` | `[pat, sat, cit]` | `[sat]` |
| `bound_tokens_only` | `false` | `true`: only tokens bound to this endpoint |
| `anonymous` | `true` (subject to the anonymous access switch) | `false` |

A token whose `endpoints` list is not empty works only on the endpoints it names; list `default` too if it should also work on the main address. The names are checked against the configuration when the token is created and kept on rotation. If an endpoint is later removed from the configuration, the token lists show **Bound to an endpoint that is not configured:** with the name; the token is valid on none of them.

The token form shows the field **Endpoints** when you may read endpoints: one checkbox per configured endpoint that accepts the type of the token (personal tokens: the endpoints that list `pat` under `auth.token_types`; service-account tokens: those that list `sat`). Tick the endpoints the token is valid on; tick none and the token is unbound. The binding is part of the scope, so it cannot be changed afterwards and rotation keeps it. Without the permission to read endpoints the field is missing and the token is created unbound. The same binding is possible with the `--scope-file` of `trove admin token create`, or with `POST /api/v1/service-accounts/<name>/tokens` and the scope as its `scope` member. The API caller is an administrator with a recent sign-in or a token of the service account itself under the derivation rule above; an administrator's personal token from the UI form exercises no role and is refused. A binding to an endpoint that does not accept the type of the token answers `400` `invalid_scope` and names the endpoint, in the API and in `trove admin token create` alike: such a token would be refused on that endpoint by its type and on every other by its binding. A scope for a customer endpoint `customers` (example values):

```json
{
  "repositories": [
    {"repositories": ["npm-customers", "npm-internal", "npmjs"], "actions": ["read"]}
  ],
  "endpoints": ["customers"]
}
```

A virtual repository needs its members in the grant as well, as above. A refused credential answers `401` `invalid_token` like an unknown one. trove counts each refusal in `trove_endpoint_credentials_refused_total` by endpoint and reason (`token_type`, `token_binding`, `anonymous`) and records a refused token as `token.rejected` with the reason `endpoint_not_accepted`; these refusals do not count towards the protection below. [Endpoints and exposure rules](endpoints.md) describes the rest of the endpoint configuration.

## Revocation and its propagation

A token is revoked by its owner, by a holder of the permission to revoke tokens, by value through `POST /api/v1/tokens/revoke` (open to anyone who has the value), or with `trove admin token revoke`. Revoking is final and idempotent.

- **Cascade.** Tokens created with the revoked token are revoked with it. A revocation by value also follows rotations forward, in case the value was rotated by whoever found it.
- **Propagation.** Every replica refuses a revoked token within `auth.revocation_bound`, 2 seconds by default, also while Valkey is unavailable: the replicas learn of the change from PostgreSQL, and a replica that cannot confirm it is up to date checks every token in PostgreSQL instead of its cache.
- **Storage failures** answer `503`, never `401`, so clients keep their working tokens while the database is unreachable.
- **Retention.** The task `token-cleanup` deletes a token row `retention_after_end` after its expiry or revocation; the token is then unknown, and the audit trail keeps its history.

## Audit of token events

The token events appear under **Administration › System › Audit log**. None of them holds a token value.

| Event | Recorded when | Details |
|---|---|---|
| `token.created` | A token is created | Type, owner, scope, expiry, creation path, description, parent token |
| `token.rotated` | A token is rotated | The predecessor, the overlap |
| `token.revoked` | A token is revoked, one event per token of a cascade | Reason: `owner`, `admin`, `rotated`, `leaked`, `principal_disabled`, `parent_revoked` |
| `token.expiring` | The task `token-expiry-notice` (daily by default) finds a token within the expiry notice period, once per token | Owner, hint, description, expiry |
| `token.rejected` | A revoked or expired token is presented (at most once per token and hour), or an endpoint refuses a token | Reason: `revoked`, `expired`, `endpoint_not_accepted` |

The expiry notice is this event; this version sends no mail. The token lists also mark a token **Expires soon** in its last seven days.

## Protection against guessing credentials

trove counts rejected credentials per client address across all replicas: malformed, unknown, revoked and expired tokens. A disabled owner, a storage failure and an endpoint's type or binding refusal do not count.

```yaml
auth:
  clients:
    basic:
      enabled: true                     # default; a token as the password of HTTP Basic authentication
  rate_limit:
    failures_per_address: 300           # default; rejected credentials per minute and client address
    starts_per_address: 30              # default; per minute and address, including the revocation by value
    recent_token_window: 10m            # default
    exempt_cidrs: []                    # default; example: ["10.42.0.0/24"]
```

When an address reaches `failures_per_address`, it is throttled: a token this replica has cached, or one that any replica validated within `recent_token_window`, still works; every other credential from that address gets `429` `rate_limited` with `Retry-After: 60` without being checked. Addresses in `exempt_cidrs`, such as a build farm behind one address, are never throttled. While Valkey is unavailable each replica applies its share of the budget. The lockout of the local break-glass account is separate; see [Sign-in and identities](sign-in-and-identity.md).
