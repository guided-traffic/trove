# trove Bastion

Status: specified for phase 5b of the [project plan](../projektplan/BETA.md#phase-5b--trove-bastion-m13) and not built. This
page describes the design of [spec/bastion.md](../spec/bastion.md) before its code exists, because the threat model of an
instance exposed to the internet is part of that design; it changes to describe the code, with links to it, in the change that
builds each mechanism.

This page says what the internet-facing Bastion can do, what an attacker who reaches it can do, what the Bastion's own token
can do, and where abuse is stopped. The roles and boundaries of the main installation are in
[roles-and-trust-boundaries.md](roles-and-trust-boundaries.md), its tokens in [tokens-and-credentials.md](tokens-and-credentials.md)
and its permissions in [permissions.md](permissions.md); the rules are in the spec page.

## What the Bastion is trusted with

```text
 internet ──► HAProxy Ingress ──► Bastion replicas ──(Bastion token + caller's credential)──► main endpoint
 (anyone)     stage 1: coarse     stage 2: blocks, limits,                                    decides every
              per-address limit   failed-auth timeout, budget                                 answer, renders
                                  │            │                                              every byte
                                  ▼            ▼
                      own bucket or volume   Valkey (optional)
                      (bytes, signed entries) (blocks, counters, signed answers)
```

| The Bastion holds | It does not hold |
|---|---|
| The Bastion token (`trv_bst_`), read from a mounted Secret and re-read on change (R-BAS-006) | Any user, group, policy, token hash or session of the main installation (R-BAS-020) |
| The main installation's answers about callers, keyed by the SHA-256 of the presented token, for at most `bastion.answers.period` (plus the grace period while the main installation is unreachable) | A caller's token beyond the request: never on disk, never in Valkey (R-BAS-019) |
| Cached artifacts under their SHA-256, shared documents, negative entries, and per-caller documents in memory only (R-BAS-022 to R-BAS-024) | Any credential for the main installation's object store, PostgreSQL or upstream registries (R-BAS-030, R-BAS-047) |
| The signing key for its records in Valkey and the bucket (R-BAS-032) | A right to write Kubernetes Secrets or to issue tokens (R-BAS-006, R-BAS-045) |

Every decision about a caller is the main installation's. A miss is forwarded with the caller's own credential and decided by
the main installation's own route; a cached artifact is served only after a `full` read answer or a conditional request with the
caller's credential answered `304` through the real route (R-BAS-022, Q-BAS-27); a shared document only to a caller whose `full`
read the main installation confirmed (R-BAS-023). The Bastion carries no format knowledge, so there is no second classifier
whose disagreement with the main installation could serve bytes.

## What an attacker who reaches the Bastion can do

- **Without a credential.** Read what `anonymous` may read at the main installation, exactly when the main installation admits
  anonymous requests (R-BAS-019); otherwise every request answers `401` from the Bastion without reaching the main installation.
  Misses cost the attacker nothing, so they are absorbed: names the main installation does not have are answered from the
  negative cache to callers who read the whole repository (R-BAS-024), anonymous requests have stricter per-address limits and a
  per-address miss budget (R-BAS-035), and anonymous misses can hold only a share of the concurrency budget towards the main
  installation (R-BAS-025). Spread over many addresses, they still take that share (H-388).
- **With guessed or stolen tokens.** A malformed token is refused without a request (R-AUTH-022's checksum). More than 20 failed
  authentications from one address put it into a timeout in which unverified credentials are refused without a request, while
  known credentials from that address keep working (R-BAS-036). Behind the Bastion, the main installation still applies its own
  failed-credential throttle to the real client (R-BAS-017).
- **With a valid token.** Read what the token may read at the main installation, no more: the shape of the answer is the decision
  point's (R-BAS-012), a narrowed reader never receives a shared document (R-BAS-023), and a revocation at the main installation
  reaches every Bastion replica within the feed's poll interval and one round trip (R-BAS-026). During an outage of the main
  installation a revoked token reads on for the grace period (H-383).
- **Writes.** None reach the main installation as writes: the Bastion sends a bodiless probe, the main installation answers `403
  bastion_read_only` in the protocol's shape, and no upload body leaves the Bastion (R-BAS-014).
- **Learn about others.** The Bastion sends no cache state (Q-BAS-40), and negative entries go only to callers who read the whole
  repository, so a narrowed reader cannot time hidden names (Q-BAS-42). The time of a hit still tells whether a readable package
  was fetched recently (H-390).
- **Flood it.** See [Where abuse is stopped](#where-abuse-is-stopped).

## What the Bastion token can do

The token has the type `bst` and belongs to a Bastion registration, a principal that holds no grants (R-BAS-004). Every route of
the main installation except the Bastion interface refuses it by type, before any lookup, as `Authorization`, Basic password or
API key (R-BAS-005).

| On the interface it can | It cannot |
|---|---|
| Run the handshake: learn the interface version, its own expiry, whether anonymous access and download recording are on | Read a package, a document or a file: on a forwarded request only the caller's credential decides |
| Check any credential and read its shape on named repositories, for a client address it names (R-BAS-011) | Write content, change a setting, read the audit trail, issue or rotate a token |
| Read the revocation feed: token IDs, principal IDs, repository keys (R-BAS-015) | Reach any route on a further endpoint |
| Submit download events for its cached downloads, marked with its registration (R-BAS-016) | Submit any other audit event type |
| Make the main installation mark its forwarded responses and refuse writes (R-BAS-013, R-BAS-014) | Turn an anonymous forwarded request into anything but anonymous |

What its holder can still do with those rights is H-386. It is revoked like every token, journaled, and renewed by hand with an
overlap (R-BAS-006); the Bastion warns 30 days before it expires and alerts when the main installation refuses it (R-BAS-041).

## Where abuse is stopped

| Stage | What it knows | What it does | Requirement |
|---|---|---|---|
| Network provider | bandwidth | absorbs volumetric attacks, or does not (H-382) | outside trove |
| HAProxy Ingress (stage 1, switchable) | the client address | a per-address request and connection limit, set wider than the Bastion's | R-BAS-038 |
| Bastion block list (stage 2) | refusals per address, shared through Valkey | blocks an address that collects too many refusals, with durations doubling to 24 h; IPv6 per `/64` | R-BAS-037 |
| Bastion limits | credential or not, misses, failures | per-address request rates (anonymous stricter), per-address miss budgets, the failed-authentication timeout | R-BAS-035, R-BAS-036 |
| Concurrency budget | requests to the main installation | at most `max_concurrent` across replicas, anonymous at most a share; collapsed misses | R-BAS-025 |
| Main installation | the real client, through `server.trusted_proxies` | its own rate limits, throttle and audit, applied to the client, not to the Bastion | R-BAS-017 |

Without Valkey every counter and block holds per replica at the configured value divided by the chart's replica count (H-389).

## Integrity of what it serves

Artifacts are stored under their SHA-256, verified when they are filled (the withheld tail of R-REPO-041) and again while they are
served (R-STOR-013's verifying reader): a replaced object aborts the transfer and is removed (R-BAS-032). Entries, negative entries
and answers in the bucket and in Valkey are signed with HMAC-SHA-256; write access to either without the key only deletes. With the
key, it forges (H-387).

What each layer of the cache holds, and what protects it:

| Held | Where | Protected by | What write access without the key does |
|---|---|---|---|
| Entries (content address, size, headers, repository, `validatedAt`) | `entries/` | HMAC-SHA-256 over every field and the cache key (`cache.Signer`); a record that fails is ignored, deleted and counted as `stale` | Deletes; cannot point an entry at other bytes, move it to another key, or extend its validity |
| Artifact and document bytes | `sha256/`, `documents/` | the content address, checked while served by a verifying reader that withholds the last 64 KiB until the SHA-256 and the length match | Replaces: the next whole read aborts, deletes the object and the entry, and counts `trove_bastion_cache_corrupt_total`; the next request refetches. A ranged read is not verified (H-451); the replacement stays until read (H-453) |
| Access markers, modification times | `access/` | nothing | Steers the order of eviction (H-453) |
| The audit spool | `audit-spool/` | nothing; the files are events the main installation receives as reports of the Bastion's own token | Deletes events or adds events marked with the Bastion's registration (H-386's capability, from the bucket) |

A fill is spooled to a temporary directory of the volume or the pod's `tmp` and committed under its address only after its digest
was checked, so a reader never sees a partial object; the backend checks the digest and size of every `PutObject` once more. Every
name that becomes a path or a key (content address, key hash, replica, spool file) is validated as hex or a fixed form before use.
The bucket is reached over `https` with the trust bundle of the chart, and its credential is a file the replica re-reads.

## What this does not cover

The gaps of the Bastion's mechanisms are recorded on the spec page, where the mechanisms are, and only linked here:

- [H-382](../spec/bastion.md#h-382--a-volumetric-attack-on-bandwidth-is-not-absorbed-by-the-bastion) — volumetric attacks on
  bandwidth;
- [H-383](../spec/bastion.md#h-383--during-the-grace-period-a-token-revoked-at-the-main-installation-reads-on-at-the-bastion) —
  revocations during an outage of the main installation;
- [H-384](../spec/bastion.md#h-384--a-version-newly-published-appears-at-the-bastion-only-after-the-metadata-period) — new versions
  and security fixes after the metadata period;
- [H-385](../spec/bastion.md#h-385--a-withdrawal-the-feed-does-not-carry-reaches-the-bastion-within-the-artifact-revalidation-period)
  — withdrawals and automatic blocks the feed does not carry;
- [H-386](../spec/bastion.md#h-386--whoever-holds-the-bastion-token-can-test-credentials-name-client-addresses-and-add-download-events)
  — what the holder of the Bastion token can do;
- [H-387](../spec/bastion.md#h-387--the-signing-key-and-write-access-to-the-bastions-valkey-or-bucket-together-forge-answers-and-entries)
  — the signing key together with write access to Valkey or the bucket;
- [H-388](../spec/bastion.md#h-388--anonymous-misses-from-many-addresses-take-the-anonymous-share-of-the-budget) — distributed
  anonymous miss floods;
- [H-389](../spec/bastion.md#h-389--without-valkey-a-block-and-a-counter-hold-on-one-replica-only) — blocks and counters without
  Valkey;
- [H-390](../spec/bastion.md#h-390--the-time-of-an-answer-tells-a-caller-whether-others-fetched-a-package-recently) — what the
  timing of a hit tells;
- [H-391](../spec/bastion.md#h-391--download-events-lost-to-a-full-buffer-a-crash-or-an-ephemeral-spool-are-missing-from-the-audit)
  — download events lost before they reach the audit;
- [H-428](../spec/bastion.md#h-428--the-ops-listener-of-a-bastion-serves-its-metrics-without-authentication) — the ops listener's
  metrics carry no authentication;
- [H-429](../spec/bastion.md#h-429--until-the-engine-and-the-guard-are-built-a-bastion-refuses-every-read-and-limits-nothing) — a
  build of the role between the work packages;
- [H-432](../spec/bastion.md#h-432--the-edge-limit-counts-per-tcp-peer-address-and-has-no-burst) — the edge limit keys on the TCP
  peer address and has no burst;
- [H-433](../spec/bastion.md#h-433--the-egress-policy-names-the-main-installation-by-address) — the egress policy names the main
  installation by address;
- [H-434](../spec/bastion.md#h-434--a-controller-that-ignores-the-annotations-turns-the-edge-limit-and-routing-off-silently) — a
  controller that ignores the Ingress annotations turns the edge limit and routing off silently;
- [H-451](../spec/bastion.md#h-451--a-ranged-read-of-a-cached-artifact-is-not-verified) — a ranged read of a cached artifact is not
  verified;
- [H-452](../spec/bastion.md#h-452--the-entries-of-evicted-bytes-stay-until-they-are-read-and-are-not-counted-in-the-cache-size) —
  entries of evicted bytes stay until read and are not counted in the size;
- [H-453](../spec/bastion.md#h-453--objects-and-markers-are-not-signed-a-replaced-object-is-found-only-when-it-is-read-and-markers-steer-eviction)
  — objects and markers are not signed.

This page does not describe the main installation's own mechanisms that the Bastion relies on (the decision point, token
validation, the revocation journal); their pages and gaps are linked above. It records no gap of its own.
