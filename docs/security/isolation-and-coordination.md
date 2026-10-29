# Isolation and coordination

Status: implemented in M0 for the role split of the replicas, the leases on the lease pool, the fence of singleton writes, the
change log that every replica polls, and the replica registry. Tenancy between repositories and between endpoints is not built
yet; the repository model is M2 work.

This page says which replica may hold what coordination state, how a singleton writer is kept from acting after it lost its
lease, which party can change what other replicas read, and where two installations can share a lock without knowing it.

## Who may hold coordination state

Only the `worker` and `all` roles claim jobs, open the lease pool and hold leases. The role sets are fixed in code, and the
`web` role has neither ([roles.go](../../internal/app/roles.go), `SubsystemsFor`):

| Role | Lease pool | Job loops | Change-log poll | Replica registry |
|---|---|---|---|---|
| `web` | no | no | yes | yes |
| `worker` | yes | yes | yes | yes |
| `all` | yes | yes | yes | yes |

A `web` replica therefore cannot construct a lease manager, and a request cannot take a singleton lock. A request that needs
singleton work enqueues a job, and a worker claims it ([architecture.md](../spec/architecture.md) R-ARCH-016, R-ARCH-017).

## Leases

A lease is a PostgreSQL session-level advisory lock on a connection of the lease pool ([lease_manager.go](../../internal/cluster/lease_manager.go),
the acquire path that calls `pg_try_advisory_lock`):

- The lock is taken with `pg_try_advisory_lock(category, number)`. Category `1` is the lease (`LockLease`,
  [locks.go](../../internal/cluster/locks.go)); the number identifies the singleton. Category `3` is the migration lock, taken
  as `pg_try_advisory_lock(3, 0)` ([migrate.go](../../internal/meta/postgres/migrate.go)); category `4` is the configuration
  lock, a transaction-level lock taken with `pg_advisory_xact_lock` ([locks.go](../../internal/cluster/locks.go)). Category `2`
  is retired and never reused.
- The lease pool points at the session host, which is a direct or session-mode endpoint. A session connection that lands on a
  transaction-mode pooler is refused. `sessionGuard` runs five `pg_backend_pid()` calls on one connection and fails when the
  backend changes, and a PgBouncer refusal of a startup parameter is classified as a session-state error
  ([preflight.go](../../internal/meta/postgres/preflight.go), `sessionGuard`, `refusedStartupParameter`).
- The lease manager releases every lease with `pg_advisory_unlock` on stop ([lease_manager.go](../../internal/cluster/lease_manager.go),
  `Stop`). A replica that dies releases its locks with its session.

## Fencing: a writer after its lease

A singleton write runs on the lease's own session. Before it writes, the holder checks that the session still holds the lock:
`Fence` queries `pg_locks` for the advisory lock of its category and number, owned by its own backend and granted
([lease_held.go](../../internal/cluster/lease_held.go), `Fence`). The write then runs in the same session, so a session that ended
cannot write. No token is issued: the design rejected a numbered token, and the decision is
[spec architecture.md](../spec/architecture.md) Q-ARCH-30.

The window that remains is the time between the loss of the lock on the server and the holder noticing it. It is bounded by the
keepalive settings (`database.keepalive`, `database.server_keepalive`) and by `database.lease_check_interval`. A write inside that
window fails, because the session it runs on is gone. Storage maintenance does not use leases; it uses row locks and a
hand-over rule ([storage.md](../spec/storage.md) Q-STOR-13).

## Change log: what other replicas read

Every replica polls the change log in PostgreSQL, and the poll applies every change again. Valkey carries a message per change
on a Pub/Sub channel, and a message only evicts a cache early
([changelog.go](../../internal/cluster/changelog.go), `handleMessage`): a message that is lost, late, duplicated or forged
cannot suppress a change, because it never advances the applied epoch. The design is [ADR-031](../decisions.md#adr-031--caches-validate-themselves-notifications-only-evict-early).

The consequence for isolation is that the authority over what replicas serve is the database, and an attacker with write access
to Valkey can only make replicas evict earlier than they would.

## Replica registry

`GET /api/v1/system/replicas` lists every replica with the fields of [gen.Replica](../../internal/api/v1/gen/api.gen.go):
`name`, `instanceUid`, `node`, `version`, `revision`, `roles`, `state`, `startedAt`, `heartbeatAt`, `lost`, the result of every
readiness check (`checks`, `degraded`), the names and start times of the leases the replica holds, and its Valkey view. The
registry is a table in PostgreSQL that every replica writes its own row to on each heartbeat
([replica_registry.go](../../internal/cluster/replica_registry.go)).

The registry therefore tells anyone who reads it how the installation is laid out: which nodes run it, which versions run side
by side, and which replica holds which singleton. Its fields hold no credential. In M0 nobody holds `system.read`, so the
only path to the listing is the bootstrap switch described on
[roles-and-trust-boundaries.md](roles-and-trust-boundaries.md#installation-facts-before-identity-ships).

## Transactions and a lost COMMIT

A write runs in one transaction on the main pool. When the connection breaks during `COMMIT`, the server may or may not have
applied the transaction. Trove records that outcome as unknown and does not retry it ([tx.go](../../internal/meta/postgres/tx.go),
`attempt`; [retry.go](../../internal/meta/postgres/retry.go), `outcomeUnknownError`). The decision is that the client receives
`503` ([spec architecture.md](../spec/architecture.md) Q-ARCH-24). The handler-level mapping to the code `database_unavailable`
was not traced line by line in this pass.

## Lock keys are per database

Lock keys are the pair (category, number). The key carries no schema name, and PostgreSQL advisory locks belong to the
database, not to a schema ([lease_manager.go](../../internal/cluster/lease_manager.go), the `pg_try_advisory_lock` call).
Two installations in one database with different schemas therefore compete for the same lock numbers. See
[H-76](#h-76--advisory-locks-are-per-database-not-per-schema).

## What this does not cover

### H-76 · Advisory locks are per database, not per schema

Stage: every lease and the migration lock ([lease_manager.go](../../internal/cluster/lease_manager.go),
[migrate.go](../../internal/meta/postgres/migrate.go)).

Dormant: live only when two installations share one PostgreSQL database and use different schemas. An installation's lock
then competes with the other installation's holder of the same category and number, so singleton jobs of one installation can
wait for the other's, and a migration of one can block the other. The effect is delay: each installation's writes go to its own
schema, and the lock does not fence across schemas. Subject: the singleton work and the migration lock of each installation.

What makes it real: a shared database for several installations. The chart's default is one database per installation, and the
design requires it for isolation; the code does not enforce it.

### H-77 · A lost COMMIT answers 503, and a client's retry can duplicate the write

Stage: a write request whose transaction loses its connection during `COMMIT` ([tx.go](../../internal/meta/postgres/tx.go),
`attempt`).

Live on every write whose connection breaks during `COMMIT`, which is rare but not excluded by any setting. Trove does not retry
it, and the client gets `503`. A client that retries the request after a `503` can write the same object twice, because no idempotency key exists
yet. Subject: the content of a write, which a repeated upload or a repeated management call duplicates.

What makes it real: a failover or a connection drop that coincides with a `COMMIT`, and a client that retries without checking
the state first. Content addressing makes a repeated blob upload harmless; a repeated metadata change is not.
</content>
