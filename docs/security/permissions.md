# Permissions

Status: implemented in M1 for the decision engine, the compiled grant index and its propagation, the token scope as a
ceiling, system roles, the epochs of `Trove-Authz-Min-Epoch` and the decision audit. Package ownership, the cache-fill and
shadowing checks, CI inline grants and projects do not exist yet; they are named in
[What this does not cover](#what-this-does-not-cover).

This page says what keeps a request from doing more than it was granted, how long a removed grant can still work, and what
an attacker who controls one of the inputs gains. The mechanics are in
[developer/authorization.md](../developer/authorization.md); the rules are in [spec/authorization.md](../spec/authorization.md).

## Where a decision is made

```text
 client ──► edge ──► authentication stage ──► handler ──► authz.Engine ──► compiled grant index (replica memory)
                      (credential → Principal)            Decide / Require        ▲
                                                                                  │ rebuilt from PostgreSQL
                                                         cluster.Changes ─────────┘ (epochs; Valkey only hints)
```

- The only code that says allow or deny is `authz.Engine` ([decide.go](../../internal/authz/decide.go)); the management API
  asks it through the shared step of [authorize.go](../../internal/api/v1/authorize.go), so no operation reaches its handler
  without the check its declaration names, and an operation without a declaration stops the start.
- Handlers receive the subject the authentication stage built. A token carries no group list and no administrator flag:
  groups are resolved at request time and roles come from the role-binding table (R-AUTHZ-002, R-AUTHZ-010).

## What narrows and what widens

| Mechanism | Effect | Where |
|---|---|---|
| Deny by default | A request with no grant, no role and no `content:bypass` is refused; a fresh installation has no policy | `evaluate` |
| Allow-only grants | A grant only adds; an exclude pattern removes only inside its own resource set | `BuildIndex`, `globSet.match` |
| No implication | `deploy` does not imply `read`, `manage` does not imply `deploy`; presets are expanded when written | [action.go](../../internal/authz/action.go) |
| Token scope | Intersected with the owner's rights at use time; never grants; a malformed element admits nothing | `scopeAdmits` |
| Roles through tokens | A token exercises roles only with `admin: true` (and only if the owner holds `admin` at that moment) or through `system` items the owner's roles grant; `content:bypass` is never an item | `capabilities` |
| `authenticated` and `anonymous` | Entries hold `read` only; `anonymous` entries select repositories by key only; neither holds a role; a service account and a CI token are never `authenticated` | `Service.entryRules`, `Index.principalIDs` |
| Endpoint ceiling | Consulted first for every action and every subject, `content:bypass` included; nil is none | [ceiling.go](../../internal/authz/ceiling.go) |
| Virtual repositories | `read` on the virtual repository is a gate, not a source of content rights; anonymous passes only if it reads every member | `evaluate` |

Principals are referenced by their immutable ID in `authz_policy_entry` and `authz_role_binding`, so a deleted and
re-created user, group or service account of the same name inherits nothing, and a repository deleted and re-created under
the same key is not covered by the old key selector (the policy reports `inert`).

## What a refusal reveals

- A refused read is `DenyHidden`. `Decision.Err()` returns `authz.NotFound()`, the one error that missing resources use too,
  and authorization runs before any lookup, so neither the body nor the time depends on existence.
- A refused write is 403 naming the action and the coordinate the caller sent, whether or not the target exists.
- An anonymous caller is always 401 (reads, writes, a partially readable virtual repository), so the status does not depend
  on what exists.
- A management operation is checked before any lookup (`Require`), so its 403 does not depend on the object.
- `authz.hide_existence: false` (R-AUTHZ-017) turns the refusal of a read of an existing repository into 403. It is a
  troubleshooting switch; the endpoint-exposure 404 and the anonymous 401 do not change.

## How long a removed grant keeps working

The index is rebuilt from a `REPEATABLE READ READ ONLY` snapshot labelled with the epochs of `authz` and `repo_config` and
their `changed_at` values, so no build combines a policy's entries from one commit with its selectors from another, and an
epoch number reused after a PostgreSQL failover that lost commits is told apart by `changed_at` and flushed.

| Situation | Bound |
|---|---|
| Replica whose change-log poll works | the next poll (`cluster.epoch_poll_interval`) plus one build; with Valkey typically about 100 ms plus one build |
| Valkey stopped, messages lost or forged | the same bound; a message never advances the applied epoch and never confirms an index |
| Replica cut off from PostgreSQL, or whose build fails | never longer than `authz.cache.max_staleness` (10 s) plus one build: after that the replica stops deciding, waits like a request waits for the database, and answers 503 `authz_stale` |
| Client that sent `Trove-Authz-Min-Epoch` with the epoch of its change | the change is in force on the replica that answers, or the request waits or fails (503, 412) |

A holder of a valid credential can keep using a removed right for that long and no longer. Revoking the credential ends it
on the timeline of the identity area, independently of the index.

`Trove-Authz-Min-Epoch` can only make a replica wait or refuse; it cannot widen a permission. A malformed value is 400, an
anonymous request ignores the header, so the epoch cannot be probed without a credential, and the replica reads the
database epoch at most every 50 ms however many requests carry an unknown epoch.

## What Valkey can do to authorization

Nothing is written to Valkey or read from it by `internal/authz`. The change log delivers Valkey messages to the engine's
listener as hints; the engine reads the epochs from PostgreSQL before it builds. Whoever can write under trove's Valkey prefix
can make a replica run extra epoch reads (at most 20 per second per replica) and can delay a change by dropping messages
until the poll applies it. They cannot add a grant, suppress an audit event or make a replica decide on older grants than the
bound above.

## The dev-profile bridge

`system.bootstrap_anonymous_read: true` lets an anonymous caller hold `settings:read` for two operations, the system
information and the replica list, on the default endpoint, while that endpoint admits anonymous requests
([access.go](../../internal/app/access.go), `bridgeAccess`, `bootstrapPath`; the endpoint stage hands it the request path).
The other operations of `settings:read` (the scheduled tasks, the encryption status, the authentication settings) stay
closed (Q-AUTHZ-71; `TestBootstrapAnonymousReadOnlyOnDefaultEndpoint`). It opens no repository action, it is refused with an
`https` base URL other than localhost, and every other permission goes to the engine. The exposure it causes is the one
recorded as
[H-63](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships).

## Audit

Refusals are recorded as `authz.denied` with the outcome `denied`: asynchronous, sent by the engine without any collapsing and
deduplicated by the audit writer per replica, principal, required action, repository and path, and for an anonymous caller per
client address (R-OPS-015, Q-AUTHZ-24); the address and the trace ID are added to the event by the recorder wrapper of the
composition root from the request context. Policy and role changes are recorded in the transaction of the change, so no change
exists without its audit record, and the events that remove access (`authz.policy.updated`, `authz.policy.deleted`,
`authz.role.unbound`, and `authz.settings.changed` when a repository update tightens or loosens an authorization setting) carry
the journal entry that a restore replays (R-AUTHZ-031). Token values, hashes and claims are never part of an event.

## The policy and role operations

Reading policies and bindings needs `policies:read` and `roles:read`, writing them `policies:write` and `roles:write` (the
auditor holds the reads, the administrator all four). Every write answers `Trove-Authz-Epoch`, and an authenticated caller may name
that epoch in `Trove-Authz-Min-Epoch` on its next request to see the change on whichever replica answers (R-AUTHZ-032;
`TestR_AUTHZ_032_MinEpochOverHTTP` shows it with Valkey stopped and a 30 s poll). `POST /api/v1/authz/check` and `GET
/api/v1/authz/me` tell a caller about its own credential only; a repository the caller cannot see answers `allowed: false`, the
same as one that does not exist. A role is bound to users, groups and service accounts only, and a group that takes members
automatically takes no role: binding a role and turning on auto-join both lock the group's row before each checks the other,
so the two cannot pass at the same moment (`TestR_AUTH_019_ABindingWaitsForAutoJoinOfTheGroup`,
`TestR_AUTH_019_AutoJoinWaitsForARoleBindingOfTheGroup`).

Who holds a role is changed only by a caller that may bind roles (R-AUTHZ-011, Q-AUTHZ-69). A change of a group that carries
any role (its members from either side, its name, description or auto-join flag, its deletion) and of a user or service
account that holds one directly or through a group (profile, identity links, disable, enable, delete) asks the decision point
for `roles:write` in the transaction of the change ([guards.go](../../internal/auth/principal/guards.go), `guardRoleHolder`;
`Caller.RoleHolders` set by the API in [auth_services.go](../../internal/api/v1/auth_services.go)). Without it,
`identities:write` would reach every capability: a security administrator joining an administrator group, a token scoped to
`identities:write` adding its owner, a pending identity link on an administrator claimed at the next login, a rename that lets
the identity provider's sync fill the group (`TestR_AUTHZ_011_IdentitiesWriteDoesNotReachTheRoles`). Ending sessions and
revoking tokens stay with `identities:write` and `tokens:revoke`; they only take access away.

`last_admin` (R-AUTHZ-022, Q-AUTHZ-70) counts the active users and service accounts that hold `admin`, not the bindings, and
it guards every change that can end the role: unbinding it, disabling or deleting a holder, deleting its group, removing it
from the group ([guards.go](../../internal/auth/principal/guards.go), `LastAdminGuard`). Each such change locks the `admin`
binding rows first and counts in a statement of its own afterwards, so two of them on different replicas run one after the
other (`TestR_AUTHZ_022_ConcurrentUnbindsLeaveAnAdministrator`, `TestR_AUTHZ_022_LastAdministratorAcrossTheIdentityOperations`).
The guard steps aside while the break-glass administrator is enabled.

## Database footprint

The engine's statements read `authz_policy*`, `authz_role_binding`, `repositories`, `principal` and `cluster_epoch`; the
service writes the five `authz_*` tables and, through `cluster.Changes`, `cluster_epoch` and `cluster_change`. The build runs
as a read-only transaction on the request pool, never on the control connection, so a slow build cannot delay the poll or the
heartbeats. See [privilege-footprint.md](privilege-footprint.md) for the grants of the runtime role.

## What this does not cover

### H-102 · A caller that decides several coordinates without `Begin` can straddle an index swap

`Decide` takes the installed index at the moment of the call. A request that decides several coordinates (the members of a
virtual repository, a copy) must call `Engine.Begin` once and `DecideOn` per coordinate to decide on one index; a caller that
calls `Decide` repeatedly can see two indexes if a build installs in between. Dormant: no caller in M1 decides more than one
coordinate per request; it becomes live with virtual resolution (M2) if that code does not use `Begin`.

### H-103 · Package ownership, the shadowing check and the cache-fill check are not part of `Decide`

R-AUTHZ-018 (ownership), the shadowing check of R-REPO-048 and the populate check of R-AUTHZ-008 are not implemented. In M1
the only format is `generic`, which has no package names, so ownership does not apply and remote repositories cannot be
created. Dormant; it becomes live with the first package format (M2): from then on a `deploy` grant alone would let its holder
publish any name inside its patterns, including a name another team publishes, and would let a virtual repository's deploy
target shadow a remote package.

### H-104 · The commit-time re-check decides on the grants, not on the credential

R-AUTHZ-015's re-check runs for every deploy of `/files/`: after the body has arrived and before the commit transaction
opens, the write is decided again on the committing replica's index, for a new file and an identical re-upload as for an
overwrite, and the recorded decision is that one ([deploy.go](../../internal/files/deploy.go);
`TestR_AUTHZ_015_ADeployIsDecidedAgainWhenItCommits` removes the grant while the body is arriving). Two things remain.

The credential is not authenticated again: a token revoked, or a session ended, while its upload runs still commits that
upload, because the subject the re-check decides for is the one the request started with. Live for every upload; what makes
it real is a stolen token whose holder starts a large upload before the revocation. The revocation stops every later request
within the bound of R-AUTH-023.

The re-check and the commit are two steps: a grant removed in the milliseconds between them does not stop the write. The
format writers (M2) have no re-check yet; each needs its own call when it ships.

### H-105 · The revocation journal is written and not replayed

The events that remove access carry the journal entry of R-AUTHZ-031, and the comparator `narrows` is not implemented
because no restore exists. Dormant until the backup and restore work (M5): a restore to an earlier time would bring back a
removed grant or role binding.

### H-212 · The identity provider's group sync can remove the last administrator

The `last_admin` guard runs in the management operations only. The group sync of a login (R-AUTH-008) removes a membership
that the identity provider no longer reports, also the last membership in the group that carries `admin`, and the sync is
not refused (Q-AUTHZ-70, option c). Live whenever an administrator group is synchronised from the identity provider and the
break-glass administrator is disabled; what makes it real is a change of the group at the identity provider, by mistake or by
someone who administers the provider but not trove. R-HA-054 reports an installation without an administrator; recovery is
the break-glass administrator or `trove admin`.
