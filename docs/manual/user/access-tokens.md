# Personal access tokens

A personal access token lets a tool such as npm, pnpm, Yarn or curl act as you without your password. This page explains what a token is and how it is recognised, how to create one with a narrow scope and a lifetime, how to see, rotate and revoke your tokens, how clients and scripts send a token, what happens when it expires, and how personal tokens differ from the service-account tokens that administrators manage.

## What a token is

- A token **acts as you**. It never holds more than your own permissions: its scope can only narrow them. When your permissions change, so do the token's, and while your account is disabled, the token does not work.
- The **value is shown once**, when the token is created. trove stores only a hash of it, so nobody, administrators included, can show it again.
- Clients send the token as a **bearer token** (`Authorization: Bearer <token>`) or as the **password of HTTP Basic authentication** with any user name, unless an administrator switched Basic authentication off. A Basic request with your account password instead of a token is refused.
- The browser session of the UI is not a token; the UI never needs one.

### Token prefixes

Every token has 57 characters: a prefix, 43 random letters and digits, and a 6-character checksum that catches a mistyped or truncated value.

| Prefix | Type | Owner |
|---|---|---|
| `trv_pat_` | Personal access token (`pat`) | A user; created by the user |
| `trv_sat_` | Service-account token (`sat`) | A service account; created by an administrator |
| `trv_cit_` | CI token (`cit`) | Reserved; this version issues none |

Lists show a token by its description and its **hint**, the prefix plus the first four random characters, such as `trv_pat_Ab3x`.

## Create a token

1. Open the user menu and choose **Profile and tokens**; the page **Personal tokens** opens.
2. Choose **Create token**.
3. Fill in the form (below) and choose **Create token**.
4. The panel **Your new token** shows the value. Choose **Copy** (or **Show** to read it), store it where your tool reads it, then choose **I have copied it**. Leaving the page also discards the value.

| Field | What to enter |
|---|---|
| **Description** | What the token is for, for example the machine or the job. Required, at most 1024 characters |
| **Lifetime** | **Server default**, **7 days**, **30 days**, **90 days**, **1 year** or **Custom date** |
| **Expires on (UTC)** | With **Custom date**: the token expires at the end of that day, in UTC |
| **Scope** | **All permissions of the owner**, or **Only the repositories and actions below** |
| **Repositories** | With a restricted scope: one repository key or pattern per line, for example `npm-internal` or `npm-team-*` |
| **Package patterns (optional)** | One pattern per line, for example `@team-a/*`; empty means every package |
| **Actions** | With a restricted scope: `read`, `annotate`, `deploy`, `overwrite`, `delete`, `manage` |

Creating a token needs a recent sign-in. If your last sign-in is too long ago, trove asks you to [sign in again](troubleshooting.md#session-ended-and-re-authentication); your entries are kept and the form reopens with them.

### Choose the scope

Give each tool its own token with only what it needs. The actions a client needs:

| Task | Actions |
|---|---|
| Install packages, download files | `read` |
| Publish a package, set a dist-tag | `read`, `deploy` |
| Upload a new file | `deploy` |
| Replace a file with other bytes | `deploy`, `overwrite` |
| Unpublish a package version | `read`, `delete` |
| Push a NuGet package, or list a NuGet version again | `read`, `deploy` |
| Unlist or delete a NuGet version | `read`, `delete` |
| Delete a file | `delete` |

Through a virtual repository a client reads the members as well, so list the members next to the virtual repository, or use a pattern that covers them. [Permissions](../admin/permissions.md) explains the actions.

A token created in the UI carries your permissions on repositories but none of your system roles, so it cannot perform administrative operations that need a role, even when you hold one. The scope of the API has members for that (`admin`, `system`), which the form does not offer.

### Choose the lifetime

Every personal token expires. **Server default** is 90 days and the longest lifetime is one year, unless an administrator set other values; a longer request is refused with `ttl_exceeds_maximum`, not shortened.

### Bind a token to endpoints

An installation can serve its repositories on further endpoints, for example one for customers. A token can be bound to endpoints by name; it then works on those endpoints only. If you may read endpoints, the form has a field **Endpoints** with one checkbox for each endpoint that accepts personal tokens (a further endpoint usually does not, so it is not offered); without that permission the form has no such field, and the binding is the `endpoints` member of the scope in the API. Further endpoints accept only service-account tokens bound to them unless an administrator configured otherwise, so for such an endpoint ask the administrator for a token. [Endpoints and exposure rules](../admin/endpoints.md) describes the endpoints.

### Limits

You can hold 50 active personal tokens unless the administrator set another number (`409` `token_limit_reached`: revoke one first). An installation can restrict personal tokens to members of certain groups (`403` `pat_not_allowed`). The local break-glass administrator holds no tokens.

## See your tokens

**Personal tokens** lists your active tokens; tick **Show revoked and expired tokens** to see the others.

| Column | Shows |
|---|---|
| **Description** | Your description and the hint of the value |
| **Type** | `pat` |
| **Scope** | **All permissions of the owner**, or the number of repository grants; a warning when the token is bound to an endpoint that is no longer configured |
| **Created**, **Expires** | Times; **Expires soon** within seven days of the expiry |
| **Last used** | When the token last authenticated a request, recorded in batches, so it can be a few minutes behind; **Not used yet** for a token without a recorded use |
| **Created via** | `ui`, `api` or `rotation` |
| **State** | **Active**, **Expired** or **Revoked** |

## Rotate a token

Rotating replaces a token's value and keeps its description and scope, for example on a schedule or when a value may have been seen.

1. Choose **Rotate** next to the token.
2. Under **The old token stays valid for**, choose **Not at all (revoked now)**, **One hour** or **One day**, and choose **Rotate**.
3. Copy the new value from **Your new token** and replace the old one everywhere within the overlap.

The new token gets the lifetime of the old one, at most the current maximum. Only the owner rotates a personal token; an expired or revoked token cannot be rotated.

## Revoke a token

Choose **Revoke** next to a token and confirm; **Revoke all tokens** revokes every token you hold. A revoked token stops working on every replica within seconds, and revoking cannot be undone. Tokens that were created with this token through the API are revoked with it.

If a token leaked, revoke it at once. Anyone who has the value, signed in or not, can revoke it by sending `{"token": "<value>"}` to `POST /api/v1/tokens/revoke`; for a caller who is not an administrator the answer is `202` and does not say whether the token existed. An administrator can also revoke it by value on the **Tokens** screen.

## Use a token

### In package clients

Keep the token in the environment variable `TROVE_TOKEN` (or the name your installation configured; the snippets show it) and let the client read it, so no configuration file holds the secret. [Connecting a package client](client-setup.md) explains the configuration of npm, pnpm, Yarn and curl, and the Client setup page shows it for your installation.

### With curl and the management API

Hand the token to curl on standard input, so that it appears neither on the command line nor in the process list. The function below is the one the generic repository snippets define:

```shell
trove_curl() {
  curl --silent --show-error --config - "$@" <<EOF
header = "Authorization: Bearer ${TROVE_TOKEN}"
EOF
}
trove_curl --fail https://trove.example.com/api/v1/tokens/current
```

Useful calls with a token:

| Call | Does |
|---|---|
| `GET /api/v1/tokens/current` | Describes the token that authenticates the request |
| `DELETE /api/v1/tokens/current` | Revokes that token, for example as the last step of a job |
| `GET /api/v1/tokens` | Lists your tokens |
| `POST /api/v1/tokens` | Creates a token from the presenting personal token: same owner, a scope no broader, an expiry no later (`403` `derivation_exceeds_parent` otherwise) |

A token created from another one is revoked when its parent is revoked.

## When a token expires

An expired token is refused with `401` `invalid_token`, the same answer as for an unknown or revoked token. Nothing renews it: create a new token, or rotate the old one before it expires. The list shows it as **Expired** while **Show revoked and expired tokens** is ticked; trove deletes the record 90 days after the expiry or revocation, unless an administrator set another period.

## Personal and service-account tokens

| | Personal token | Service-account token |
|---|---|---|
| Prefix | `trv_pat_` | `trv_sat_` |
| Acts as | You | A service account, independent of any person |
| Created by | You, under **Personal tokens** | An administrator ([Managing tokens](../admin/tokens.md)) |
| Use | Your own machine and tools | CI jobs and other automation |
| Lifetime | Always expires | Expires; can be non-expiring where the installation allows it |
| Further endpoints | Refused unless an administrator allows personal tokens there | The token type further endpoints accept by default |

For a CI job, ask an administrator for a service-account token with only the permissions the job needs, and store it as a masked, protected CI/CD variable named `TROVE_TOKEN`.
