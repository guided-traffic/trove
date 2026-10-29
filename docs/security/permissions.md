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

`system.bootstrap_anonymous_read: true` lets an anonymous caller hold `settings:read` on the default endpoint, while that
endpoint admits anonymous requests ([access.go](../../internal/app/access.go), `bridgeAccess`). It opens no repository
action, it is refused with an `https` base URL other than localhost, and every other permission goes to the engine. The
exposure it causes is the one recorded as
[H-63](roles-and-trust-boundaries.md#h-63--anonymous-read-of-installation-facts-and-the-replica-list-until-identity-ships).

## Audit

Refusals are recorded as `authz.denied` (asynchronous, deduplicated per replica, principal, action, repository and path);
policy and role changes are recorded in the transaction of the change, so no change exists without its audit record, and the
events that remove access (`authz.policy.updated`, `authz.policy.deleted`, `authz.role.unbound`) carry the journal entry that
a restore replays (R-AUTHZ-031). Token values, hashes and claims are never part of an event.

## Database footprint

The engine's statements read `authz_policy*`, `authz_role_binding`, `repositories`, `principal` and `cluster_epoch`; the
service writes the five `authz_*` tables and, through `cluster.Changes`, `cluster_epoch` and `cluster_change`. The build runs
as a read-only transaction on the request pool, never on the control connection, so a slow build cannot delay the poll or the
heartbeats. See [privilege-footprint.md](privilege-footprint.md) for the grants of the runtime role.

## What this does not cover

### H-101 · Anonymous denials are deduplicated by repository and path, not by client address

`audit.record_anonymous_denials` is off by default. When it is on, the denial deduplication of
[spec R-AUTHZ-029](../spec/authorization.md) keys an anonymous refusal by repository and path, because the client address is
not part of `authz.Subject`. A scanner that probes many paths therefore produces one first event per path and replica and
window instead of one per address. Live only with that switch on; its effect is bounded by the audit buffer
(`audit.async_buffer`), which drops events and counts them when full. The deduplication state is 10,000 keys per replica;
past it a refusal is written without being collapsed.

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

### H-104 · The commit-time re-check of writes has no caller yet

R-AUTHZ-015 requires a write to be decided again when it commits, against the index of the committing replica. `Decide` can
be called at that point and `RecordAllowed` records the allowed write in the transaction, but the writers (`/files/`, formats)
do not exist in this tree. Dormant until the files area ships: an upload that started before a grant was removed would commit
unless the writer re-checks.

### H-105 · The revocation journal is written and not replayed

The events that remove access carry the journal entry of R-AUTHZ-031, and the comparator `narrows` is not implemented
because no restore exists. Dormant until the backup and restore work (M5): a restore to an earlier time would bring back a
removed grant or role binding.
