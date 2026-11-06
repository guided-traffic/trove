# Sign-in and identities

This page explains how people and machines become known to trove. It covers the OpenID Connect sign-in, how the identity provider's claims become users and group memberships, how sessions behave, the local break-glass administrator, anonymous access and the protection against password and token guessing. It also describes the Users, Groups and Service accounts screens and the commands for recovery.

## Where the settings live

trove has no password database. People sign in through an OpenID Connect identity provider such as Keycloak, Dex or GitLab; the one exception is the break-glass administrator. Machines use tokens of a service account.

| What | Where it is set | When a change applies |
|---|---|---|
| Identity providers, claim mapping, group synchronization | Configuration file, `auth.oidc` | When a replica starts |
| Login page behaviour | Configuration file, `auth.login_page` | When a replica starts |
| Session lifetimes and the recent sign-in window | Configuration file, `auth.sessions` | When a replica starts |
| Break-glass account | Configuration file, `auth.break_glass` | When a replica starts; the password file is re-read when it changes |
| Anonymous access ceiling | Configuration file, `auth.anonymous.allowed` | When a replica starts |
| Anonymous access switch, token lifetimes | **Admin area › System › Settings** | Within seconds, on every replica |
| Users, groups, memberships, service accounts | **Admin area › Identity** | Within seconds, on every replica |

The **Authentication settings** screen under **Admin area › System › Settings** also shows a read-only **Status (read only)** section: each identity provider with **discovery document loaded** or **not available on this replica**, whether the break-glass account is enabled or locked and, when it is enabled but cannot verify a password, an error that says why (see [The break-glass administrator](#the-break-glass-administrator)), the session timeouts and whether HTTP Basic with a token is allowed. Reading the screen needs the `settings:read` capability, saving needs `settings:write` and a recent sign-in. The token lifetimes on the same screen are described in [Managing tokens](tokens.md).

## Configure an OpenID Connect provider

### Register trove at the provider

1. Set `server.base_url` to the address people use, for example `https://trove.example.com`. A provider cannot be configured without it.
2. At the identity provider, create a confidential client for the authorization code flow.
3. Register the redirect URI `https://trove.example.com/auth/oidc/<provider-id>/callback`, where `<provider-id>` is the key of the provider in `auth.oidc.providers`.
4. Register the post-logout redirect URI `https://trove.example.com/ui/logged-out` if the provider supports RP-initiated logout.
5. Make sure the ID token or the userinfo answer carries the username claim and, for group synchronization, a groups claim.

trove uses the authorization code flow with PKCE, `state` and `nonce`, and checks `iss` on the callback. The specification is [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html).

### Provider settings

```yaml
server:
  base_url: https://trove.example.com            # example
auth:
  login_page:
    auto_redirect: true                          # default: go straight to the provider when exactly one is configured; /ui/login?auto_sign_in=false shows the page anyway
  oidc:
    providers:
      corp:                                      # example: the provider ID, ^[a-z0-9][a-z0-9-]{0,31}$
        order: 0                                 # default: sorts the sign-in buttons
        display_name: Corporate SSO              # example; required
        issuer: https://login.example.com/realms/acme   # example; required, https
        client_id: trove                         # example; required
        client_secret_file: /var/run/secrets/trove/oidc-corp/client-secret  # example
        client_auth_method: client_secret_basic  # default: client_secret_basic | client_secret_post | none
        scopes: [openid, profile, email]         # default; must contain openid
        rp_initiated_logout: true                # default
```

<details><summary>All provider keys</summary>

| Key | Default | Meaning |
|---|---|---|
| `order` | `0` | Position of the sign-in button; equal values sort by ID |
| `display_name` | none, required | Text of the **Sign in with** button |
| `issuer` | none, required | The issuer URL; `https` (plain `http` only for a loopback host), no query, no fragment |
| `client_id` | none, required | The client ID at the provider |
| `client_secret_file`, `client_secret_env` | none | The client secret, as a file path or the name of an environment variable; exactly one for `client_secret_basic` and `client_secret_post`, none for `none` |
| `client_auth_method` | `client_secret_basic` | How trove authenticates at the token endpoint |
| `scopes` | `[openid, profile, email]` | Requested scopes; add the scope that releases the groups claim if your provider needs one |
| `extra_auth_params` | `{}` | Extra authorization request parameters; the parameters trove sets itself are refused |
| `allowed_signing_algs` | `[RS256, PS256, ES256]` | Accepted ID token algorithms; supported are `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`; `none` and `HS*` are always refused |
| `trusted_audiences` | `[]` | Audiences besides the client ID that the ID token may carry |
| `clock_skew` | `60s` | Tolerance for `exp` and `nbf`, 0s to 5m |
| `max_id_token_age` | `10m` | Oldest accepted `iat`, 1m to 24h |
| `use_user_info` | `true` | Read the userinfo endpoint; claims missing from the ID token are taken from it |
| `discovery_refresh` | `24h` | How often the discovery document is fetched again, at least 1m |
| `rp_initiated_logout` | `true` | On sign-out, send the browser to the provider's end-session endpoint when it advertises one |
| `claims.username`, `claims.email`, `claims.display_name`, `claims.groups` | `preferred_username`, `email`, `name`, `groups` | Claim names; a dotted path reads nested objects, for example `realm_access.roles` |
| `username_prefix` | empty | Put in front of every mapped username |
| `sync_username` | `true` | Rename the user when the mapped username changes |
| `jit` | `true` | Create a user at the first sign-in |
| `require_verified_email` | `false` | Refuse a sign-in whose `email_verified` is not true |
| `login_requires_groups` | `[]` | Admit only identities whose group claim holds one of these values |
| `public_issuer` | `false` | Mark an issuer where anyone can register; `https://gitlab.com` and `https://accounts.google.com` count as public always |
| `group_sync.enabled` | `true` | Synchronize group memberships at every sign-in |
| `group_sync.on_missing_claim` | `clear` | `clear`, `keep` or `deny` when the groups claim is missing or unusable |
| `group_sync.create_groups` | `false` | Create trove groups that a rule names but that do not exist |
| `group_sync.rules` | `[]` | Rules that map claim values to trove group names |

</details>

### The client secret

The secret is never written into the configuration file. `client_secret_file` is read again whenever the file changes, so a rotated secret applies without a restart; `client_secret_env` is read at start. With the Helm chart, list the provider under `secrets.oidc` with the Kubernetes Secret that holds the value: the chart mounts it on the web pods and sets `client_secret_file` for you. See [Configuration](configuration.md).

### Check the provider

Open **Admin area › System › Settings**. Under **Identity providers**, every provider should show **discovery document loaded**. A provider whose discovery failed shows **not available on this replica**, and its button on the sign-in screen is disabled with **Currently unavailable**.

## Map claims to users and groups

### Usernames and profiles

At every sign-in trove reads the username claim, puts `username_prefix` in front, normalizes it to Unicode NFC lower case and checks it: it must match `^[a-z0-9][a-z0-9._@+-]{0,127}$` and must not be `anonymous`, `authenticated`, `system`, `me` or the break-glass username. Email and display name are updated from the claims at every sign-in, which replaces corrections an administrator made.

The first sign-in of an identity is resolved in this order:

1. The identity (issuer and subject) is already linked to a user: that user signs in.
2. An administrator pre-provisioned a user with the same username and a pending link to this provider: the link is completed.
3. The username is taken by another user: the sign-in fails with `username_taken`.
4. `jit` is true: a new user is created and joins every group with auto-join.
5. Otherwise the sign-in fails with `not_provisioned`.

When the mapped username of a known identity changes and `sync_username` is true, the user is renamed if the new name is free. Grants follow the user, because policies reference principals by ID.

### Who may sign in

- `require_verified_email: true` refuses identities whose email is not verified.
- `login_requires_groups` admits only identities whose groups claim contains one of the listed values, compared exactly.
- For a public issuer, trove refuses to start if `jit` is true and `login_requires_groups` is empty, because every account of that issuer would become a user. A `match_regex` rule must then start with a literal namespace ending in `/`, such as `acme/`, because anyone can create groups there.
- A disabled user cannot sign in.

### Group synchronization

The groups claim may be a string or a list; the first 1000 values are read. Each rule maps claim values to a trove group name. A rule has exactly one of `match` (exact, case-sensitive) and `match_regex` (anchored to the whole value; `group` may use `$1` or `${name}`). Without rules, no group is synchronized.

```yaml
auth:
  oidc:
    providers:
      corp:
        claims:
          groups: groups                         # default
        group_sync:
          enabled: true                          # default
          on_missing_claim: clear                # default: clear | keep | deny
          create_groups: false                   # default
          rules:
            - match: trove-admins                # example: exact claim value
              group: platform-admins             # example: trove group name
            - match_regex: "acme/team-(.+)"      # example
              group: "team-$1"                   # example
```

At every sign-in, the user's memberships from this provider become exactly the groups the rules produce. Only groups that already exist are joined, unless `create_groups` is true. Memberships added by hand or by auto-join are never touched. A result that is not a valid group name is skipped and logged. When the claim is missing, `clear` removes the provider's memberships, `keep` leaves them, and `deny` refuses the sign-in; the user's detail page then shows a warning that the last sign-in carried no usable group claim.

A membership change at the provider reaches trove only at the user's next sign-in. The sync can remove the last member of a group that holds the `admin` role; keep the break-glass account or a service account with `admin` for recovery.

## Administrators from the Helm values

To decide in your GitOps repository which group of your identity provider administers trove, list the role bindings under `config.authz.role_bindings` in the chart values. The migration Job (`trove migrate`) applies the list on every `helm install` and `helm upgrade`, after the schema steps and in one transaction. The group name is the trove group your group-sync rule produces, not the claim value.

```yaml
config:
  auth:
    oidc:
      providers:
        dex:                                          # example: the provider ID
          group_sync:
            rules:
              - match: wds18:k8s-main-admins          # example: the value the provider sends in the groups claim
                group: gh-wds18-k8s-main-admins       # example: the trove group
  authz:
    role_bindings:
      - role: admin                                   # example: admin | security-admin | auditor | repository-admin | webhook-admin
        group: gh-wds18-k8s-main-admins               # example; or user: <name>, or service_account: <name>; exactly one
```

- A group that does not exist yet is created as a local group, so the binding holds before anyone has signed in. At sign-in, group synchronization puts the members in; `create_groups` is not needed.
- A declared user or service account must exist. If it does not, the Job fails and its log names the entry: `kubectl -n trove logs job/trove-migrate`.
- An unknown role, an entry with two principals or none, and `anonymous` or `authenticated` fail the Job before it touches the database, and the message names the key, for example `authz.role_bindings[0].role "root" is not a system role`.
- **Admin area › Access › System roles** shows these bindings with the source **Configuration** and without **Unbind**; the API answers `409 role_binding_managed_by_configuration`. Bindings you made on that screen stay as they are. A binding you made there and then add to the list is taken over by the configuration.
- An entry you remove from the list is unbound at the next upgrade. The Job refuses to remove the last administrator while the break-glass account is disabled (`last_admin`). To hand administration from one group to another, add the new group first and remove the old one in a later upgrade, after a member of the new group has signed in, or keep `secrets.breakGlass` set.
- A group with auto-join cannot be declared (`auto_join_admin_conflict`).
- Deleting a declared user or group on the **Users** or **Groups** screen removes its binding until the next upgrade, which creates a declared group again (empty until its members sign in) and fails for a declared user.
- Every change is in the audit log as `authz.role.bound` or `authz.role.unbound` with the actor `system:configuration`.

Whoever can change the membership of the declared group at your identity provider decides who administers trove. With a GitHub team behind Dex, that is the team's maintainers and the organization's owners. A membership change there reaches trove at the member's next sign-in.

To check an upgrade, look for the line `role bindings of the configuration applied` in the Job's log, then sign in as a member of the group: the **Admin** button appears in the top bar.

## Sessions

A browser sign-in creates a server-side session. The session cookie and the CSRF cookie are `__Host-` cookies; the session lives in PostgreSQL, so any replica can serve the next request.

```yaml
auth:
  sessions:
    idle_timeout: 1h                             # default; 5m to 24h
    absolute_timeout: 12h                        # default; 5m to 168h
    max_per_user: 20                             # default; one more ends the oldest
    recent_auth_window: 1h                       # default; 1m to 24h
```

- **Idle and absolute timeout.** A session ends after `idle_timeout` without a request, and `absolute_timeout` after its creation, whichever comes first. `idle_timeout` must not exceed `absolute_timeout`.
- **Recent sign-in.** Actions that mint credentials or widen permissions need a sign-in within `recent_auth_window`: creating and rotating tokens (service account tokens included), changing permission policies and role bindings, changing exposure rules, changing the authentication settings, deleting a repository and changing package owners. Otherwise the UI shows **Confirm it is you** and **Sign in again**; what you entered is kept. Callers with a token are not asked.
- **CSRF.** Every unsafe request authenticated by the session cookie needs the `X-CSRF-Token` header and a same-origin `Origin`. The UI sends both. Scripts should use a token instead of the cookie.
- **Sign-out.** Signing out ends the session and, with `rp_initiated_logout`, also sends the browser to the provider's end-session endpoint. The browser ends on **Signed out**.
- **Ending sessions.** People see and end their own sessions on their profile. An administrator ends all sessions of a user with **End all sessions** on the user's page (`identities:write`). Disabling or deleting a user ends the sessions too. Every end is in force on all replicas within `auth.revocation_bound` (2 seconds by default).

## The break-glass administrator

The break-glass account is the one local account. It holds the `admin` role without a role binding and exists for the first setup and for outages of the identity provider.

1. Create a Kubernetes Secret that holds a plaintext password of at least 20 characters or an Argon2id hash in PHC string format.
2. Name it in the Helm value `secrets.breakGlass.existingSecret`. Without it, the chart sets `auth.break_glass.enabled: false`.
3. Sign in at `https://trove.example.com/ui/login?auto_sign_in=false`, or set `show_on_login_page: true` to show the username and password form on the sign-in screen.

```yaml
auth:
  break_glass:
    enabled: true                                # default
    username: admin                              # default
    password_file: /var/run/secrets/trove/break-glass/password   # example; the chart sets it
    show_on_login_page: false                    # default
    idle_timeout: 15m                            # default
    absolute_timeout: 1h                         # default
    rotation_hold: 2m                            # default
    lockout:
      threshold: 5                               # default
      window: 15m                                # default
      duration: 15m                              # default
```

- **Lockout.** `threshold` failed attempts within `window`, from any address, lock the account for `duration`; during the lock even the right password is refused. Anyone who can reach the sign-in can keep the account locked, so keep it disabled outside recovery or restrict `/auth/local/` at your edge. An administrator clears a lock with **Unlock** on the account's user page, or with `trove admin break-glass unlock`.
- **Rotation.** Replace the password in the Secret. When a replica notices the new file, all break-glass sessions end and local sign-in is refused for `rotation_hold`. A file without a usable password refuses every local sign-in.
- **No password file.** An enabled account without `password_file` starts, logs a warning and refuses every local sign-in. The settings screen shows **Enabled.** and, below it, the error **No password is configured: `auth.break_glass.password_file` is empty.** The API field is `status.breakGlass.passwordStatus` of `GET /api/v1/settings/auth`: `ok`, `not_configured`, `unusable` (a rotated file that holds no usable password; the screen says **The password file is not usable on this replica**) or `not_applicable` (the account is disabled). The value describes the replica that answers; a replica that has not yet noticed a rotation can still say `ok`. Test a local sign-in before you rely on the account.
- **Disable it** once an administrator can sign in through the identity provider: remove `secrets.breakGlass` and roll the release.

## Anonymous access

Requests without a credential act as the built-in principal `anonymous`. They are admitted only when all of these hold:

1. `auth.anonymous.allowed` is true (the default) in the configuration.
2. **Allow requests without a credential to read what the anonymous principal may read** is switched on under **Admin area › System › Settings**. It is off on a fresh installation.
3. The endpoint the request arrives on admits anonymous requests; see [Endpoints and exposure rules](endpoints.md).
4. A permission policy grants `read` to `anonymous` on the repository, which must not have **Require authentication** set; see [Permissions](permissions.md#anonymous-and-all-signed-in-users).

Anonymous callers only read: on the management API they get safe methods only. With the switch on, the sign-in screen shows **Continue without signing in**.

## Brute-force protection

| Key | Default | Effect |
|---|---|---|
| `auth.rate_limit.failures_per_address` | `300` | Rejected credentials per minute and client address, cluster-wide; above it, credentials not seen recently get 429 |
| `auth.rate_limit.recent_token_window` | `10m` | A token validated within this window is still checked while its address is throttled |
| `auth.rate_limit.starts_per_address` | `30` | Sign-in starts and local sign-in attempts per minute and address; above it, 429 |
| `auth.rate_limit.max_pending_flows` | `100000` | Unfinished sign-ins across the cluster; above it, 503 `too_many_pending_logins` |
| `auth.rate_limit.exempt_cidrs` | `[]` | Addresses or CIDRs that are never throttled |

An IPv6 address counts per `/64`. The break-glass lockout applies on top of these limits.

## The Users screen

**Admin area › Identity › Users** lists every user with **User name**, **Display name**, **Email**, **Origin**, **Status** and **Last sign-in**. Filter by **Name or email**, **Status** (**Active**, **Disabled**, **Locked**), **Origin**, **Group** and **Administrator**. **Origin** is **Identity provider** for users created at first sign-in, **Created by an administrator**, or **Break-glass**. Reading needs `identities:read`, changing needs `identities:write`.

To pre-provision a user, select **Add user** and fill in:

| Field | Meaning |
|---|---|
| **User name** | Required; lower-case letters, digits and `. _ @ + -`. It must equal the username the provider will map |
| **Display name**, **Email** | Optional; replaced at the first sign-in |
| **Link to the identity provider** | Shown when providers are configured; creates a pending link that the first sign-in completes. **No link yet** leaves it unlinked |

A user's page shows **Attributes**, **Identities** (**Linked** or **Pending first sign-in**), **Groups** with the **Source** of each membership (**Manual**, **Auto-join**, **Identity provider**), the user's system roles, **Tokens** and **Sessions**. You can:

- correct **Display name** and **Email** with **Save correction**;
- add a manual membership with **Add to group**, or **Remove** a manual one; memberships from the provider or auto-join cannot be removed here;
- **Disable** the user: sessions end, tokens stop working, and **Also revoke all tokens of this user** (checked by default) revokes them for good. **Enable** reverses a disable, but not a revocation;
- **Delete** the user with tokens, sessions, identity links, memberships, policy entries and role bindings. A later sign-in of the same identity creates a new user with no grants, if `jit` allows it;
- **End all sessions**.

Nobody can disable or delete their own account here. Disabling or deleting the last active holder of `admin` is refused with `last_admin` while the break-glass account is disabled. Changing a user or group that holds a system role needs `roles:write` as well.

## The Groups screen

**Admin area › Identity › Groups** lists **Name**, **Description**, **Origin** and **Members**. Select **New group** and enter:

| Field | Meaning |
|---|---|
| **Name** | Required; lower-case letters, digits and `. _ -`, at most 128 characters |
| **Description** | Optional |
| **Every user joins this group automatically** | Auto-join: users created afterwards become members with source **Auto-join**; existing users are not added |

A group's page lets you rename it, change the description and auto-join flag (**Save group**), **Delete group**, add members with **Add a member** (a username, or `sa:<name>` for a service account of no project; a service account of a project never joins a group, `project_account_not_groupable`), and **Remove** manual memberships. It also lists the group's **System roles** and the **Permission policies naming this group**. A group that holds a system role cannot be auto-join (`auto_join_admin_conflict`), and an auto-join group cannot be made a member of a project (`principal_not_allowed`): every user is in it.

## The Service accounts screen

A service account is a non-human principal for CI systems and automation. Its tokens act as the account, not as a person, and it is never part of `authenticated` and never a member of a project. A service account belongs to no project, `sa:<name>`, or to one project for good, `sa:<projectKey>/<name>`; the name is unique among the accounts of its project, and among those of no project, so `sa:partner` and `sa:web/partner` are two accounts.

**Admin area › Identity › Service accounts** lists **Name**, **Description** and **Status**. Select **New service account** and enter a **Name** (lower-case letters, digits and `-`, two to 63 characters) and a **Description**. On the account's page you can **Save description**, **Disable** (with **Also revoke all tokens of this account**, unchecked by default), **Enable**, **Delete** the account with its tokens, see its system roles and manage its **Tokens**; see [Managing tokens](tokens.md). Grant it rights with a permission policy entry `sa:<name>`, or add it to a group.

A service account of a project is created and managed by the Owners of the project, and by holders of `identities:write`, under `/api/v1/projects/<projectKey>/service-accounts/<name>`; it is granted only by its project's policies and holds no system role. `GET /api/v1/service-accounts` lists the accounts of every project too, each with its `project`, and filters by `project=`; the operations on `/api/v1/service-accounts/<name>` address the accounts of no project only. See [Service accounts of a project](projects.md#service-accounts-of-a-project).

## Commands for bootstrap and recovery

The `trove admin` commands run against the database with the replica's configuration and write their own audit events.

| Command | Purpose |
|---|---|
| `trove admin break-glass status`, `unlock`, `end-sessions` | Inspect, unlock or sign out the break-glass account |
| `trove admin sessions end --user <username>` or `--all` | End sessions |
| `trove admin service-account create <name> [--description <text>] [--admin]` | Create a service account, optionally with the `admin` role |
| `trove admin token create --service-account <name> [--ttl 15m] [--scope-file <file>]` | Print a token of a service account |
| `trove admin user disable <username> [--keep-tokens]` | Disable a user |

<details><summary>Reasons on the "Sign-in failed" page</summary>

| Reason | Meaning |
|---|---|
| `flow_expired` | The sign-in took longer than 10 minutes or the link was used twice |
| `browser_mismatch`, `provider_mismatch`, `issuer_mismatch` | The callback does not belong to the sign-in this browser started |
| `idp_error` | The provider answered with an error, shown on the page |
| `token_exchange_failed`, `userinfo_failed` | The provider's token or userinfo endpoint failed |
| `id_token_invalid`, `id_token_expired`, `nonce_mismatch`, `userinfo_mismatch` | The ID token failed a check; compare `allowed_signing_algs`, `clock_skew` and the clocks |
| `username_invalid` | The username claim is missing or maps to an invalid or reserved name |
| `username_taken` | Another user holds the mapped name |
| `not_provisioned` | `jit` is false and no user is pre-provisioned |
| `user_disabled` | The user is disabled |
| `email_unverified` | `require_verified_email` refused the identity |
| `not_in_allowed_group` | `login_requires_groups` refused the identity |
| `groups_claim_missing` | `on_missing_claim: deny` and no usable groups claim |
| `reauth_user_mismatch` | A recent sign-in was confirmed with a different identity |
| `provider_unavailable`, `unavailable`, `provisioning_failed` | The provider's discovery or trove's database did not answer |

Every attempt is recorded as `auth.login.succeeded` or `auth.login.failed` in the audit log.

</details>
