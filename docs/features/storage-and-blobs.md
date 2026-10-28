# Storage and blob providers

This page covers how the system keeps binary content physically: content-addressed storage with
deduplication and its consequences for copy, move and delete; the checksums kept per binary and
the background job that backfills SHA-256 for old records; the binary store as a chain of
storage providers (local file system, node-local cache, database blobs, sharding with redundancy,
health-aware shards, cluster-shared node stores, asynchronous upload, retry); S3-compatible object
storage with all its settings, other object stores, migration into object storage and
storage-class compatibility; redirecting downloads to signed object-storage URLs; garbage
collection of unreferenced binaries and pruning of orphaned objects; the storage summary, disk-space
quota thresholds and storage optimization. Items, checksum exposure, deploy by checksum, signed
download links and the trash can are described in [artifacts.md](artifacts.md); the per-repository
redirect switch in [repositories.md](repositories.md); retention and cleanup policies in
[retention-and-cleanup.md](retention-and-cleanup.md); cluster topology in
[ha-and-deployment.md](ha-and-deployment.md).

## Content-addressed storage

### STOR-001 · Content-addressed binary storage with deduplication
Priority: **MVP**

Every binary is stored under its checksum, not under its repository path, and is stored once no
matter how many paths in how many repositories carry the same content. The relation between a
repository path and its binary lives only in the database.

Deploy sequence:

1. The system receives the content and computes its checksums (ART-008 in
   [artifacts.md](artifacts.md)).
2. When the store already holds content under that checksum, nothing new is written; the only
   effect is one more database record that points the repository path at the existing checksum.
3. Otherwise the binary is written to the store under a name equal to its checksum, inside a
   directory named after the first characters of the checksum, and then the path-to-checksum
   record is created.

| Aspect | Behavior | Default |
|---|---|---|
| Content address | Binaries are addressed by a checksum of their content. | SHA-1 |
| Directory layout | `<store root or object prefix>/<first N hex characters>/<full checksum>`; for example a binary whose checksum starts with `5be0c1d2` *(example)* lies in directory `5b`. | N = 2 |
| Prefix length on object storage | Configurable from 0 to 5 characters; 0 places all objects directly under the prefix (see STOR-020). | 2 |
| Scope | Applies to every binary in every repository, of every class and format. | — |

Benefits that implementations rely on: deduplication, copy/move/delete as database transactions
(STOR-002), fast checksum search because it is a database lookup (SRCH-008 in
[search-and-query.md](search-and-query.md)), and freedom of repository layout (any format's path
scheme maps onto the same store because the database is the indirection layer).

Edge cases:

- Deleting the last path that references a binary does not delete the binary; garbage collection
  does (STOR-036).
- A store's layout is the same on a local file system and in object storage, which is what makes
  a plain copy of the store directory tree into a bucket a valid migration (STOR-027).

Interactions: deploy by checksum (ART-004 in [artifacts.md](artifacts.md)) creates a path record
for an existing binary without transferring content; storage statistics distinguish physical
binaries from logical artifacts (STOR-039).

### STOR-002 · Reference-only copy, move and delete
Priority: **MVP**

Because the path-to-binary mapping lives in the database, copying an item adds a database record,
moving an item rewrites one, and deleting an item removes one. None of the three reads or writes
binary content, so their cost is that of a database transaction regardless of file size. This also
reduces write locking on the file system compared with manipulating files.

Edge cases: a delete leaves the binary in place even when no path references it any more; the
binary becomes a candidate for garbage collection (STOR-036). The trash can, when enabled, keeps the
deleted path as a record in a trash area, so the binary stays referenced until the trash retention
expires (ART-036 in [artifacts.md](artifacts.md)).

### STOR-003 · Checksum-first transfers
Priority: **P1**

Before content moves between two places (client to server on upload, server to client on
download, instance to instance on replication), the sender announces the content's checksums. If
the receiving side already holds a binary with that checksum, the bytes are not transferred, even
when the destination path differs from any path that holds the content.

Interactions: the client-facing form is deploy by checksum (ART-004 in
[artifacts.md](artifacts.md)); replication uses the same shortcut (see
[replication-and-federation.md](replication-and-federation.md)).

Unspecified: the exact negotiation on download and the behavior when the announced checksum does
not match the content that is eventually sent.

### STOR-004 · SHA-256 kept as a first-class checksum
Priority: **MVP**

Besides the addressing checksum (SHA-1 by default, STOR-001), the SHA-256 of every binary is
computed at deploy time and stored in the database next to SHA-1 and MD5. It is then available
everywhere a checksum is used:

| Surface | What SHA-256 enables |
|---|---|
| Query language | Filter by SHA-256 and receive it in results (see [search-and-query.md](search-and-query.md)). |
| Download | Returned in a response header with the file. |
| Deploy and deploy by checksum | Accepted as the client-supplied checksum (ART-002, ART-004 in [artifacts.md](artifacts.md)). |
| Folder download | SHA-256 values are returned with the downloaded folder. |
| UI | Shown on the general information tab of an item. |
| Search operations | Accepted by checksum search operations (SRCH-008). |

Edge cases: records created before SHA-256 was computed at deploy time lack the value until the
backfill job (STOR-005) has processed them; the older per-item operation that stores SHA-256 as a
property (ART-010) is kept only for backward compatibility.

### STOR-005 · SHA-256 backfill job
Priority: **P2**

A background job that adds the missing SHA-256 value to every database record that lacks it, so
that all SHA-256-based features (STOR-004) cover old content.

Algorithm:

1. Select records without SHA-256, a bounded number per query.
2. For each, look for other records with the same SHA-1.
   - If one of them already has SHA-256, copy it to the others.
   - If none has, read the binary once, compute SHA-256 and write it to all of them.
3. A record whose SHA-1 occurs nowhere else gets its SHA-256 computed from its binary.
4. Work proceeds in batches; after each batch the job pauses.

The job can run on any cluster node unless pinned to one; pinning requires the same pin setting on
every node, and only the node whose identifier matches runs it. It finishes when every relevant
record carries SHA-256.

<details>
<summary>Settings (startup configuration; restart of the instance or chosen node required)</summary>

| Setting | Meaning | Default |
|---|---|---|
| Run backfill on startup | When on, the job starts when the node starts. | off |
| Pinned node | Identifier of the only node allowed to run the job (HA only). | none (any node) |
| Rows per query | Records without SHA-256 fetched per database query. | 100 |
| Batch size | Records updated concurrently per batch before the pause. | 10 |
| Worker threads | Threads that update records; the first record of a SHA-1 may need a download, the rest reuse its result. | 2 |
| Pause after each batch | Milliseconds of sleep after each batch. | 5000 |

</details>

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Start backfill | Optional JSON: batch threshold (default 10), query limit (default 100), pause while waiting for the cluster in ms (default 5000), pause between batches in ms (default 5000), node to force the run on (default empty). | 200 with a plain-text confirmation that the job is scheduled to start; 400 when the deployment type does not support it (hosted offering); 401 without valid credentials; 403 for non-administrators. | administrator |
| Stop backfill | Query parameter: pause interval in ms, which should equal the value used at start (default 5000). | 200 with a plain-text confirmation that the job is scheduled to stop; 401; 403. | administrator |

Monitoring: the job writes its progress and failures to a log file of its own; its start, and any
failure while starting, also appear in the main service log.

Recommendations: run garbage collection (STOR-036) first, because records of
deleted but not yet collected items are also processed and would otherwise cost downloads and
time; dedicate a node with extra resources for large databases; tune workers by observed I/O and
CPU; remove the settings and restart after completion (optional).

Edge cases: available only on self-managed installations; resource intensive on large databases;
the defaults are deliberately conservative.

## Binary store configuration and provider chain

### STOR-006 · Binary store configuration
Priority: **MVP**

The binary store is configured once per installation in a dedicated store configuration that
selects a store layout (a built-in template or a custom chain, STOR-007) and overrides settings of
named providers in it.

- Default when nothing is configured: a single local file-system store (STOR-008).
- Built-in templates need only the template name plus account-specific settings (for object
  storage: bucket, prefix, endpoint or region, credentials); every other setting keeps its
  recommended default.
- A template can be modified by declaring a provider with the template's provider identifier and
  overriding individual settings (for example the store directory).
- Changes take effect on restart. In a cluster, the configuration is placed on one node and is
  propagated automatically to the other members when they restart; restarting the nodes one at a
  time applies a change without downtime.
- On startup the system logs whether it could connect to the configured object store; object
  stores additionally run a startup connection test unless disabled (STOR-020).

Edge cases: a wrong change to the store configuration can lose binaries (for example switching to
a provider that does not see the existing content); custom chains whose providers are not
compatible with each other can lose data. Moving a file-system store requires stopping the
system, copying the store directory to the new location, changing the configuration and
starting again.

Interactions: deployment-level configuration in [ha-and-deployment.md](ha-and-deployment.md); the
chart exposes the template name and the object-store settings as values (missing values take the
chart's defaults).

### STOR-007 · Composable provider chain and built-in templates
Priority: **P2**

The store is a chain of providers nested inside each other; each provider implements the same
binary interface on top of checksum-based storage (STOR-001) and delegates to the provider it
wraps. Some providers are never used alone and exist only as links in a chain.

| Provider kind | Role | Entry |
|---|---|---|
| File system | Stores binaries in a local or mounted directory. | STOR-008 |
| Node-local cache | LRU cache in front of a slower store. | STOR-009 |
| Database blob | Stores binaries as blobs in the database. | STOR-010 |
| Sharding | Spreads binaries over several sub-stores with redundancy. | STOR-011 |
| Health-aware shard | File-system or object-store shard that tracks its own availability. | STOR-012, STOR-026 |
| Cluster sharding | Sharding over the local store of each cluster node. | STOR-014 |
| Remote node | Gives one node access to another node's store over HTTP. | STOR-014 |
| Asynchronous upload | Buffers locally, uploads later. | STOR-016 |
| Retry | Retries failed reads and writes of the provider it wraps. | STOR-017 |
| Object store | S3-compatible, Google Cloud Storage, Azure Blob Storage. | STOR-019 to STOR-031 |
| Federated binaries | Pulls binaries of federated repositories from other sites. | STOR-018 |

<details>
<summary>Built-in templates (layout of each chain)</summary>

| Template (descriptive name) | Chain |
|---|---|
| Local file system (default) | file system |
| Cached file system | node-local cache → file system |
| Database with cache | node-local cache → database blob |
| Database direct | database blob |
| Object store, direct upload (recommended for S3) | node-local cache → S3 (STOR-019) |
| Object store, asynchronous upload | node-local cache → asynchronous upload → retry → S3 (STOR-016) |
| Object store, cluster | node-local cache → cluster sharding (redundancy 2, lenient limit 1, zones local and remote) → [per-node asynchronous upload → retry → S3] plus dynamic remote-node providers (STOR-014) |
| Object store, multi-bucket sharding | node-local cache → sharding → several health-aware S3 shards (STOR-026) |
| Two shards, one copy | node-local cache → sharding (redundancy 1) → two health-aware file-system shards |
| Two shards, two copies | node-local cache → sharding (redundancy 2) → two health-aware file-system shards |
| Cluster file system | node-local cache → cluster sharding (redundancy 2, zones local and remote) → local health-aware store plus dynamic remote-node providers |
| Google Cloud Storage, single and cluster | analogous to the S3 templates (STOR-030) |
| Azure Blob Storage, single and cluster | analogous to the S3 templates (STOR-031) |

</details>

Edge cases: building custom chains without expert support is discouraged; a provider's
temporary directory, where it has one, is relative to its own store directory unless given as an
absolute path (an older variant resolved it relative to the base data directory).

### STOR-008 · Local file-system store
Priority: **P2**

The basic store: binaries in a directory on a local disk or a mounted network file system, laid
out as in STOR-001.

| Setting | Meaning | Default |
|---|---|---|
| Base data directory | Root under which the system keeps data files. | the installation's data directory |
| Store directory | Root folder of binaries; absolute if it starts with `/`, otherwise relative to the base data directory. | a fixed subfolder of the base data directory (name unspecified here) |
| Temporary directory | Folder for internal temporary files; absolute if it starts with `/`, otherwise relative to the store directory under the base data directory. | `temp` |

Placement guidance:

| Store | Suitable for | Notes |
|---|---|---|
| Local file system | Single-node evaluation and development | Not safe for a cluster: a local volume is bound to one node and cannot be shared by several replicas. |
| Network file system | Self-managed clusters without object storage | Only with SSD-backed storage, latency of at most 5 ms, 6000+ IOPS and 1000 MB/s+ throughput; not for high load; inconsistent performance makes uploads and downloads fail. The application itself must not be installed on a network file system, only the binaries may live there. |
| Object storage | Production and clusters | Recommended (STOR-019); on premises without shared storage, an S3-compatible store such as MinIO is the alternative (STOR-029). |

Capacity guidance: plan at least three times the total size of the stored artifacts, so that
backups fit; above roughly 10 TB of artifacts no general figure applies and the sizing is
individual.

### STOR-009 · Node-local read cache
Priority: **P2**

A cache layer on the node's local disk in front of a slower store (object storage, network file
system, database). It is the outermost provider in the chain, closest to the application. Frequently
requested binaries are served from it; eviction is least-recently-used. In a cluster every node has
its own cache. In front of a store that is already local it brings no benefit.

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Maximum cache size | Bytes the cache may occupy. Does not include files being uploaded, which sit in a separate staging area of the cache; leave extra room for it. | 5,000,000,000 (5 GB) |
| Cache directory | Root folder of the cache; absolute if it starts with `/`, otherwise relative to the base data directory. | `cache` |
| Maximum cached file size | Binaries larger than this many bytes are never cached. | no limit |
| Skip caching on upload | When on, binaries enter the cache only when downloaded, not when uploaded. | off |
| Collapse concurrent reads | When on, parallel downloads of one binary share a single fetch from the underlying store. | on |
| Validate checksum before caching | Verifies the binary's checksum before moving it into the cache. | on |
| Temporary directory | Absolute if it starts with `/`, otherwise relative to the cache directory under the base data directory. | unspecified |
| Clean up stale staging records | Enables periodic removal of old upload-staging record files in the cache's staging area. | off |
| Staging cleanup interval | Seconds between cleanup runs. | 86,400 (1 day) |
| Staging cleanup age | Seconds after which a staging record file may be removed. | 86,400 (1 day) |

</details>

Sizing guidance: SSD, 500 GB or more, 6000+ IOPS, 1000 MB/s+; a larger cache
improves downloads of frequently used artifacts. The cache directories must exist before start.

Interactions: the cache partition is the one measured by the disk-space quota when the store is
object storage or the database (STOR-040).

### STOR-010 · Database blob store
Priority: **P2**

The database holds everything, item metadata and binary content alike, as blobs, either behind a node-local
cache (STOR-009) or without one, in which case every download is served from the database.

Limits: the largest storable artifact equals the database's maximum blob size; artifacts are not
split across blobs, so databases with a small blob limit need a different store.

Edge cases: with this store, the storage summary (STOR-039) shows the directory that caches
binaries extracted from the database, and the disk-space quota (STOR-040) measures that cache's
partition.

### STOR-011 · Sharded store with redundancy
Priority: **P2**

A store spread over M sub-stores (mounts or buckets) that keeps R copies of each binary, R ≤ M.
Losing a sub-store reduces the effective redundancy but does not stop service while enough
sub-stores remain; adding capacity means adding a sub-store, without downtime of the store, after
which redundancy is rebuilt (STOR-015). The sharding provider is never used alone.

Write path: the incoming stream is buffered and written by R parallel threads, one per copy; the
binary counts as written only when all copy threads have finished. Reads and writes pick
sub-stores according to configurable strategies.

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Redundancy | Copies per binary; must not exceed the number of sub-stores. | 1 |
| Lenient limit | Minimum number of sub-stores that must be available to accept uploads; an upload must still succeed on all available sub-stores; the next balancing cycle restores full redundancy. 0 means the full redundancy is required. Example: limit 3, four sub-stores, one down: writes continue; two down: writes stop. | 0 |
| Read strategy | Round robin over the sub-stores. | round robin |
| Write strategy | Round robin; most absolute free space; highest percentage of free space. | round robin |
| Copy wait timeout | Milliseconds a copy thread waits for the other copy threads to finish; raise it when writes fail. | 30,000 |
| Copy buffer size | Size of the buffer that accumulates the stream before it is fanned out to the copy threads; raise it when writes fail. | 32 KB |
| Balancer enabled | Turns the rebalancing mechanism on or off (STOR-015). | on |
| Maximum balancing session time | How long one balancing session may run before it pauses until the next garbage collection; 0 lets balancing run to completion. | 0 ms |
| Disable balance check on upload | When off, every upload checks how many copies of the binary exist on other sub-stores and marks surplus copies (beyond redundancy) for removal in the next balancing cycle; when on, the check and the resulting balancing are skipped. | off |
| Free-space sampling interval | Milliseconds between free-space checks used by the free-space write strategies; lower it before heavy uploads to reduce imbalance. | 36,000,000 (10 h) |
| Minimum idle writer threads | Writer threads kept alive per redundant sub-store when idle. | 2 |
| Writer idle time | Milliseconds after which idle writer threads may be stopped. | 120,000 (2 min) |
| Balancer threads | Threads used for rebalancing. | 1 |
| Availability check threads | Threads that check whether sub-stores are reachable. | 2 |
| Availability check timeout | Milliseconds to wait for a sub-store availability check. | 15,000 |

</details>

Built-in variants: two file-system shards with one copy each (capacity) and two file-system shards
with two copies (mirroring), both behind a node-local cache (STOR-007).

Edge cases: for bucket sharding, changed sharding rules (redundancy, strategies) apply only to
binaries written after the change, and existing binaries must be copied to the new shards by hand
(STOR-026); for file-system shards the optimize operation (STOR-015) restores balance after
deliberate changes such as a migration.
A configuration change needs a restart (rolling restart in a cluster, without downtime).

### STOR-012 · Health-aware shards and read-only shards
Priority: **P2**

A shard provider that knows whether its underlying disk or bucket works. After fatal errors it is
taken out of service and periodically re-activated by its parent; otherwise it behaves like the
plain store it wraps. A shard can be made read-only: it keeps serving reads and garbage collection
keeps deleting binaries from it, but no new binaries are written to it.

| Setting | Meaning | Default |
|---|---|---|
| Re-activation check period | Minimum milliseconds between attempts to re-activate a failed shard. | 15,000 |
| Write enabled | When off, the shard serves only reads and deletions by garbage collection (only under a sharding provider). | on |
| Zone | Name of the sharding zone the shard belongs to (only under a sharding provider). | unspecified |
| Store directory | Custom directory of the shard (file-system shards). | unspecified |
| Temporary directory | As in STOR-008. | unspecified |

Object-store shards have the same health settings (STOR-026).

### STOR-013 · Cross-zone sharding
Priority: **P2**

Sharding across zones or regions of one cluster, so that losing a zone keeps copies available.
Each shard carries a zone; each node declares a unique node identifier and an ordered list of
zones to write to.

| Setting | Scope | Meaning | Default |
|---|---|---|---|
| Read strategy | Sharding provider | Read from shards according to zone settings. | unspecified |
| Write strategy | Sharding provider | Inside each zone, pick either the shard with the largest free-space percentage or the shard with the most free bytes (two values). | unspecified |
| Node identifier | Node | Descriptive name, unique across the whole network. | unspecified |
| Zone write order | Node | Comma-separated zone list in write order, e.g. `zone-a,zone-b` *(example)*. | unspecified |

Behavior (example: redundancy 2, two zones, two nodes and two shards per zone, write
strategy by free-space percentage): a deploy reaches the nearest node; the first copy lands on the
shard of the first zone that has the largest free-space percentage, the second copy on the
corresponding shard of the second zone. A new node can join an already sharded cluster by
declaring its zone write order, without any change to the store configuration.

### STOR-014 · Cluster-shared node-local stores
Priority: **P2**

A cluster layout without shared storage: every node keeps its own local store and sees the stores
of all other nodes through remote-node providers that are created and removed automatically as
nodes join and leave (the provider reacts to changes in cluster membership). Binaries are written with
redundancy across nodes, reads and writes pick providers by zone (`local`, `remote`).

| Setting (cluster sharding) | Meaning | Default |
|---|---|---|
| Zones | Zones known to the read and write strategies. | unspecified (templates use `local,remote`) |
| Lenient limit | Minimum number of successful writes for an upload to succeed; the next balancing cycle restores redundancy. | 1 |
| Dynamic provider kind | Kind of provider added and removed with topology changes; only the remote-node provider is supported. | remote node |
| Redundancy | Copies per binary; at most the number of stores. | 2 |
| Read and write strategy | Cross-network strategy driven by zones (the only value). | cross-network |
| Balancer threads | Threads used for rebalancing. | 1 |
| Availability check threads | Threads that check whether stores are reachable. | 2 |
| Availability check timeout | Milliseconds to wait for an availability check. | 15,000 |

| Setting (remote-node provider) | Meaning | Default |
|---|---|---|
| Connection timeout | Milliseconds before an outgoing connection attempt times out. | 5,000 |
| Socket timeout | Milliseconds without data on an established connection before it times out. | 15,000 |
| Maximum connections | Outgoing connections from the provider. | 200 |
| Connection retries | Retries of a failed connection. | 2 |
| Zone | Sharding zone of the provider. | unspecified |
| Re-activation check period | Milliseconds between re-activation attempts after fatal errors. | 15,000 |

A remote-node provider reports read and write failures to its parent. The object-store cluster
template combines this layout with per-node asynchronous upload to the bucket (STOR-016); the
direct-upload template (STOR-019) is recommended instead.

### STOR-015 · Rebalancing and storage optimization
Priority: **P2**

Restores the configured redundancy of a sharded store (STOR-011, STOR-014, STOR-026) and removes
surplus copies.

- After a failed shard returns, binaries written while it was down are copied to it. Because this
  can be a large volume, balancing runs in sessions, each started automatically after a garbage
  collection run (STOR-036) and limited by the maximum balancing session time.
- After deliberate changes such as a store migration, balancing is started with the optimize
  operation.
- On every deploy to a shard, the system checks whether the checksum is stored more often than the
  redundancy; if so, it raises the optimization flag and the surplus copies are removed by the next
  full garbage collection.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Optimize storage | none | 200 with an informational plain-text message; 401 without valid credentials; 403 for non-administrators. Older behavior: only raises a flag so that balancing runs after the next full garbage collection. Current behavior: starts balancing immediately; skipped if balancing is already running. | administrator |

To speed up a flagged optimization, an administrator can run garbage collection manually
(STOR-037).

### STOR-016 · Asynchronous upload to the persistent store
Priority: **P2**

An upload mode for slow or temporarily unavailable remote stores (object storage hosted far from
the system, network problems, store outages):

1. The client uploads the binary.
2. The system receives it completely, answers 201, creates the database record and keeps the
   binary in a local queue directory.
3. Background workers upload it to the persistent store.
4. After a successful upload the local copy is removed.

The queue directory lives under the base data directory, is not configurable and needs full read
and write permission. It has three areas: a staging area that validates files before they are
queued, a pending-upload area and a pending-delete area (deletions also reach the remote store
through the queue).

| Setting | Meaning | Default |
|---|---|---|
| Lock timeout | Upper bound in milliseconds for how long a queued file may be held locked during a local write or delete. | unspecified (example: 180,000) |
| Scan interval | Milliseconds between scans of the queue for files to upload. | 5,000 |
| Upload threads | Parallel uploads to the persistent store. | 5 |
| Pass-through | When on, new binaries go directly to the persistent store and the queue is bypassed, like direct upload; used to migrate from asynchronous to direct upload. | off |

Cluster variant (per-node queue, no shared mount needed; used under cluster sharding, STOR-014):

| Setting | Meaning | Default |
|---|---|---|
| Stale-add period | Milliseconds after which a queued upload whose local file is missing counts as stale. | 300,000 |
| Workers | Threads handling all actions against the persistent store. | 5 |
| Scan interval | Milliseconds between scans of the queue. | 1,000 |
| Re-activation check period | Milliseconds between re-activation attempts after fatal errors. | 15,000 |
| Zone | Sharding zone of the provider. | unspecified |

Edge cases:

- A 201 in this mode does not mean the binary has reached the persistent store; it is still in
  the local queue.
- The single-node variant in a cluster needs the queue on a shared mount; the cluster variant does
  not.
- Before switching from asynchronous to direct upload, the queue must be empty, otherwise data is
  lost; pass-through mode helps drain it.
- The migration into object storage (STOR-027) reuses this queue.

### STOR-017 · Retry of store operations
Priority: **P1**

A chain link that, when a read or write of the provider it wraps fails, retries the operation
before reporting failure. The built-in asynchronous object-store chains place it between the queue
and the object store.

| Setting | Meaning | Default |
|---|---|---|
| Retry interval | Milliseconds to wait between attempts. | 5,000 |
| Maximum attempts | Attempts to read or write before failing. | 5 |

### STOR-018 · Binary fetch workers for federated repositories
Priority: **Later**

In federated repositories the source site pushes metadata, and each target site pulls the
corresponding binaries from the source's store. A store provider manages that pull: background
importers fetch binaries queued after metadata synchronization, and on-demand importers fetch a
binary immediately when a client requests an item whose metadata has arrived but whose binary has
not, taking priority over queued work.

| Setting | Meaning | Default |
|---|---|---|
| Background importers | Workers pulling binaries for queued synchronization tasks. | 6 |
| On-demand importers | Workers pulling binaries requested by clients before they were synchronized. | 6 |
| Maximum retries | Import attempts before a binary counts as a fetch failure. | 10 |
| First retry delay | Milliseconds before the first retry. | 30,000 |
| Maximum idle time | Maximum milliseconds between task assignments. | 60,000 |
| Maximum execution time | Maximum milliseconds one node may hold a task. | 3,600,000 |

A change requires a restart. Interactions: federated repositories in
[replication-and-federation.md](replication-and-federation.md).

## S3-compatible object storage

### STOR-019 · Direct (synchronous) upload to object storage
Priority: **MVP**

The recommended way to use object storage: the binary is streamed to the bucket while the client
uploads it, and the upload succeeds only when the object store has it.

1. The client uploads the artifact.
2. The system receives it and writes it to the bucket at the same time.
3. Only once the bucket holds the complete object does the system answer 201 to the client and
   record the item in the database (whether the record is committed before the answer leaves is
   unspecified).

Properties:

- No persistent local queue is needed; works for single nodes and clusters without a shared mount.
- Faster than asynchronous upload when the system runs close to the store (less local disk I/O).
- A local temporary directory is still used during the transfer (STOR-020).
- The built-in direct template puts a node-local cache (STOR-009, default 5 GB) in front of the
  bucket.
- Large files are uploaded in parts (STOR-023).

Edge cases: when the object store is unavailable, uploads fail (asynchronous upload, STOR-016,
accepts them instead). Co-location is mandatory: the bucket sits in the region
where the system runs, because of latency and because traffic between regions is billed.

### STOR-020 · Object-store connection and bucket settings
Priority: **MVP**

Settings of the S3 provider that locate the bucket and shape the requests. Object storage is the
recommended store for production and clusters (all nodes share the bucket).

<details>
<summary>Settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Bucket name | Globally unique bucket name. Mandatory. | none |
| Path prefix | Prefix inside the bucket under which binaries are stored. | a fixed default prefix (name unspecified here) |
| Endpoint | URL or host of the object store; for AWS the official endpoints, for S3-compatible stores their own. | unspecified (example: the AWS global endpoint) |
| Region | Region of the bucket. Needed when the endpoint is the default; with a custom endpoint it may be omitted only when the client library can determine it from its own defaults (environment, instance metadata). Always setting it, with an endpoint that matches it, is recommended. | unspecified |
| Port | Explicit port; takes precedence over the scheme. Without it: 80 when plain HTTP is used, else 443. | 443 |
| Use plain HTTP | Connect without TLS. | off |
| Path-style access | Use path-style instead of virtual-hosted-style bucket addressing; requires the region-specific endpoint. Turn on for non-AWS stores and Ceph. | off |
| Folder name length | Number of leading checksum characters used as the folder name (0–5; 0 = no folders, objects lie directly under the prefix). With 4, a checksum starting `e1a97f30` *(example)* goes to folder `e1a9`. | 2 |
| Test connection at startup | Uploads and downloads a test object on startup to prove the connection works. | on |
| Disable chunked encoding | Turns off chunked encoding for object and part uploads; only for endpoints that do not implement it; may cost performance. | off |
| Legacy bucket existence check | Check bucket existence with a request on the bucket itself (deprecated by AWS). | off |
| Temporary directory | Local folder for internal temporary files during direct upload; absolute if it starts with `/`, otherwise relative to the store directory under the base data directory. | unspecified |
| Signed-URL redirect | Enables download redirects to signed URLs (STOR-032). | unspecified |
| Signed-URL validity | Seconds a redirect URL handed to a client stays valid (STOR-032). | 30 |

</details>

Request signing uses AWS Signature Version 4; a setting to choose another signature version is
unspecified.

Edge cases: when the path prefix is `/`, the newer AWS client library writes a literal double
slash into generated signed URLs (the older one percent-encoded it); clients that normalize `//`
in URLs then cannot follow download redirects (STOR-032).

### STOR-021 · Object-store authentication
Priority: **MVP**

Two ways to authenticate to S3:

| Mode | Behavior | Settings |
|---|---|---|
| Ambient credentials | The client library's default credential chain is used, e.g. an IAM role attached to the EC2 instance or to the Kubernetes workload on EKS. No secret in the configuration. | Use instance credentials: on (default off). |
| Static key pair | Credentials are written into the provider configuration. | Access key ID (also called identity); secret access key (credential). |

Minimum bucket permissions when not using a full-access managed policy: `s3:ListBucket`,
`s3:ListBucketVersions`, `s3:ListBucketMultipartUploads`, `s3:GetBucketLocation`, `s3:GetObject`,
`s3:GetObjectVersion`, `s3:PutObject`, `s3:DeleteObject`, `s3:ListMultipartUploadParts`,
`s3:AbortMultipartUpload`, `s3:ListAllMyBuckets`, `s3:CreateBucket`.

Security note: a static key pair puts the secret into the store configuration, where it can sit in
plain form; ambient credentials avoid storing a secret at all.

Interactions: outbound proxy credentials are separate (STOR-025).

### STOR-022 · Object-store encryption
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Server-side encryption with KMS | On: the object store encrypts objects with the default KMS key; instead of on, a KMS key ID or alias can be given. | not set (no KMS encryption requested) |
| Client-side encryption key | Encrypts on the client with the given KMS key ID or alias (an older name of this setting is still accepted). | not set |
| Client-side crypto mode | Encryption only; authenticated encryption; strict authenticated encryption. Older client library only. | encryption only |

Edge cases: client-side encryption is not supported with the newer AWS client library; an
installation using it must stay on the older library, which AWS ended support for at the end of
2025.

### STOR-023 · Multipart upload and transfer thresholds
Priority: **MVP**

Objects above a size threshold are uploaded in parts by several threads; this is also how objects
larger than 5 GB are stored.

| Setting | Meaning | Default |
|---|---|---|
| Multipart threshold | Size in bytes above which uploads are split and parallelized (older library: only without internal presigning). | 100,000,000 |
| Part size | Size of each part when the multipart threshold applies. With the newer library, 0 or less selects the library's own default (8 MB). | 5 MB (older library), 8 MB (newer library) |
| Transfer threads | Threads for multipart uploads (only without internal presigning). | 10 |
| Internal presigning | The system uses presigned URLs for its own put, get and delete calls. Limits objects to less than 5 GB; ignored with a warning by the newer library; not available with multi-bucket sharding. | off |
| Internal signed-URL validity | Seconds that internally used signed URLs for uploads and downloads stay valid. | 300 |
| Single-part copy | Forces copies inside the bucket to be done in a single part (newer library). | unspecified |

Interactions: client-driven multipart upload with presigned part URLs is ART-007 in
[artifacts.md](artifacts.md).

### STOR-024 · Object-store connection pool, timeouts and transport health
Priority: **MVP**

| Setting | Meaning | Default |
|---|---|---|
| Maximum connections | HTTP connections of the object-store client. | 50 |
| Connection timeout | Milliseconds to establish a connection; 0 or less means the library default. | not set, i.e. the library default; the built-in direct-upload template sets 10,000 |
| Socket timeout | Milliseconds of inactivity on an established connection. | 50,000 |
| Connection acquisition timeout | Milliseconds to wait for a pooled connection; 0 or less means the library default (newer library). | -1 |

<details>
<summary>Client-library tuning specific to the newer AWS library</summary>

| Setting | Meaning | Default |
|---|---|---|
| Library generation | Selects the newer or the older AWS client library. | newer library in current versions |
| Blocking read threads | Thread pool that adapts blocking I/O to the library's non-blocking client. | 50 |
| Target throughput | Throughput target in Gbps for the high-performance client; throttles on slow networks. | 10.0 |
| Connection health monitoring | Drops connections whose throughput stays below a minimum. | off |
| Minimum throughput | Bits per second below which a monitored connection counts as unhealthy. | 5,000,000 |
| Minimum-throughput window | Seconds below the minimum before the connection is dropped; values below 2 are treated as 2. | 2 |
| Upload client strategy | Selects the client for non-synchronous uploads; the asynchronous value disables the high-performance client. | not set (the high-performance client is then presumably used) |
| Disable checksum validation | Turns off the library's automatic checksum validation (STOR-029). | off |

</details>

A change of library requires a restart.

### STOR-025 · Outbound proxy for object-store traffic
Priority: **P1**

| Setting | Meaning | Default |
|---|---|---|
| Proxy host | Proxy used to reach the object store. | unspecified |
| Proxy port | | unspecified |
| Proxy user | | unspecified |
| Proxy password | | unspecified |
| Non-proxy hosts | Hosts reached without the proxy; with the newer AWS library a plain substring (not a regular expression) matched against the host. | unspecified |

### STOR-026 · Multi-bucket sharding on object storage
Priority: **P2**

Sharding (STOR-011) over several buckets, possibly in several regions, behind a node-local cache.
Each bucket is a health-aware object-store shard with the full object-store settings (STOR-020 to
STOR-024) except internal presigning, plus:

| Setting | Meaning | Default |
|---|---|---|
| Re-activation check period | Milliseconds between re-activation attempts after fatal errors. | 15,000 |
| Write enabled | When off, the bucket serves reads and garbage-collection deletions only. | on |
| Zone | Sharding zone of the bucket. | unspecified |

Example configurations: two buckets with redundancy 1; two buckets with redundancy 2; five
buckets in two regions with redundancy 3, one of them read-only. Changed sharding rules apply only
to new data; existing objects must be copied to new shards by hand.

### STOR-027 · Migration of an existing store into object storage
Priority: **P2**

Two procedures move an existing file-system store into a bucket:

| Procedure | Steps | Effect |
|---|---|---|
| Manual (recommended) | Stop the system; copy the store directory tree to the bucket under the configured prefix; switch the store configuration to object storage; start. | Old store remains as it was. |
| Automatic | Configure an asynchronous-upload object-store template and point its pending-upload area at the existing store directory (and its staging area at the store's staging directory) via links; start. | On startup every binary is uploaded, verified and then deleted from the old store. The old store is consumed; a full backup beforehand is strongly advised. The links can be removed afterwards. |

The same copy-the-tree approach applies to Google Cloud Storage and Azure Blob Storage; for Azure,
switching the store deletes the files of the current store.

### STOR-028 · Storage-class compatibility
Priority: **P2**

The system needs no change to run on a bucket whose objects are moved between storage classes by
the object store's lifecycle rules (AWS Intelligent-Tiering), provided only tiers with immediate
access are used.

- Allowed: frequent, infrequent and archive-instant access tiers.
- Not allowed: asynchronous archive tiers that require a restore before reading; with them the
  system does not work properly.
- Tiering only manages objects above 128 KB and costs a per-object monitoring fee, so only large
  objects should be transitioned (for example above 10 MB).
- Processes that read every object regularly (replication, for example) keep objects in the
  frequent tier and defeat tiering.
- Designed availability drops by one nine compared with the standard class; durability is
  unchanged.
- Rolling back to the standard class cannot be done by lifecycle rule; objects must be copied onto
  themselves with the standard class.

### STOR-029 · Compatibility with S3-compatible object stores
Priority: **MVP**

Any store that speaks the S3 API can serve as the binary store (Ceph, OpenStack Swift through its
S3 API, MinIO and others), not only AWS.

| Concern | What the system offers |
|---|---|
| Endpoint and addressing | Custom endpoint; path-style addressing (STOR-020). |
| Region | Must be set explicitly for the newer AWS library. |
| Strict payload checksums | The newer AWS library validates checksums strictly; stores that do not implement AWS-compliant checksums fail uploads with a content SHA-256 mismatch. Turning off checksum validation (STOR-024) works around it at the cost of integrity checking. |
| Chunked uploads | Can be disabled for stores that do not support them (STOR-020). |
| Private CA certificates | The newer library's high-performance client does not use the runtime's trust store; either add the CA certificates to the system's pod or switch the upload client strategy to asynchronous (STOR-024). |
| Plain HTTP and ports | STOR-020. |

### STOR-030 · Google Cloud Storage
Priority: **P2**

Google Cloud Storage as the binary store through Google's official client library, as a single
store or in a cluster layout analogous to the S3 templates. Large files above 5 GB are uploaded in
parts; direct upload (STOR-019) and download redirects (STOR-032) are supported. Credentials can
no longer be written inline into the store configuration. A full backup is advised before
migrating an existing store (STOR-027). Settings: unspecified.

### STOR-031 · Azure Blob Storage
Priority: **P2**

Azure Blob Storage as the binary store, as a single store or in a cluster layout. Files above 5 GB
are uploaded in parts; the blob size limit is 4.75 TB. Direct upload (STOR-019) is supported;
download redirects (STOR-032) only on self-managed installations. Switching an existing
installation to Azure deletes the files of the current store, so a full backup comes first. A
secret-less workload-identity mode for Kubernetes exists. Settings: unspecified on this page.

## Direct download from object storage

### STOR-032 · Download redirect to signed object-storage URLs
Priority: **P2**

Instead of streaming a binary through the system, the system answers a download with HTTP 302 and
a `Location` header carrying a signed, short-lived URL of the object in the bucket; the client
downloads directly from the object store. This saves node bandwidth, threads and local cache space,
especially for large binaries such as container image layers.

Enablement has two levels; both must be on:

| Level | Setting | Default |
|---|---|---|
| Store | Signed-URL redirect on the object-store provider (S3, Google Cloud Storage, Azure). | unspecified (must be switched on) |
| Store | Signed-URL validity in seconds. | 30 |
| Repository | Download redirect switch, in the repository's advanced settings or in the repository configuration through the API (REPO-019 in [repositories.md](repositories.md)). There is no global switch. | off |
| System | Minimum size for redirects (STOR-033). | 1 MB |

Resolution rules:

- Local and remote repositories with the switch on redirect every eligible request.
- A virtual repository redirects only if the member repository that finally serves the artifact has
  the switch on; otherwise the system streams the artifact as usual.
- A remote repository can redirect only if it stores artifacts locally; switching redirect on for a
  remote repository that does not store artifacts locally is an error.
- Mixing clients is possible: repositories without the switch serve clients that cannot follow
  redirects.

Formats for which redirects are supported: Docker, Helm and Helm OCI, Maven and Gradle,
npm, PyPI, Go, RPM, Debian (from client version 9 on), Composer, Conan, CRAN,
Bower, Vagrant, Git LFS, Hugging Face and generic repositories. With curl, wget and Gradle the
client version must be one that follows such redirects (minimum versions unspecified).

Signed URL content: the object's address in the bucket (prefix, checksum folder, checksum) plus a
response-content-disposition parameter with the artifact's file name, so that the client saves the
file under its name rather than its checksum. The Google Cloud Storage and Azure examples also
carry a trace identifier; the Azure example additionally carries request context (repository,
project, user name, artifact path, repository class, package format) and the requesting subject.

Network requirements: clients must be allowed to reach the object store's host, e.g.
`https://<bucket>.s3.amazonaws.com/…`, `https://storage.googleapis.com/<bucket>/…`,
`https://<account>.blob.core.windows.net/<container>/…`, or the CDN domain (STOR-034).

Interactions: credential-free download links created by users are a different feature (ART-018 in
[artifacts.md](artifacts.md)); the hosted-CDN switch REPO-020 is out of scope.

### STOR-033 · Minimum size for redirected downloads
Priority: **P2**

| Setting | Meaning | Default |
|---|---|---|
| Minimum redirect size | Smallest artifact size for which a download may be redirected (STOR-032); smaller artifacts are streamed through the system. | 1 MB (self-managed) |

UI location: general settings (see the general settings screen in
[ui-inventory.md](ui-inventory.md)). Applies to self-managed installations.

### STOR-034 · CDN-signed download URLs
Priority: **P2**

For S3, download redirects can point at an AWS CloudFront distribution in front of the same
bucket, so that clients download from the CDN.

Setup: create the distribution for the bucket; create a CloudFront key pair; configure the
distribution to forward query strings (so files download under their real name rather than their
checksum); enable signed-URL redirect on the S3 provider.

| Setting | Meaning | Default |
|---|---|---|
| CDN domain name | Domain of the distribution. | unspecified |
| CDN key pair ID | Identifier of the signing key pair. | unspecified |
| CDN private key | PEM-encoded RSA private key of the key pair. | unspecified |
| Use signature | On/off flag; meaning unspecified. | unspecified (example: on) |

When a valid CDN is configured, redirects go to the CDN by default (STOR-035).

### STOR-035 · Client choice of the redirect target
Priority: **P2**

A client can steer a redirectable download with a request header whose value selects the target:

| Value (descriptive) | Effect |
|---|---|
| No redirect | Download through the system, no signed URL. |
| Object store | Redirect to the object store even if a CDN is configured. |
| CDN | Redirect to the CDN if one is available (the default behavior when a valid CDN is configured). |

Applies to self-managed installations. Header name and exact value spellings: unspecified;
a compatible implementation defines its own.

## Garbage collection and pruning

### STOR-036 · Garbage collection of unreferenced binaries
Priority: **MVP**

Deleting a path removes only its database reference (STOR-002). Garbage collection later finds
binaries that no path references any more and deletes them from the store and the database. The
scan costs a lot of CPU and holds file locks for its duration, so it is scheduled for quiet periods
(STOR-037).

Referenced means: at least one repository path in the database still points to the binary's
checksum. Items in the trash can are such paths, so their binaries stay referenced until the
trash-can collection undeploys them after the trash retention expires. The binary records of
deleted items stay in the database until collection removes them.

Two strategies run in the same schedule:

| Strategy | When | What it deletes |
|---|---|---|
| Trash-can collection | Every run, when the trash can is enabled (ART-036 in [artifacts.md](artifacts.md)). | Undeploys trash-can items older than the trash retention period; deletes each linked binary if no other item references its checksum. Runs on several worker threads (count configurable; example: 3; default unspecified). |
| Full collection | Every N-th run. | Every binary that has no reference, including binaries left unreferenced for other reasons and items deleted from the trash can by hand. |

| Setting | Meaning | Default |
|---|---|---|
| Runs between full collections | N above. | 20 |
| Worker threads of trash-can collection | Threads that undeploy expired trash-can items and delete their binaries. | unspecified (example: 3) |
| Skip ordering in full collection | Skips sorting the objects during full collection to make it faster. | off (switching it on is an opt-in) |

With the default schedule (every 4 hours, six runs a day) a full collection happens every 20 runs,
i.e. about every 80 hours.

Interactions:

- After each run, pending shard-balancing sessions continue (STOR-015); surplus copies flagged by
  the optimization check are removed by the next full collection.
- Read-only shards still get their unreferenced binaries deleted (STOR-012, STOR-026).
- Retention and cleanup runs do not start while garbage collection is running (RET-018 in
  [retention-and-cleanup.md](retention-and-cleanup.md)); policies that skip the trash can leave
  their binaries to the full collection (RET-038).
- The SHA-256 backfill (STOR-005) should run after a collection.
- Redundant metadata records on trash-can folders slow the trash-can collection down and degrade
  the database over time; a separate maintenance operation purges them (ART-039 in
  [artifacts.md](artifacts.md)).
- Objects that exist in the store but have no database record at all are not handled here; see
  STOR-038.

### STOR-037 · Garbage-collection schedule and manual run
Priority: **MVP**

UI location: administration, maintenance screen, garbage-collection section.

| Setting / control | Meaning | Default |
|---|---|---|
| Schedule | Cron expression (with seconds field) for automatic runs, e.g. `0 30 1/6 * * ?` *(example)* for every six hours at half past. | every 4 hours |
| Next run time | Read-only display of the next scheduled run. | — |
| Run now | Starts a run immediately. | — |

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Run garbage collection | none | 200 on success; 401 without authentication; 403 for non-administrators. A run removes expired trash-can items and, when it is the full cycle, all unreferenced binaries. | administrator |

Edge cases: collection is resource-intensive, so running it too often hurts system performance.
In a hosted (SaaS) deployment the operator runs and monitors this maintenance on the tenants'
behalf; on self-managed installations the schedule is the administrator's.

### STOR-038 · Pruning unreferenced data in the store
Priority: **P1**

Removes objects and empty folders from the binary store that no database record knows at all, for
example after uploads that failed because of wrong file-system permissions or a full disk. It is
the reverse check of garbage collection: it walks the store, not the database. Supported for the
file-system store and for S3, Azure and Google Cloud Storage. It does not remove empty folders of
the repository tree (those disappear on their own after their last item is deleted).

The store is processed directory by directory (one directory per checksum prefix, e.g. 256 with a
two-character prefix), and a run can be stopped and resumed.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Start or resume pruning | Optional JSON: dry run (default off; on only counts unreferenced binaries per directory without deleting); directory to resume from (default: first directory); position inside that directory to resume from (default 0; taken from the last status report); minimum age in days (default 1; younger unreferenced binaries are skipped; 0 ignores the age). | 202 with a message that the task was submitted; 412 when a run is already active or cannot start. | administrator |
| Get pruning status | none | 200 with: state (running, stopped, finished, error); whether it is a dry run; timing (start, duration, last update, each as epoch milliseconds and as a local date-time string without time-zone offset); progress as processed directories of total (e.g. `12 / 256` *(example)*); totals of binaries processed, binaries removed and bytes reclaimed; the directory being processed with its own state and counters; error details when failed. 412 when no pruning task exists. | administrator |
| Stop pruning | none | 202 with a message that the stop request was submitted; 412 when no run is active. | administrator |

UI location: administration, maintenance screen, storage section, "prune unreferenced data" action.

Edge cases: the minimum-age guard keeps the job from deleting binaries of uploads that are still
in progress; the date-time strings in the status are not RFC 3339 because they lack an offset.

## Storage monitoring, quotas and housekeeping

### STOR-039 · Storage summary
Priority: **P1**

An overview of storage use for administrators. UI location: administration, monitoring, storage,
with a refresh button.

| Section | Figures |
|---|---|
| Binaries | Physical binary size and count; logical artifacts size (as if every artifact were its own binary) and count; optimization (physical size as a percentage of logical size, i.e. the deduplication effect); item count (files and folders). |
| Binary store | Store type (file system, database, object store); store directory (for a database store: the directory caching extracted binaries); total, used and free space with percentages; in the UI also the configured quota warning and limit thresholds (STOR-040). |
| Repositories | Per repository: key, class (local, remote cache, virtual), package format, percentage, artifacts size (like the export size, counting non-unique references), file count, folder count, item count. |

Calculation: the summary is computed by a background job and cached, by default every hour
starting at a randomly chosen minute when no schedule is configured; the schedule is configurable
as a cron expression on self-managed installations.

| Operation | Inputs | Output | Privilege |
|---|---|---|---|
| Get storage summary | none | 200 with the three sections above, without the quota thresholds. All sizes are human-readable strings with units; used and free space carry the percentage in the same string; the binaries section gives its counts and the optimization figure as formatted strings, while the per-repository folder, file and item counts are integers. 401 without valid credentials; 403 without the required privilege. | access token only: administrator, or a token scoped to read storage information |
| Refresh storage summary | none | 202 with a plain-text message that the recalculation is scheduled; 401; 403. | administrator |

Edge cases: values can be up to one schedule interval old until a refresh completes. Open: a
repository's percentage is either its share of the total available space or its share of the total
artifacts size; the spec decides.

### STOR-040 · Storage quota and disk-space thresholds
Priority: **P1**

Keeps the store's disk from filling up. UI location: administration, maintenance screen, storage
quota section.

| Setting | Meaning | Default |
|---|---|---|
| Enable quota control | Monitor disk usage and act on the two thresholds. | unspecified |
| Storage space limit | Percentage of disk space that may be used; above it every deploy of a binary is rejected. | unspecified |
| Storage space warning | Percentage of disk space at which a warning is issued. | unspecified |

Behavior:

| Threshold reached | Effect |
|---|---|
| Warning | A warning in the service log and a banner on the maintenance screen saying datastore disk usage is too high. |
| Limit | Deploys fail with `413 Request Entity Too Large`; the same error banner shows on the maintenance screen. |

Measured partition:

| Store | Partition checked |
|---|---|
| File system | The partition holding the store directory. |
| Database blobs | The partition holding the local cache directory. |
| Object storage (S3 templates) | The partition holding the node-local cache (STOR-009). |

Interactions: the summary shows the thresholds (STOR-039); project-level storage quotas and their
UI indicators are UI-052 in [ui-inventory.md](ui-inventory.md); retention tools that bound growth
(maximum unique snapshots REPO-025 in [repositories.md](repositories.md), unused-cache expiry on
remote repositories RET-036, cleanup policies) are in
[retention-and-cleanup.md](retention-and-cleanup.md).

Edge cases: with object storage the quota does not measure the bucket at all, only the local
cache partition.

### STOR-041 · Compact the embedded database
Priority: **Out of scope**

A maintenance action that returns unused allocated space of an embedded database's tables and
indexes to the operating system; best run when activity is low, since it may not complete when
storage is busy. It has meaning only for an embedded database; trove has none (PostgreSQL only).

## Notes for implementers

- Delete never touches the blob. Before a binary is removed, the collector must make sure no path
  references it (the scan holds file locks, STOR-036) and leave recently written binaries
  alone (the pruning job skips binaries younger than one day by default); otherwise a concurrent
  upload that re-references the checksum loses its content.
- The default addressing of STOR-001 is SHA-1 with a two-character directory prefix; trove
  addresses by SHA-256. SHA-1 and MD5 must still be computed and searchable, because clients
  deploy by SHA-1, verify downloads with SHA-1 or MD5 and search by them (ART-004, ART-008,
  SRCH-008).
- In direct-upload mode clients and replication treat 201 as "durable in the object store"; in
  asynchronous mode 201 only means "accepted locally". Do not mix the semantics silently.
- A download redirect is a 302 to a URL valid for about 30 seconds; clients follow it at once.
  The signed URL must carry the file name as content disposition, or clients save the object
  under its checksum.
- Redirect eligibility is decided per serving repository: a virtual repository redirects only when
  the member that serves the artifact does, and remote repositories must store artifacts locally.
- A path prefix of `/` produces `//` in signed URLs with newer AWS client libraries; clients and
  proxies that normalize `//` break on the redirect.
- With object storage the disk-space quota measures the local cache partition, not the bucket; a
  full cache disk blocks deploys while the bucket has room.
- S3-compatible stores often need path-style addressing, an explicit region, disabled chunked
  encoding or relaxed checksum validation; a startup round trip (write and read a test object)
  catches most misconfiguration before clients do.
- For bucket sharding, changed rules (redundancy, strategies) affect only data written afterwards
  and existing objects must be copied by hand; treat redistribution as an explicit, observable
  operation rather than a side effect of a configuration change.
- The storage summary is a cached computation; a refresh is asynchronous (202), so a UI that reads
  right after refreshing still shows old numbers.
