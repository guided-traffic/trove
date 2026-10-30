# Valkey client

The Valkey client lives in [`internal/cluster`](../../internal/cluster) (files `valkey*.go`) and is the only code that imports valkey-go ([R-ARCH-002](../spec/architecture.md), enforced by `internal/archtest`). Specs: R-ARCH-030 (usage map), R-ARCH-031 (client), R-HA-056 (connection), R-HA-057 (degraded mode), Q-HA-13 (readiness).

Valkey is never the only copy of anything. A caller treats every error of the client, `ErrUnavailable` or not, as "run the fallback".

Nothing outside `internal/cluster` reads or writes a Valkey key (Q-ARCH-16: nothing served and no positive authorization decision comes from Valkey). The raw commands, the key-family registration and key forming are unexported; other packages get typed mechanisms whose code lives in this package (in M0: the signals and the change messages of `signal_*.go` and `changelog*.go`). The `Bus` interface of [`signal_bus.go`](../../internal/cluster/signal_bus.go) that the change log and the signals use has only unexported methods (`publish`, `subscribe`, `channel`), so no other package can implement or call it; `RegisterChannelFamily` is likewise unexported. `TestValkeySurface` pins the exported method set of `*Valkey` and bans the exported family registrations from the untagged build. `/readyz?verbose=1` shows `string(ValkeyStatus.ReasonCode)` (`valkeyCheck` in [`wire_valkey.go`](../../internal/app/wire_valkey.go)); a start-up error keeps the code of the failed connection, so a rejected credential reads `auth_failed` while the first readiness stays held back.

## Files

| File | Responsibility |
|---|---|
| [`doc.go`](../../internal/cluster/doc.go) | Package documentation |
| [`valkey.go`](../../internal/cluster/valkey.go) | `NewValkey`, lifecycle, `Available`/`Status`/`StartupError`, connect loop, monitor (primary changes, health probe), version check, start-up trust warnings |
| [`valkey_ops.go`](../../internal/cluster/valkey_ops.go) | The commands trove needs (`get`, `set`, `setNX`, `setIfEq`, `delIfEq`, `incr`, `publish`, `ping`), unexported, behind breaker and command timeout |
| [`valkey_keys.go`](../../internal/cluster/valkey_keys.go) | Key and channel family registry (`registerKeyFamily`, `registerChannelFamily`), `Key`/`Channel` types, `formKey`/`formKeyTTL`/`channel`, keyspace |
| [`valkey_ratelimit.go`](../../internal/cluster/valkey_ratelimit.go), [`limiter.go`](../../internal/cluster/limiter.go) | The rate-limit counters (key family `rl`) and the limiter with its per-replica fallback, see [Rate-limit counters](#rate-limit-counters) |
| [`valkey_flight.go`](../../internal/cluster/valkey_flight.go) | `FlightKey`, `FlightLock` (`Acquire`, `Wait`), `FlightHold` (extension, `Release`, `Lost`), the shared flight subscriptions, see [FlightLock](#flightlock) |
| [`valkey_negative.go`](../../internal/cluster/valkey_negative.go) | `NegativeStore` (`Lookup`, `Record`): the entries of the negative cache in Valkey, see [Negative store](#negative-store) |
| [`valkey_pubsub.go`](../../internal/cluster/valkey_pubsub.go) | `subscribe`, the one Pub/Sub connection and its resubscription |
| [`valkey_reason.go`](../../internal/cluster/valkey_reason.go) | `ReasonCode`, the fixed vocabulary of `ValkeyStatus.ReasonCode` |
| [`valkey_integration.go`](../../internal/cluster/valkey_integration.go) | Compiled only with the `integration` build tag: `cluster.Raw(v)` and `RegisterTestKeyFamily`/`RegisterTestChannelFamily` for `test/integration/valkey`; no build of the binary has it |
| [`valkey_sentinel.go`](../../internal/cluster/valkey_sentinel.go) | Sentinel probe (`trove_valkey_sentinels_reachable`) and startup classification (Q-HA-13) |
| [`valkey_breaker.go`](../../internal/cluster/valkey_breaker.go) | Circuit breaker |
| [`valkey_errors.go`](../../internal/cluster/valkey_errors.go) | Error kinds (`trove_valkey_errors_total{kind}`) and what counts for the breaker |
| [`valkey_tls.go`](../../internal/cluster/valkey_tls.go), [`valkey_auth.go`](../../internal/cluster/valkey_auth.go) | TLS modes and the CA bundle and client pair read again for every handshake; credential files read per connection attempt |
| [`valkey_metrics.go`](../../internal/cluster/valkey_metrics.go), [`valkey_version.go`](../../internal/cluster/valkey_version.go) | `trove_valkey_*` families; the 9.0 floor |

## Connections and discovery

Per replica: up to four pipelined connections to the primary (`trove:<instance>:main`), one Pub/Sub connection (`trove:<instance>:pubsub`) and one connection to the current Sentinel (`trove:<instance>:sentinel`). Pipelined connections open lazily, so an idle replica shows one; under load four. Measured on the integration stack: 4 main + 1 pubsub = 5 on the primary after 3,200 writes from 64 goroutines.

valkey-go resolves the primary through the Sentinels, checks `ROLE`, follows `+switch-master` and reconciles every `valkey.sentinel.refresh_interval` (`TopologyRefreshInterval`). The poll is the bound; the event only speeds it up. Verified: with `SUBSCRIBE` denied to the Sentinel user the client reaches the new primary 1.3 s after the Sentinels announce it (interval 2 s); with events 0.1 s.

The Sentinel probe is separate: every refresh interval, every configured Sentinel is asked concurrently (each within `sentinel_query_timeout`) over a short-lived connection. The client keeps a single Sentinel connection.

## Start-up

`NewValkey` returns an error only for settings it cannot run with (unreadable credential/CA/certificate file, invalid mode, duplicate metrics registration). It waits for the first connection attempt, bounded by timeouts, so `StartupError()` is meaningful. Outcomes:

| Situation | `Available()` | `Status().ReasonCode` | `StartupError()` |
|---|---|---|---|
| Connected, Valkey 9.0+ | true | `""` | nil |
| No attempt finished yet; refused or reset connection | false | `connecting` | nil |
| Every Sentinel unreachable | false, reason `no Sentinel reachable (0 of N): ...` | `no_sentinel` | nil (Q-HA-13) |
| Connection attempt ran into a deadline | false | `timeout` | nil |
| Unknown `primary_name` on every reachable Sentinel | false | `config_error` | non-nil |
| `WRONGPASS`/`NOAUTH`/`NOPERM` (data node or Sentinel) | false | `auth_failed` | non-nil |
| Certificate rejected | false | `config_error` | non-nil |
| Server below 9.0 (HELLO `version`) | false, reason `valkey 8.1.11 is not supported; trove requires 9.0 or later` | `unsupported` | nil |

Later states: `circuit_open` (breaker open), `probe_failed` (last health probe failed), `closed` (after `Close`). `Status().DegradedReason` is the free text: it can name addresses and server replies, so it goes to the log (`valkey degraded` carries `reason_code` and `reason`) and the authenticated replica list; `/readyz?verbose=1` on the ops listener shows only the code (`cluster.ReasonCodes()` lists them).

**TLS material reload** (R-HA-056, R-ARCH-035). `newValkeyTLS` holds a `tlsfiles.Reloader` for `valkey.tls.ca_file`
(or the system roots) and one for `cert_file`/`key_file`. valkey-go carries only a base `tls.Config` as the "TLS on"
marker; the dial function (`dialFn(resolver, prepare)`) turns it into the configuration of each handshake, data nodes
and Sentinels alike: the CA pool read now, the client certificate read when the server asks for it, the hand-written
chain check of `verify-ca` against this handshake's pool. A file that does not load at start fails `NewValkey`; one
that breaks later keeps the last good material and is counted (`trove_tls_reload_errors_total{kind="valkey"}`).
Connections are recycled every `max_conn_lifetime`, so a rotated CA or client certificate is in use within that
lifetime even without reconnects. `cluster.WithTLSMetrics` passes the families, see [tls.md](tls.md).

**Start-up warnings.** In Sentinel mode `NewValkey` logs one WARN each when `valkey.sentinel.password_file` is unset (anyone who reaches a Sentinel can name another primary) and when TLS verifies against the system roots (`valkey.tls.ca_file` unset: a primary a Sentinel names passes with any publicly issued certificate for that name). The configuration has no strictness switch that turns them into errors. `tls.mode: verify-ca` without `tls.ca_file` is refused by the configuration check and by `NewValkey`: verify-ca skips the host name, and with the system roots any public certificate would pass.

The client keeps retrying with `reconnect.*` backoff; `StartupError` clears when a later attempt connects. The readiness registry holds back only the first readiness on a non-nil `StartupError`.

## Key registry

Every key is `<key_prefix><keyspace>:<mechanism>:v<version>[:<part>...]`; channels the same. The keyspace is the first 12 hex digits of `system_state.keyspace_id` (`WithKeyspaceID` or `SetKeyspaceID`, set once).

- A `Key` can only be created by `v.formKey(family, parts...)` or `v.formKeyTTL(family, ttl, parts...)`. A family exists only with a positive maximum TTL (`registerKeyFamily` panics otherwise), and the key carries its TTL. The write methods take a `Key` and set the expiry themselves, so a write without a TTL cannot be expressed. The zero `Key` and a zero family are rejected. `incr` sets the expiry once (`EXPIRE ... NX` in the same `MULTI`).
- Parts must match `[A-Za-z0-9_-]{1,128}`: identifiers and hashes, never raw names.
- Values and payloads are limited to 1 KiB (`ErrValueTooLarge`).
- There is no `Del`: the ACL user has none. A malformed value is removed with `DelIfEq`.

### Add a key family

1. The mechanism lives in `internal/cluster` (a `valkey_<mechanism>.go` file) and exports a typed API (`FlightLock.Acquire`, `NegativeStore.Lookup`, …), never a key or a raw command. Register its family at package level there: `var fam = registerKeyFamily("lock:fetch", 1, MaxFlightLockTTL)` (mechanism lower-case words separated by `:`; version is the encoding version; the duration is the maximum TTL). For a channel, `registerChannelFamily`.
2. Form keys with `v.formKey(fam, parts...)`; pass a shorter TTL with `formKeyTTL` where the TTL is configured.
3. Use only the methods of `valkey_ops.go`. If a new command is needed, it must be on the R-ARCH-031 list and in the ACL rule of `test/integration/conf/users.acl`; add the unexported method to `valkey_ops.go` and its wrapper to `valkey_integration.go`, its label to the command list of R-OPS-007.
4. Add the row to the usage table of R-ARCH-030 and a test with the R- ID in the subtest name.
5. A changed encoding gets a new version number, never an edited one.

(R-ARCH-030 names `internal/cluster/keys.go` for the registry; the family registry is in `valkey_keys.go` because that package is shared with other work. The families of each milestone sit between `// >>> M<n> <area>` and `// <<< M<n> <area>` markers there: `lock:fetch`, `fill` and `neg` in `M2 proxy`.)

## Rate-limit counters

[`limiter.go`](../../internal/cluster/limiter.go) and [`valkey_ratelimit.go`](../../internal/cluster/valkey_ratelimit.go) are the
limiter of R-ARCH-034. It is the one mechanism that other packages use through a typed API: the management API's classes
(`api_read`, `api_write`, `api_anonymous`, R-API-016) and, from the identity area, the brute-force counters of R-AUTH-038
(`auth.credential_failures`, `auth.flow_starts`, `auth.device_code_failures`).

```go
set, err := cluster.NewLimiterSet(cluster.LimiterOptions{
    Valkey: vk,                  // nil: every limiter runs its fallback
    Registry: reg,               // trove_ratelimit_decisions_total, trove_ratelimit_fallback
    LiveReplicas: live.Get,      // n: live replicas of this replica's role, refreshed with the heartbeat
})
l := set.NewLimiter("api_read", cluster.ScopeCluster, 50 /* per second */, 200 /* burst */)

v := l.Take(ctx, bucket)         // counts the request, then decides
v.Allowed; v.Remaining; v.RetryAfter; v.Reset; v.Limit; v.Window; v.Mode

l.Allow(ctx, bucket)             // Take reduced to a bool

// Count-only form (failures are counted after the request is processed, the check comes before it):
if l.Exceeded(ctx, bucket) { /* refuse */ }
l.Count(ctx, bucket)             // one failure
```

- `NewLimiter(name, scope, rate, burst)` is a method of the set, not a package function (Q-ARCH-38): the limiter needs the
  Valkey client, the registry and the live-replica count. The name is lower-case words separated by `_` or `.`; it panics
  on a bad or duplicate name or a window above 12 h. The window is `burst ÷ rate` seconds, at least 1 s.
- Scope `cluster` counts in Valkey (key family `rl`, version 1, `rl:v1:<limiter>:<bucket hash>:<window index>`, expiry
  two windows; the limiter name has `.` written as `-`; the bucket is a SHA-256, never a raw name). A `Take` is one round
  trip, `MULTI`, `INCR`, `EXPIRE … NX`, `GET` of the previous window, `EXEC`, and no script. The request is counted before
  it is decided, so refused requests use up budget. `Exceeded` is a pipelined `GET` of both windows, `Count` the `INCR` part.
  The window index comes from the replica's wall clock; clock skew between replicas of some milliseconds moves a window
  boundary by that much.
- Scope `replica` counts in memory and never touches Valkey.
- **Fallback.** When `Valkey.Available()` is false (client down, circuit open, probe failed) or a call fails, the check is
  answered from a local token bucket with rate `rate ÷ n` and burst `max(1, burst ÷ n)`; no Valkey I/O is attempted while the
  client says it is unavailable, so an open breaker adds no delay, and a failing call has already cost at most
  `valkey.command_timeout` (and counts for the breaker). The count-only form falls back to local sliding-window counters with
  the limit `max(1, burst ÷ n)`. Local buckets are bounded (`MaxFallbackBuckets` per limiter, idle buckets dropped first); a
  dropped bucket starts full.
- **Metrics.** `trove_ratelimit_decisions_total{limiter,mode,outcome}`, `trove_ratelimit_fallback{limiter}` (1 from the first
  fallback decision until a decision used Valkey again, and while Valkey is unavailable), and
  `trove_valkey_degraded_total{mechanism="rate_limit"}`.
- The verdict carries what the `429` and the `RateLimit` fields need: `RetryAfter` is computed from the same counters
  (whole seconds, at least 1), `Remaining` is `burst` minus the window's estimate, `Reset` the time to the end of the
  window. During the fallback they describe the replica's share.

Tests: [limiter_test.go](../../internal/cluster/limiter_test.go) (fake storage and clock: the 300-of-1000 bound over three
replicas with and without storage, the 4 s / 5 s windows, Retry-After, count-only, fallback bounds) and
[test/integration/api](../../test/integration/api/ratelimit_test.go) (three replicas against the real Valkey, the client
closed, a Sentinel that does not answer, and `CLIENT PAUSE WRITE` for the delay bound).

## FlightLock

[`valkey_flight.go`](../../internal/cluster/valkey_flight.go) is the cross-replica lock of R-ARCH-033 and the Valkey half of the collapse protocol of R-REPO-026 (INV-11). The proxy reaches it only through the `FlightLock` interface and never sees a key.

```go
f := cluster.NewFlightKey(repoID, canonicalPath, variant) // sha256(id 0x00 path 0x00 variant), 64 hex digits
lock := cluster.NewFlightLock(vk, cluster.FlightLockOptions{
    TTL: ttl,                // cluster.flight_lock_ttl (30 s)
    PollInterval: poll,      // proxy.lock_poll_interval_ms (1 s)
    InstanceUID: instance,   // empty: the instance name of vk
    Registerer: reg,         // trove_flight_lock_lost_total
})                           // vk == nil: every call is ErrUnavailable

h, ok, err := lock.Acquire(ctx, f)  // SET lock:fetch:v1:<f> <token> NX PX <ttl>
// errors.Is(err, cluster.ErrUnavailable): fetch under the in-process single-flight only
// !ok: another holder; lock.Wait(ctx, f), then repeat the proxy's checks
defer h.Release(ctx, cluster.OutcomeStored, traceparent) // DELIFEQ, then PUBLISH fill:v1:<f>
h.Lost()                            // an extension found the key gone or foreign
```

- **Key and token.** The lock is `<prefix><ks>:lock:fetch:v1:<f>` (family `lock:fetch`, longest expiry `MaxFlightLockTTL` = 10 min; the actual expiry is the configured TTL, capped there); the outcome channel is `<prefix><ks>:fill:v1:<f>`. The token is `<instance uid>.<16 random bytes, base64url>` and is new for every acquisition. `<f>` is the full SHA-256 (Q-ARCH-7); the repository id enters it as decimal digits.
- **Extension.** `Acquire` starts one goroutine per hold (Q-ARCH-196). Every TTL/3 it runs `SET key token IFEQ token PX ttl`. A call that fails is tried again at the next tick. A call that finds the key gone or owned by another token marks the hold lost, counts `trove_flight_lock_lost_total` once and ends the loop; the fetch carries on, and the hold never deletes the other holder's key. The goroutine is bound to the Valkey client, not to the request: `Close` ends it, a cancelled client request does not.
- **Release.** Stops the extension, runs `DELIFEQ` with the token, then publishes the outcome (`stored` or `failed`, with the leader's `traceparent` after a space when it is a short printable token, Q-ARCH-195). It runs on a context that is not cancelled with the caller's, and does nothing when called again. After a loss the token no longer matches, so a late `Release` deletes nothing of the new holder.
- **Wait.** Registers the waiter on the flight's channel first, then `GET`s the key; no key returns `OutcomeGone` at once. Otherwise the waiter sleeps until the outcome message, the poll interval, a re-subscription of the Pub/Sub connection (`OnSubscribed(reconnect=true)`, after a primary change or a broken connection: every waiter checks its key) or the end of `ctx` (returned as `ctx`'s error). After each wake-up other than a message it checks the key again. The subscription reaches the server a moment after the registration, and a message published in that gap is missed; the poll covers it, as it covers a dropped message (Q-ARCH-15).
- **Shared subscriptions.** The waiters of one flight on a replica share one `Valkey.subscribe` registration: 50 waiters cost one `SUBSCRIBE`, and the last one leaving removes it. The outcome is handed to the waiters from the Pub/Sub reader goroutine without blocking.
- **Errors.** Every failed Valkey call, an open breaker and a missing keyspace included, returns an error that wraps `ErrUnavailable` and counts `trove_valkey_degraded_total{mechanism="flight_lock"}`; with the breaker open there is no network I/O. A context that ended returns its own error. A `FlightKey` that is not from `NewFlightKey` is refused with a plain error.
- **What lives elsewhere.** The in-process single-flight group in front of `Acquire`, the bypass reasons (`trove_flight_lock_bypass_total`), the re-check after `Wait` and the shutdown rule (release with `failed` 5 s before the deadline) belong to the proxy engine (`internal/proxy`, `flight.go`); none of it is in this package.

## Negative store

[`valkey_negative.go`](../../internal/cluster/valkey_negative.go) holds the Valkey half of the negative cache (R-REPO-028, R-ARCH-030).

```go
store := cluster.NewNegativeStore(vk)          // vk == nil: every call is ErrUnavailable
err := store.Record(ctx, repoID, f, cluster.NegativeEntry{Status: 404, SentAt: sentAt}, notFoundTTL)
e, found, err := store.Lookup(ctx, repoID, f)  // found == false: none, or a malformed one that is now deleted
```

- **Key and value.** `<prefix><ks>:neg:v1:<repo id>:<f>` (family `neg`, longest expiry `MaxNegativeTTL` = 24 h + 5 s), value `{"s":404,"c":<Unix ms>}`. The expiry of a written key is the `ttl` argument, the repository's `notFoundTtlSeconds`; `ttl <= 0` writes nothing, a `ttl` above the maximum is an error, and a status other than 404 or 410 is refused.
- **Form only.** `Lookup` decodes strictly: `s` is 404 or 410, `c` is above 0, no other field, nothing after the value. A value that fails is removed with `DELIFEQ` of the value that was read (the ACL user has no `DEL`, and a writer that replaced it meanwhile keeps its entry) and returned as not found. The period, the clock window and the invalidations that cover a path are the proxy's (Q-ARCH-197), and so is the per-replica L1 (`proxy.negative_l1_entries`), a registered cache of `internal/proxy`.
- **Metrics.** `trove_valkey_cache_lookups_total{cache="negative",result}` with `hit` (a well-formed entry was returned), `miss`, `rejected` (malformed) and `unavailable`; `trove_valkey_degraded_total{mechanism="negative_cache"}` for every call that met an unavailable Valkey. An entry that the proxy refuses after the lookup is counted by the proxy as a further `rejected` through `Valkey.CountCacheLookup`.
- **Errors** as for the flight lock: they wrap `ErrUnavailable`, and the caller answers from its L1.

## Breaker and degraded mode

After `circuit_breaker.failure_threshold` consecutive failures the breaker refuses calls (`ErrUnavailable`, no I/O) for `open_duration`, then lets one probe through. Failures are timeouts, connection errors, `LOADING`-class replies, `OOM`. Not counted: caller cancellation, other server replies, and `READONLY` (the node was demoted; the refresh moves the client on, and opening the circuit would add `open_duration` to the failover bound). The health probe (`SET` of `probe:v1:<instance>`, 30 s TTL; `PING` while no keyspace is set) runs every `health_check_interval` through the breaker and is what closes it again without traffic.

`Available()` is false while: no client is connected, the server is unsupported, the circuit is open, or the last probe failed. Transitions log once (WARN degraded, INFO recovered).

## Pub/Sub

`subscribe` (unexported, reached through the sealed `Bus` interface of signal_bus.go) registers locally and never fails because Valkey is down. One goroutine owns the connection: it subscribes every wanted channel, and reconnects when the connection breaks or the primary the client is bound to changes (checked every 250 ms, so a demoted primary that keeps its connection cannot hold the subscription). `OnSubscribed(fn)` and `OnPrimaryChange(fn)` are the hooks for the change-log poll and flight-lock waiters. Handlers run on the reader goroutine and must not block.

## Metrics

`trove_valkey_available`, `_circuit_open`, `_sentinels_reachable`, `_pubsub_subscribed`, `_primary_changes_total`, `_errors_total{kind}`, `_command_duration_seconds{command}`, `_degraded_total{mechanism}` (`CountDegraded`), `_cache_lookups_total{cache,result}` (`CountCacheLookup`). Also `trove_flight_lock_lost_total`, registered on the `Registerer` of `FlightLockOptions` and not in the client's own registry.

## Tests

- Unit (`go test -race ./internal/cluster/...`): the flight lock and the negative store against `memServer` ([valkey_flight_fake_test.go](../../internal/cluster/valkey_flight_fake_test.go): an in-memory server with `SET` `NX`/`PX`/`IFEQ`, `GET`, `DELIFEQ`, `PUBLISH` and RESP3 `SUBSCRIBE` pushes that can lose a key, drop messages and cut the Pub/Sub connection; [valkey_flight_test.go](../../internal/cluster/valkey_flight_test.go), [valkey_negative_test.go](../../internal/cluster/valkey_negative_test.go)), breaker with a fake clock, error classification (real server errors via a fake RESP server in `valkey_fake_test.go`), key registry, version decision, startup classification, reason codes, start-up warnings, TLS modes with real handshakes, credential rotation, the client against fake servers (8.1 refusal, WRONGPASS, unreachable Sentinels, breaker), and the exported surface (`valkey_surface_test.go`).
- Integration (`make integration-up`, then `make test-integration`; package [`test/integration/valkey`](../../test/integration/valkey)): connection names and count, wrong name/password/CA with their reason codes, failover with and without `+switch-master` (the stack is returned to its layout), Pub/Sub, key TTL scan, breaker via `CLIENT PAUSE WRITE`, the flight lock ([flight_test.go](../../test/integration/valkey/flight_test.go): acquire, extension past the TTL, release and the outcome message, 50 waiters on two replicas with one subscription each, a dropped message completed by the poll, a killed holder taken over within TTL + poll, the breaker open, a lock deleted on the new primary after a forced failover with at most two holders, and a waiter woken by the re-subscription after a primary change), the negative store ([negative_test.go](../../test/integration/valkey/negative_test.go)) and a throwaway Valkey 8.1 (refusal; image pinned in `tools/versions.env`). The stack runs Valkey 9.1, the only line tested while trove is in development. The tests reach the raw commands through `cluster.Raw(v)` (integration build tag only).
- The tests that create a Sentinel ACL user (`nosub`) delete it at the end.
