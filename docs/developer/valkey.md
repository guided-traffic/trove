# Valkey client

The Valkey client lives in [`internal/cluster`](../../internal/cluster) (files `valkey*.go`) and is the only code that imports valkey-go ([R-ARCH-002](../spec/architecture.md), enforced by `internal/archtest`). Specs: R-ARCH-030 (usage map), R-ARCH-031 (client), R-HA-056 (connection), R-HA-057 (degraded mode), Q-HA-13 (readiness).

Valkey is never the only copy of anything. A caller treats every error of the client, `ErrUnavailable` or not, as "run the fallback".

## Files

| File | Responsibility |
|---|---|
| [`doc.go`](../../internal/cluster/doc.go) | Package documentation |
| [`valkey.go`](../../internal/cluster/valkey.go) | `NewValkey`, lifecycle, `Available`/`Status`/`StartupError`, connect loop, monitor (primary changes, health probe), version check |
| [`valkey_ops.go`](../../internal/cluster/valkey_ops.go) | The commands trove needs (`Get`, `Set`, `SetNX`, `SetIfEq`, `DelIfEq`, `Incr`, `Publish`, `Ping`) behind breaker and command timeout |
| [`valkey_keys.go`](../../internal/cluster/valkey_keys.go) | Key and channel family registry, `Key`/`Channel` types, keyspace |
| [`valkey_pubsub.go`](../../internal/cluster/valkey_pubsub.go) | `Subscribe`, the one Pub/Sub connection and its resubscription |
| [`valkey_sentinel.go`](../../internal/cluster/valkey_sentinel.go) | Sentinel probe (`trove_valkey_sentinels_reachable`) and startup classification (Q-HA-13) |
| [`valkey_breaker.go`](../../internal/cluster/valkey_breaker.go) | Circuit breaker |
| [`valkey_errors.go`](../../internal/cluster/valkey_errors.go) | Error kinds (`trove_valkey_errors_total{kind}`) and what counts for the breaker |
| [`valkey_tls.go`](../../internal/cluster/valkey_tls.go), [`valkey_auth.go`](../../internal/cluster/valkey_auth.go) | TLS modes; credential files read per connection attempt |
| [`valkey_metrics.go`](../../internal/cluster/valkey_metrics.go), [`valkey_version.go`](../../internal/cluster/valkey_version.go) | `trove_valkey_*` families; the 9.0 floor |

## Connections and discovery

Per replica: up to four pipelined connections to the primary (`trove:<instance>:main`), one Pub/Sub connection (`trove:<instance>:pubsub`) and one connection to the current Sentinel (`trove:<instance>:sentinel`). Pipelined connections open lazily, so an idle replica shows one; under load four. Measured on the integration stack: 4 main + 1 pubsub = 5 on the primary after 3,200 writes from 64 goroutines.

valkey-go resolves the primary through the Sentinels, checks `ROLE`, follows `+switch-master` and reconciles every `valkey.sentinel.refresh_interval` (`TopologyRefreshInterval`). The poll is the bound; the event only speeds it up. Verified: with `SUBSCRIBE` denied to the Sentinel user the client reaches the new primary 1.3 s after the Sentinels announce it (interval 2 s); with events 0.1 s.

The Sentinel probe is separate: every refresh interval, every configured Sentinel is asked concurrently (each within `sentinel_query_timeout`) over a short-lived connection. The client keeps a single Sentinel connection.

## Start-up

`NewValkey` returns an error only for settings it cannot run with (unreadable credential/CA/certificate file, invalid mode, duplicate metrics registration). It waits for the first connection attempt, bounded by timeouts, so `StartupError()` is meaningful. Outcomes:

| Situation | `Available()` | `StartupError()` |
|---|---|---|
| Connected, Valkey 9.0+ | true | nil |
| Every Sentinel unreachable | false, reason `no Sentinel reachable (0 of N): ...` | nil (Q-HA-13) |
| Unknown `primary_name` on every reachable Sentinel | false | non-nil |
| `WRONGPASS`/`NOAUTH`/`NOPERM` (data node or Sentinel) | false | non-nil |
| Certificate rejected | false | non-nil |
| Server below 9.0 (HELLO `version`) | false, reason `valkey 8.1.11 is not supported; trove requires 9.0 or later` | nil |

The client keeps retrying with `reconnect.*` backoff; `StartupError` clears when a later attempt connects. The readiness registry holds back only the first readiness on a non-nil `StartupError`.

## Key registry

Every key is `<key_prefix><keyspace>:<mechanism>:v<version>[:<part>...]`; channels the same. The keyspace is the first 12 hex digits of `system_state.keyspace_id` (`WithKeyspaceID` or `SetKeyspaceID`, set once).

- A `Key` can only be created by `Valkey.Key(family, parts...)` or `KeyTTL(family, ttl, parts...)`. A family exists only with a positive maximum TTL (`RegisterKeyFamily` panics otherwise), and the key carries its TTL. The write methods take a `Key` and set the expiry themselves, so a write without a TTL cannot be expressed. The zero `Key` is rejected. `Incr` sets the expiry once (`EXPIRE ... NX` in the same `MULTI`).
- Parts must match `[A-Za-z0-9_-]{1,128}`: identifiers and hashes, never raw names.
- Values and payloads are limited to 1 KiB (`ErrValueTooLarge`).
- There is no `Del`: the ACL user has none. A malformed value is removed with `DelIfEq`.

### Add a key family

1. Register it at package level in the owning package: `var fam = cluster.RegisterKeyFamily("lock:fetch", 1, 30*time.Second)` (mechanism lower-case words separated by `:`; version is the encoding version; the duration is the maximum TTL). For a channel, `RegisterChannelFamily`.
2. Form keys with `v.Key(fam, parts...)`; pass a shorter TTL with `KeyTTL` where the TTL is configured.
3. Use only the methods of `valkey_ops.go`. If a new command is needed, it must be on the R-ARCH-031 list and in the ACL rule of `test/integration/conf/users.acl`; add the method to `valkey_ops.go`, its label to the command list of R-OPS-007.
4. Add the row to the usage table of R-ARCH-030 and a test with the R- ID in the subtest name.
5. A changed encoding gets a new version number, never an edited one.

(R-ARCH-030 names `internal/cluster/keys.go` for the registry; the family registry is in `valkey_keys.go` because that package is shared with other work.)

## Breaker and degraded mode

After `circuit_breaker.failure_threshold` consecutive failures the breaker refuses calls (`ErrUnavailable`, no I/O) for `open_duration`, then lets one probe through. Failures are timeouts, connection errors, `LOADING`-class replies, `OOM`. Not counted: caller cancellation, other server replies, and `READONLY` (the node was demoted; the refresh moves the client on, and opening the circuit would add `open_duration` to the failover bound). The health probe (`SET` of `probe:v1:<instance>`, 30 s TTL; `PING` while no keyspace is set) runs every `health_check_interval` through the breaker and is what closes it again without traffic.

`Available()` is false while: no client is connected, the server is unsupported, the circuit is open, or the last probe failed. Transitions log once (WARN degraded, INFO recovered).

## Pub/Sub

`Subscribe` registers locally and never fails because Valkey is down. One goroutine owns the connection: it subscribes every wanted channel, and reconnects when the connection breaks or the primary the client is bound to changes (checked every 250 ms, so a demoted primary that keeps its connection cannot hold the subscription). `OnSubscribed(fn)` and `OnPrimaryChange(fn)` are the hooks for the change-log poll and flight-lock waiters. Handlers run on the reader goroutine and must not block.

## Metrics

`trove_valkey_available`, `_circuit_open`, `_sentinels_reachable`, `_pubsub_subscribed`, `_primary_changes_total`, `_errors_total{kind}`, `_command_duration_seconds{command}`, `_degraded_total{mechanism}` (`CountDegraded`), `_cache_lookups_total{cache,result}` (`CountCacheLookup`).

## Tests

- Unit (`go test -race ./internal/cluster/...`): breaker with a fake clock, error classification (real server errors via a fake RESP server in `valkey_fake_test.go`), key registry, version decision, startup classification, TLS modes with real handshakes, credential rotation, the client against fake servers (8.1 refusal, WRONGPASS, unreachable Sentinels, breaker).
- Integration (`make integration-up`, then `make test-integration`; package [`test/integration/valkey`](../../test/integration/valkey)): connection names and count, wrong name/password/CA, failover with and without `+switch-master` (the stack is returned to its layout), Pub/Sub, key TTL scan, breaker via `CLIENT PAUSE WRITE`, a throwaway Valkey 8.1 (refusal; image pinned in `tools/versions.env`) and 9.0 (API under the example ACL rule). The stack's own line (9.1 or 9.0) is not switched by these tests.
- The tests that create a Sentinel ACL user (`nosub`) delete it at the end.
