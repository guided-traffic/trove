# High availability and deployment

This page catalogs how a full-featured repository manager is run in production: the clustered
topology and what the nodes share, how nodes coordinate and lock, how traffic enters through a load
balancer, how the system is packaged and configured as a Helm chart, what the metadata database
(PostgreSQL) must provide, how a deployment is sized, and how it is upgraded and removed. Storage
back ends themselves are covered in [storage-and-blobs.md](storage-and-blobs.md); logs, metrics,
backup and general settings in [operations-and-monitoring.md](operations-and-monitoring.md).
Most features here are deployment-time configuration rather than API operations; where a feature
has no API or UI, the entry says so.

## Cluster topology and coordination

### HA-001 · Active-active cluster of identical nodes
Priority: **MVP**

**Behavior.** A clustered deployment consists of at least two nodes that sit on one local network.
The nodes are interchangeable: each one accepts client traffic, runs every internal service of the
product and works against the same database and binary storage as its peers. A load balancer
spreads requests across all nodes (see HA-022). If a node drops out, through a failure or for
planned maintenance, its share of the load moves to the remaining nodes without any manual
failover step. Data, configuration, cached objects and changes to scheduled jobs stay consistent
across the nodes (see HA-006).

Two nodes is the technical minimum; three is the recommended minimum. With three nodes, one can be
lost or upgraded while two keep sharing the load, and upgrades without downtime require three
nodes (see HA-069).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Node count (replica count) | Number of application nodes in the cluster | 1 (single node); 2 is the HA minimum, 3 is recommended |

**Operations.** None specific to this feature; node and cluster health are exposed through the
health endpoints (HA-023).

**UI.** None.

**Limits and edge cases.**
- Clustering requires an external database; the embedded database of a single-node evaluation
  setup cannot be used for a cluster (see HA-003).
- Clustering requires shared binary storage reachable by every node (see HA-004).
- License gating of clustering is out of scope (HA-072).

**Interactions.** HA-002 (which node runs cluster-wide tasks), HA-007 (locking), HA-009 (scaling),
HA-032 (replica count in the Helm chart).

### HA-002 · Cluster-wide task execution without a primary node
Priority: **MVP**

**Behavior.** Cluster-wide background work (scheduled jobs and other tasks that must run once per
cluster rather than once per node) can run on any node. There is no designated primary node. Each
node carries a task-eligibility setting:

| Value | Behavior |
|---|---|
| any | Eligible for every cluster-wide task. New installations start with this value, so a node that joins the cluster can take such tasks right away. |
| none | The node never runs cluster-wide tasks; it only serves requests. |

Older releases used a fixed primary node. On upgrade from such a release the legacy "primary" flag
is preserved for compatibility, and the operator must switch each node to "any" explicitly to get
the primary-less behavior.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Task eligibility (per node) | Whether the node may execute cluster-wide tasks: any or none | any (new installations) |
| Legacy primary flag (per node) | Marks a node as the single primary; kept only for upgraded clusters | unspecified (absent on new installations) |

**Operations.** None; node-level configuration only.

**UI.** None.

**Limits and edge cases.**
- What happens when every node is set to none is unspecified.
- Background-job synchronization uses the database lock table even when advisory locks are enabled
  (see HA-007).

**Interactions.** HA-007 (locks that keep a task on one node), HA-009 (new nodes are eligible by
default), HA-015 (separately deployed components).

### HA-003 · Shared metadata database as the cluster backbone
Priority: **MVP**

**Behavior.** All nodes connect to one external database. It holds all item metadata, the
cluster-wide configuration and the coordination state (locks; see HA-007). Binaries are stored
separately in the binary store (HA-004); this split is the default and recommended mode. A second
mode stores binaries as large objects inside the database; it is supported but not recommended,
because the PostgreSQL driver cannot stream an object of unknown length into the database, so every
upload is first written to a local temporary file.

Requirements on the database in a cluster:
- It must handle the combined load of all nodes and accept the maximum number of connections all
  nodes can open together (see HA-062).
- If the database is replicated, every node must see one consistent view at every moment,
  whichever database instance it reaches. Eventually consistent replication and write-behind
  synchronization are not supported.
- The network path to the database must be reliable, stable and low-latency; storing binaries in
  the database additionally needs high bandwidth.

**Settings.** See HA-061 (connection settings) and HA-062 (pool sizing).

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- A cluster cannot run on the embedded single-node database.
- Changing to another database does not move existing data; migration uses a full system export
  and import (see [operations-and-monitoring.md](operations-and-monitoring.md)).

**Interactions.** HA-058 (supported engines and versions), HA-064 (database high availability),
[storage-and-blobs.md](storage-and-blobs.md) (binary storage modes).

### HA-004 · Shared binary storage for all nodes
Priority: **MVP**

**Behavior.** In a cluster, all nodes must use one shared binary store that every node can reach
over the network: object storage (S3, Google Cloud Storage, Azure Blob Storage), NFS, or another
supported shared back end. Plain node-local file storage is meant for a single node; using it
with more than one replica can leave the data inconsistent. An
alternative is a cluster-replicated local storage mode in which each binary is stored on several
nodes; the number of copies (the redundancy factor) is fixed at initial deployment.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Storage type | Which binary-store back end the deployment uses | plain local file system (chart values); see the first limit below |
| Redundancy factor | Number of nodes that hold a copy of each binary; open whether it applies only to the cluster-replicated storage types or to every type except NFS (the spec decides) | 3 (chart default) |
| Lenient limit | Lowest number of copies that must succeed before a write in cluster-replicated storage is accepted | 1 (chart default) |

**Operations.** None in this page; see [storage-and-blobs.md](storage-and-blobs.md).

**UI.** None.

**Limits and edge cases.**
- Open: the default storage type is either the plain file-system type, with the replicated variant
  offered as a separate type, or a "file-system replication" default that copies data to all
  nodes; the spec decides. An implementation should not ship a default that silently becomes
  unsafe when the replica count is raised.
- The redundancy factor cannot be changed through the Helm chart after the first deployment. It
  must be set above half of the cluster size, and the cluster must never be scaled below it.
- Network file systems that the chart cannot mount itself (for example a managed elastic file
  service) are configured as plain file-system storage on a mounted volume.

**Interactions.** HA-005 (node-local volume still required), HA-053 (storage selection in the
chart), [storage-and-blobs.md](storage-and-blobs.md).

### HA-005 · Node-local persistent volume for transient data
Priority: **MVP**

**Behavior.** Each node keeps its own persistent volume even when binaries live in external object
storage. The volume holds data in transit: uploads staged before they reach the object store, the
local read cache, and data buffered while the object store is unreachable. Without a persistent
volume, an unplanned pod termination can lose data that has not yet reached shared storage.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Volume size | Size of each node's persistent volume | 20 GiB (chart default; to be increased for production) |
| Access mode | Kubernetes access mode of the per-node volume | ReadWriteOnce |
| Storage class | Storage class used for dynamic provisioning; empty uses the cluster default | cluster default |
| Local cache size | Upper bound of the node-local binary cache | 5,000,000,000 bytes (chart default) |
| Existing claim | Use a pre-created volume claim instead of a provisioned one | unset (single-node only; not supported for clustered mode in the single chart) |
| Existing per-node claims (legacy cluster chart) | Pre-created claims, one per pod, named after the release, the pod role and the pod ordinal | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Because the nodes form a Kubernetes StatefulSet, scaling down keeps the removed node's volume
  claim. The operator deletes the claim with the highest ordinal by hand (see HA-009).
- For slow primary storage, the cache directory can be placed on a separate fast volume (for
  example SSD) mounted as a custom volume (see HA-048).

**Interactions.** HA-004, HA-053, HA-071 (claims survive uninstall).

### HA-006 · Cluster-wide synchronization of configuration and state
Priority: **MVP**

**Behavior.** Configuration changes, cached objects and scheduled-job changes made on one node
become effective on all nodes without manual action. Cluster-wide configuration is stored in the
shared database. Certificates issued by the built-in certificate authority are distributed to all
application nodes through the database (see HA-018). Nodes also address each other directly: each
node has an advertised peer address that other nodes use to reach it over HTTP or TCP (see HA-020).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Node peer address | Address other nodes use to contact this node | unspecified |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Not everything is synchronized: certificates added to a node's local trust store are not
  propagated and must be added on every node (see HA-019).
- Node-local configuration files edited by hand on one node diverge from the others; in the Helm
  deployment all configuration is supplied through chart values (see HA-036).
- The transport used for cache invalidation between nodes is unspecified. One candidate is an
  embedded in-memory data grid that clusters run by default.

**Interactions.** HA-003, HA-007, HA-036.

### HA-007 · Distributed locking through the database
Priority: **MVP**

**Behavior.** Nodes serialize conflicting work (concurrent uploads of the same path, downloads that
populate a cache, background jobs) through locks held in the shared database. Two mechanisms exist:

1. **Lock table (default).** Locks are rows in a dedicated database table. This mechanism keeps its
   own connection pool, sized like the main pool by default, so a node can open up to twice its
   configured pool size (see HA-062). Under heavy load the table bloats, which increases storage
   use, can overload the database CPU and slows lock and unlock operations.
2. **PostgreSQL advisory locks (optional).** Locks are PostgreSQL advisory locks, enforced by the
   application rather than by the database, and held over a small dedicated connection pool that
   must stay connected. This reduces the table bloat and its side effects. Uploads, downloads and other
   request-path operations then synchronize through advisory locks. Background-job
   synchronization stays on the lock table. Whether the locks are session- or transaction-scoped
   is unspecified.

A recovery task watches the advisory-lock connections. When one drops, it re-acquires every lock
the node held on a fresh connection and logs that it did so. A single occurrence after a transient
disconnect is expected; frequent or continuous occurrences point to connectivity or pooling
problems.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Force advisory locks | Switches request-path locking from the lock table to advisory locks (PostgreSQL only) | off |
| Advisory-lock pool size | Number of dedicated connections per node that hold advisory locks | 5 (3 in older releases); raise it for repositories with millions of items |
| Lock-table pool size | Connections per node used by the lock-table mechanism | equal to the main pool's maximum |

**Operations.**

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Enable advisory locks | Partial system-configuration update that sets the force flag | Success or validation error; the node logs that the criteria for advisory locks are met | System administrator |

**UI.** None.

**Limits and edge cases.**
- Prerequisites: every node runs a release that supports advisory locks, and each node keeps a
  continuous connection to PostgreSQL.
- Advisory locks cannot be enabled when the nodes reach PostgreSQL through a pooler running in
  transaction mode (PgBouncer is the named example). In PostgreSQL, session-scoped advisory locks
  do not survive a transaction-pooled connection.
- Switching needs no downtime. It should happen at a quiet time, followed by a rolling restart.
- Current advisory locks can be inspected through PostgreSQL's lock catalog view (`pg_locks`).

**Interactions.** HA-002, HA-062 (connection budget), HA-064 (database failover drops sessions, so
locks are re-acquired), [remote-repositories.md](remote-repositories.md) (concurrent cache misses).

### HA-008 · Network locality of cluster components
Priority: **MVP**

**Behavior.** All components of one deployment (application nodes, database, binary store,
companion services) must be on the same fast local network. Running a cluster across a wide-area
network is possible in principle but not practical because of latency, and is not recommended.
Components far apart from each other cause intermittent health-check failures.

All components of one deployment must share one internal network: either all run in the same
Kubernetes cluster, or all run on virtual machines on one LAN. Kubernetes-hosted and
non-Kubernetes components must not be mixed in one deployment, because dynamic pod addressing
breaks communication between them. Outside Kubernetes, the components need static IP addresses,
and firewalls must allow the internal ports (see HA-021).

**Settings.** None.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Multi-site setups use separate deployments connected by replication or
federation (see [replication-and-federation.md](replication-and-federation.md)), not one stretched
cluster.

**Interactions.** HA-021, HA-022.

### HA-009 · Horizontal scaling: adding and removing nodes
Priority: **MVP**

**Behavior.** Capacity grows by adding nodes: raise the replica count, and the new node joins the
cluster and becomes eligible for cluster-wide tasks (HA-002). The cluster can grow as large as
needed. Scaling down removes the highest-ordinal nodes.

**Settings.** Replica count (HA-032); autoscaling bounds (HA-043).

**Operations.** None beyond the Helm upgrade that changes the replica count.

**UI.** None.

**Limits and edge cases.**
- Every node adds its connection pools to the database's connection budget; check the budget
  before scaling up (HA-062).
- Scaling down does not delete the removed node's persistent volume claim; delete it explicitly.
- With the bundled evaluation database and an auto-generated password, the password must be passed
  explicitly on every scale operation, or the chart regenerates it and the cluster is misconfigured
  (see HA-034).
- Components that run as separate workloads (HA-015) do not scale with the application replica
  count; their replica counts are set separately.
- In cluster-replicated storage, the cluster must never shrink below the redundancy factor (HA-004).

**Interactions.** HA-032, HA-043, HA-062.

### HA-010 · Deployment topology: single node or cluster
Priority: **MVP**

**Behavior.** Two supported topologies with different guarantees:

| Aspect | Single node | Cluster |
|---|---|---|
| Use case | Evaluation, development, small teams | Production, mission-critical |
| Nodes | 1 | 2 or more (3 or more recommended) |
| Database | Bundled or external | External PostgreSQL (for example a managed service) |
| Binary storage | Local persistent volume | Shared object storage |
| Failover | None, manual recovery | Automatic |
| Maintenance needs downtime | yes | no (rolling updates) |
| Horizontal scaling | No | Yes |

A cluster deployment needs: an external database, shared object storage, a load balancer with
health checks (an Ingress or an equivalent route), two or more replicas, and the shared cluster
keys stored as Kubernetes Secrets (HA-016, HA-031).

Rules of thumb for choosing: strict uptime targets, several teams with sustained parallel CI
usage, long-lived production use, or a need for automatic failover point to a cluster; non-critical
workloads, small teams with low concurrency, short-lived environments and acceptable manual
recovery point to a single node. Moving from one node to a cluster later is possible: switch to an
external database and shared storage, then raise the replica count.

**Settings.** None beyond those of HA-032, HA-033, HA-053.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** None beyond the table.

**Interactions.** HA-001, HA-034, HA-033.

### HA-011 · Active-passive warm standby
Priority: **P2**

**Behavior.** As a disaster-recovery alternative to an active-active cluster, a passive standby
server can take over from a single active server. Two methods:

1. **Fault-tolerant shared storage (recommended).** The installation and its data live on
   fault-tolerant network storage (NAS or SAN). If the active server fails, the standby machine
   mounts the storage, starts the system from it and takes over. Virtualization-level failover of
   a VM image that mounts the storage achieves a very short recovery time.
2. **Cross-server synchronization.** The database is replicated to the standby, and the data and
   configuration directories are copied (for example with `rsync`) from the active server to the
   standby, excluding work, temporary, cache and log directories. The copy runs on the standby and
   pulls from the active server.

The database must always be at an earlier state than the copied file system. Database replication
(or a dump) therefore runs before the file copy, and the standby's point in time is the database
replication time. Files copied without a database record are removed with the maintenance action
that prunes unreferenced data.

**Settings.** None in the system; the method is operational.

**Operations.** Prune unreferenced binaries (a maintenance operation; see
[operations-and-monitoring.md](operations-and-monitoring.md)).

**UI.** Administration, maintenance section: prune unreferenced data.

**Limits and edge cases.** The two copy steps are not atomic, so the file system can contain
binaries that the database does not reference.

**Interactions.** HA-003, [operations-and-monitoring.md](operations-and-monitoring.md) (backup and
restore).

## Node architecture and internal communication

### HA-012 · Single client entry point with path-based routing
Priority: **MVP**

**Behavior.** Clients reach every function of a node through one entry port. A routing component
on that port forwards each request to the internal service that owns its path prefix (repository
and package traffic, UI, identity service, and so on). A second external port gives direct access
to the core application for high-volume artifact traffic, bypassing the routing component;
deployments can disable this bypass so that all traffic goes through the router. Both ports serve
the same certificate when TLS is enabled (HA-018).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Router path | Path prefix routed to the routing component (all services, including the UI) | `/` |
| Direct application path | Path prefix routed straight to the core application | the application's own context prefix |
| Disable router bypass | Remove the direct route so that everything flows through the router | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Clients never call internal service ports directly. Internal
service-to-service traffic also goes through the router.

**Interactions.** HA-013, HA-021, HA-039, HA-040.

### HA-013 · Internal service registry and health-gated routing
Priority: **Later**

**Behavior.** The routing component on each node also acts as service registry. Every internal
service registers through the router with the identity service when it starts; startup succeeds
only when all services have joined. The router health-checks the services on its node. A service
that misses a health check is marked unavailable, and requests routed to it fail with HTTP 502
until it recovers.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Skip registry TLS verification | Lets the router skip TLS verification when it first contacts the registry, so it can fetch the internal certificate | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Errors from internal services that mention the router's local
addresses indicate a router failure.

**Interactions.** HA-012, HA-018, HA-023.

### HA-014 · Availability-zone affinity for internal routing
Priority: **P2**

**Behavior.** When a node is assigned an availability-zone identifier, its router prefers service
instances in the same zone. If no instance is available in the local zone, it falls back to
round-robin across instances in other zones. Setting a zone identifier enables the feature.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Availability-zone identifier (per node) | Zone the node runs in; enables zone affinity when set | unset (feature off) |
| Zone-configuration logging interval | How often the router logs its zone configuration even without changes | unspecified (example: 600 seconds) |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** HA-012, HA-044 (topology spread across zones).

### HA-015 · Components deployed as separately scaled workloads
Priority: **P1**

**Behavior.** The Helm chart runs some components as separate Kubernetes Deployments next to the
core StatefulSet: the UI server, an internal event bus and an event-collection pipeline. Each such
pod has its own routing sidecar and telemetry sidecar. This decouples their lifecycles (restarting
one no longer restarts the whole pod), allows independent autoscaling and resource tuning, and
limits the blast radius of a failure. The remaining services run as one container each inside the
core pod. The older layout with every service in one shared container is gone: the chart now
rejects that setting.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| UI enabled | Deploy the UI component | on |
| UI as separate pod | Run the UI as its own Deployment; off puts it back into the core pod as a container | on |
| Component replica count (per component) | Replicas of each separately deployed component | 1 |
| Per-component custom volumes, volume mounts, environment variables | Extra volumes and variables for the router, UI and telemetry sidecars of these pods | empty |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Scaling the core application does not scale these components; set their replica counts
  explicitly.
- Every one of these pods has to prove its identity to the deployment when it starts, which is why
  the chart insists that both cluster keys resolve at install time (HA-031).
- A volume mount added to the router sidecar needs a matching volume defined for every pod that
  contains the sidecar. Otherwise Kubernetes rejects the Deployment with a "volume not found"
  error. Open for global-level volumes: either they reach only the core StatefulSet, or declaring
  both the global volume list and the global mount list gives them to every pod; the spec decides.
- The split raises the baseline CPU and memory use of a deployment (see HA-067 for UI sizing).
- The separate UI pod holds its start in an init step until the core application is reachable;
  a UI pod that never starts points at that wait or at missing key variables.
- Network policies written for the single-pod layout may block the new pods.

**Interactions.** HA-031, HA-043, HA-048, HA-054.

### HA-016 · Cluster encryption key
Priority: **MVP**

**Behavior.** A symmetric AES key (128 or 256 bit) encrypts secrets and other shared data that the
nodes store in the database. Every node of a component must hold the identical key; a node with a
different key cannot decrypt the shared data. Unless an operator supplies a key, one is generated
at first start. For automated installations the operator generates the key (32 random bytes,
hex-encoded, for example `openssl rand -hex 32`) and provides it to every node, in Kubernetes as a
Secret (HA-031).

The key is not used for authentication between services and plays no part in validating tokens.
After a security incident such as leaked tokens, the response is to revoke the tokens, rotate the
service trust key (HA-017) or rotate the token-signing key pair (see TOK-036 in
[access-tokens.md](access-tokens.md)), not to change this key.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Cluster encryption key | Hex-encoded AES key shared by all nodes | generated at first start if not supplied; in the Helm chart there is no default and it must be supplied |
| Key secret reference | Name of an existing Kubernetes Secret that holds the key | unset |

**Operations.** None. For an upgrade, the key is read from a running node's file system; there is
no way to view it in the UI (unlike the service trust key, HA-017).

**UI.** None.

**Limits and edge cases.**
- No rotation procedure exists. Replacing the key without re-encrypting the stored data breaks
  decryption and loses data.
- Starting nodes against a database that was initialized with a different key fails with a
  key-mismatch error. Recovery means restoring the original key, or dropping and recreating the
  database after scaling all workloads down.
- Changing the key after installation is very difficult; demonstration keys must
  never be used in production.

**Interactions.** HA-031, HA-037, [storage-and-blobs.md](storage-and-blobs.md) (encrypted storage
credentials), [remote-repositories.md](remote-repositories.md) (stored upstream credentials).

### HA-017 · Service trust key provisioning and rotation
Priority: **Out of scope**

**Behavior.** A shared secret establishes trust between the internal services of one deployment:
each service signs the tokens it sends to other services with HMAC-SHA256 using this secret, and a
service with a different secret cannot communicate. The secret is generated at first start unless
supplied. For automation, the operator generates it (`openssl rand -hex 32`) and supplies it to
every service. Unlike the cluster encryption key it can be rotated: generate a new value, apply it
to every component that shares the old one within one maintenance window, and check the service
logs for authentication errors. Administrators can display the key in the security settings after
re-entering their password. The chart refuses a key that equals a previously published default
value. A single-binary product has no internal service mesh (see IAM-043 in
[identity-and-access.md](identity-and-access.md)).

### HA-018 · Internal TLS with a built-in certificate authority
Priority: **P2**

**Behavior.** TLS between nodes and services is off by default. When an administrator enables it,
all communication with and inside the deployment must use TLS: the routing components and the
identity service accept HTTPS only, plain HTTP is blocked, and every cookie gets the secure flag.
Enabling internal TLS is the only way to set the secure flag on cookies. The
identity service acts as certificate authority and signs the certificates the routers use. Its
root certificate reaches all application nodes through the database. Companion products and the
load balancer must have the root certificate copied into their trust stores.

Three certificate options:
1. The built-in authority generates and signs node certificates.
2. The operator supplies a signed server certificate and key, placed under fixed bootstrap file
   names, and copies the issuing CA into the trust store of every node and of the load balancer.
3. The operator supplies a custom CA certificate and key, which the identity service then uses to
   sign node certificates.

The built-in CA can be forced to regenerate (a marker file in the bootstrap location followed by a
restart). After regeneration, the new root certificate must be redistributed to every trust store.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Internal TLS | Require TLS for all external and internal communication | off |
| Additional subject alternative names | Names added to the internally issued certificate, for example the in-cluster Service name | empty |
| HTTPS connector for direct application port | Serve the direct application port over TLS; the port number changes when enabled | off |

<details>
<summary>Requirements for a custom server certificate</summary>

| Requirement | Value |
|---|---|
| Private key algorithm | RSA |
| Minimum key size | 1024 bit |
| Certificate and key | Must match |
| Issuer | Must equal the CA certificate's subject |
| Subject | Must equal the node's configured IP address |
| Subject alternative names | Must include the subject |
| Key usage extension | Marked critical; digitalSignature and keyEncipherment enabled |
| Extended key usage | TLS web server authentication; TLS web client authentication (required by older releases only) |
| Ports | Both public ports must present the same certificate |

</details>

**Operations.** None beyond the configuration patch that enables TLS.

**UI.** None.

**Limits and edge cases.**
- In Kubernetes, the internally issued certificate must carry the in-cluster Service name as a
  subject alternative name, because companion products connect through the Service. The node IP is
  added automatically at startup. Because pod IPs change, supplying a custom certificate per pod is
  impractical; the recommended alternative is to terminate TLS at the edge (HA-042).
- After TLS is enabled, Kubernetes probes must switch to HTTPS, and companion services must use an
  `https://` base URL for the core service.

**Interactions.** HA-013, HA-042, HA-046, AUTHN-034 in
[authentication-providers.md](authentication-providers.md) (mutual TLS for clients).

### HA-019 · Trust store for outbound TLS connections
Priority: **MVP**

**Behavior.** Every outbound TLS connection (to remote repository upstreams, LDAP over TLS, OAuth
or OIDC providers, outbound proxies and other external services) is validated against trusted CA
certificates; the system never connects to a server whose certificate chain it cannot validate. To
accept a server whose certificate is self-signed or issued by a private CA, the operator adds the
PEM certificate to the trust
directory of every service that needs it and restarts the service. Services written in Go read the
operating system's certificate directory. In Kubernetes, a Secret holding the certificate chain can
be referenced from the chart; an init container copies the certificates into the trust directory of
every enabled component on each start.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Custom certificates enabled | Copy certificates from a Secret into the trust store at startup | off |
| Certificate Secret | Kubernetes Secret holding the certificate chain | unset |
| Peer trust root certificates | Secret with root certificates of peer instances, used for cross-instance token trust | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Trusted certificates are not propagated between cluster nodes; each node needs them.
- A restart is required for a newly added certificate to take effect.
- The server chain of a target can be retrieved with `openssl s_client -connect host:port
  -showcerts`.

**Interactions.** RMT-008 in [remote-repositories.md](remote-repositories.md) (upstream
certificate trust), TOK-039 in [access-tokens.md](access-tokens.md) (cross-instance trust),
[oidc-ci-integration.md](oidc-ci-integration.md) (self-hosted CI issuers with private CAs).

### HA-020 · IPv6 and dual-stack networking
Priority: **P1**

**Behavior.** The system runs on hosts with IPv6 addresses. Host names that resolve to IPv6 or
IPv4 both work. A node's advertised peer address (HA-006) may be an IPv6 literal, written in square
brackets with or without a port. It is the only setting that needs a literal IP instead of a host
name, unless a separate bind interface is configured. In Kubernetes, the Services accept a
dual-stack policy and IP-family list, and the bundled reverse proxy can listen on IPv6 only.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Node peer address | IPv6 literal in brackets allowed | unspecified |
| Service IP family policy | Kubernetes single-stack, prefer-dual-stack or require-dual-stack | empty (cluster default, single-stack IPv4) |
| Service IP families | Kubernetes IP families of the Service | empty (cluster default) |
| Proxy IPv6-only listener | Bind the bundled proxy listeners to IPv6 | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Not supported with literal IPv6 addresses: the public base URL, remote repository upstream URLs,
  and authentication provider URLs (LDAP, SAML and similar). Host names must be used there.
- Cross-instance identity federation does not work over IPv6.
- With the cluster's embedded data grid active (HA-006), the node's runtime must be told not to
  prefer the IPv4 stack, otherwise the grid cannot bind to an IPv6 interface. A separate
  bind-interface setting lets the peer address stay a host name.

**Interactions.** HA-025 (base URL), HA-038 (Service settings).

### HA-021 · Network ports and listener layout
Priority: **MVP**

**Behavior.** A node exposes two external ports: the routing component's entry port, which serves
the UI and all APIs, and the core application's direct port (HA-012). Each internal service
additionally listens on its own internal HTTP port, and many also on a gRPC port, for traffic
inside the node. A separate maintenance port serves the core application's liveness and
readiness probes (HA-046). The bundled proxy listens on unprivileged ports inside its container
and the Service maps them to 80 and 443; the move away from 80 and 443 inside the container is
due to OpenShift, where non-root processes may not bind ports below 1024.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Entry port | Port of the routing component | unspecified (product-specific) |
| Direct application port | Port of the core application | unspecified (product-specific) |
| Maintenance port | Port for health probes, separate from client traffic | unspecified (product-specific) |
| Service HTTP / HTTPS ports | Ports on the Kubernetes Service of the bundled proxy | 80 / 443 |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Clients never reach internal service ports directly. Firewalls between
the nodes of a non-Kubernetes deployment must allow the internal ports (HA-008).

**Interactions.** HA-012, HA-038, HA-046.

## Load balancer and edge proxy

### HA-022 · Load balancer in front of the cluster
Priority: **MVP**

**Behavior.** A load balancer is the single entry point of a clustered deployment. It distributes
requests across the application nodes, and each node's router forwards them to the right internal
or companion service. One load balancer is needed per deployment, in front of the core application
only; companion services are reached through the core nodes' routers. A single-node installation
may use a plain reverse proxy instead; clusters use a load balancer. The load balancer must be set
up before traffic is directed to the deployment. Configuring the load balancer, database and object
storage is the operator's responsibility.

In Kubernetes, the load balancer is either a cloud load balancer provisioned for a Service of type
LoadBalancer (HA-038), an Ingress controller (HA-039) or a Gateway API controller (HA-040).

**Settings.** See HA-024 (proxy requirements), HA-025 (headers and base URL), HA-038 – HA-041.

**Operations.** Health endpoints for load balancer checks: HA-023.

**UI.** None.

**Limits and edge cases.**
- With internal TLS enabled (HA-018), the routers reject plain connections. The load balancer must
  then connect over TLS and trust the deployment's CA.
- A caching layer between clients and the system is not supported (see HA-024).

**Interactions.** HA-023, HA-024, HA-025.

### HA-023 · Health endpoints for load balancers and probes
Priority: **MVP**

**Behavior.** The system exposes unauthenticated-capable health endpoints (they require a valid
user, which may be the anonymous user):

| Endpoint (generic) | Scope | Response when healthy | Use |
|---|---|---|---|
| Ping | Core application | `200`, plain text `OK`; otherwise an HTTP error with a reason | Legacy simple check |
| Platform readiness | All services of the node, through the router | `200`, JSON object with a code field `OK` | Recommended load balancer health check |
| Platform liveness | Routing component | `200`, JSON object with a code field `OK` | Container liveness |
| Platform health | All services, per service | JSON with a status `HEALTHY` per service | Diagnostics when readiness fails |
| Application readiness / liveness | Core application only, on the maintenance port | `200` | Kubernetes probes of the core container |

The readiness check of the core application alone can report OK while the router or the UI is
down. Load balancers must therefore use the platform readiness endpoint, which covers every service
of the node.

**Settings.** None.

**Operations.**

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Ping | none | text `OK` or error status | any user, including anonymous |
| Readiness | none | JSON code `OK` or error status | any user, including anonymous |
| Liveness | none | JSON code `OK` or error status | any user, including anonymous |
| Health detail | none | JSON per-service status | unspecified |

**UI.** None.

**Limits and edge cases.**
- Right after deployment, services can report unhealthy for 2 to 5 minutes while they initialize.
- See [operations-and-monitoring.md](operations-and-monitoring.md) for the monitoring view of the
  same data.

**Interactions.** HA-022, HA-046, HA-047.

### HA-024 · Proxy requirements for large and long transfers
Priority: **MVP**

**Behavior.** Every proxy hop in front of the system (load balancer, Ingress controller, Gateway,
reverse proxy) must be configured for artifact traffic:

| Requirement | Reason | Example value |
|---|---|---|
| No request body size limit | Nobody can predict how large an upload will get, so every finite cap sooner or later turns a valid upload into an HTTP 413 | unlimited (`0`) |
| Long read and send timeouts | Large transfers outlast common 60-second proxy defaults | 600 s on Ingress and Gateway examples; 900 s read timeout on the bundled proxy; 2400 s in the reference reverse-proxy configuration and for subdomain-routed container traffic |
| Request buffering off, HTTP/1.1 upstream | With request buffering on, the proxy holds the whole upload and then forwards it at once; clients can time out during the stall | off |
| Request and response buffering off on the bundled proxy | Streams in both directions instead of buffering | buffering on by default; every sizing profile turns it off |
| Larger proxy header buffers | Container manifest responses carry large headers | 40 × 128 KiB buffers, 128 KiB busy buffers (subdomain example) |
| Chunked transfer encoding | Clients stream uploads without a length | on |
| Upstream keep-alive | Reuse connections to the nodes | 320 idle connections, 10,000 requests per connection (bundled proxy) |
| Next-node behavior | Pass a request to another node on connection error or timeout, including non-idempotent requests, with a limit of 1 try | 1 try (reference configuration) |
| Connection timeout to node | Fail fast when a node is unreachable | 5 s (reference configuration) |
| Encoded slashes preserved | Package names contain encoded slashes that must reach the application unchanged | the reference Apache configuration allows encoded slashes and disables URI canonicalization, and the reference NGINX configuration forwards the original URI; the bundled proxy normalizes unless its raw-URI option is on (off by default) |
| No response cache | A caching layer between clients and the system causes stale metadata and upload failures; use pull-through repositories instead | no cache |

**Settings.** As listed; they are settings of the proxy, not of the system.

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Ingress controllers silently ignore annotations meant for a different controller. Two unrelated
  NGINX-based Ingress controllers exist under almost the same name, each with its own annotation
  prefix. A configuration written for the wrong one looks right but does nothing, and the
  symptoms (HTTP 413 on uploads, 404 on registry login) only show up later.
- Snippet-based configuration is disabled by default in common controllers. One controller needs
  an additional risk-level setting before it accepts snippet annotations.

**Interactions.** HA-027 (registry rewrites), HA-039, HA-040, RMT-040 in
[remote-repositories.md](remote-repositories.md) (chained remote repositories as the supported
cache).

### HA-025 · Forwarded headers and the public base URL
Priority: **MVP**

**Behavior.** Behind a proxy, the system builds absolute URLs (UI links, redirects, `Location`
headers of upload sessions, URLs inside package metadata) from the public base URL. Three inputs
feed it:
- a configured custom base URL (general settings), recommended to match the public server name;
- a product-specific request header carrying the externally visible base URL, which every proxy
  is recommended to set (scheme, host and, for non-standard ports, port);
- the standard forwarding headers `Host`, `X-Forwarded-For`, `X-Forwarded-Proto` and
  `X-Forwarded-Port`.

A complete precedence order is unspecified. Two points are fixed: with correct
`Host` and `X-Forwarded-*` headers the system already derives the right URL, and the explicit
base-URL header wins when an upstream hop dropped those headers or when the configured base URL
differs from the host name clients use. The reference proxy configurations fill in the scheme
header from the request's own scheme when no earlier hop set it. A proxy behind another load
balancer must pass the upstream `X-Forwarded-*` headers through instead of overwriting them (for
the community Ingress controller: its use-forwarded-headers option). Client IP addresses come from
the forwarding header, but only proxies that match a trusted-proxy pattern are believed.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Custom base URL | Public URL of the deployment | unset |
| Trusted internal proxy pattern | Regular expression of proxy IPs whose forwarding headers are trusted | private ranges: 10.0.0.0/8, 192.168.0.0/16, 169.254.0.0/16, 127.0.0.0/8 |

**Operations.** Base URL management: see [operations-and-monitoring.md](operations-and-monitoring.md).

**UI.** General settings screen (UI-044 in [ui-inventory.md](ui-inventory.md)).

**Limits and edge cases.**
- If the base URL is wrong, links and redirects carry the wrong port or `http` instead of `https`.
- If an upstream proxy drops or rewrites `Host` and `X-Forwarded-*`, container uploads fail
  partway because the returned `Location` header points to the wrong host. Setting the base-URL
  header explicitly avoids this.
- When the system acts as identity provider for companion services, the proxy must send the
  base-URL header with the public `https` URL.
- A literal IPv6 address in the base URL is not supported (HA-020).
- For the strongest isolation, a reverse proxy runs on the same host as the application, and the
  application binds only to the proxy host.

**Interactions.** AUTHN-002 in [authentication-providers.md](authentication-providers.md), HA-027.

### HA-026 · Reverse-proxy configuration generator
Priority: **P2**

**Behavior.** An administration screen collects the reverse-proxy parameters and generates a
ready-made configuration snippet for NGINX or Apache HTTP Server, which the operator downloads and
installs in the proxy. In a cluster, the generated configuration contains an upstream section that
balances across all nodes. The same parameters drive per-repository registry routing (HA-027).

<details>
<summary>Generator fields</summary>

| Field | Meaning | Default |
|---|---|---|
| Server provider | Embedded server, NGINX or Apache | unspecified |
| Internal host name | Host the proxy uses to reach the application; `localhost` if co-located | unspecified |
| Internal application port | Direct application port | from node configuration |
| Internal router port | Router entry port | from node configuration |
| Public server name | Name clients use | unspecified |
| Use HTTP / HTTPS | Which listeners the proxy exposes | unspecified |
| HTTP port | Public HTTP port | 80 |
| HTTPS port | Public HTTPS port | 443 |
| Registry routing method | Subdomain or port per repository (HA-027) | unspecified |

</details>

**Operations.**

| Operation | Inputs | Outputs | Privilege |
|---|---|---|---|
| Get reverse-proxy configuration | none | configuration object | unspecified (administrative configuration) |
| Update reverse-proxy configuration | configuration object | success or validation error | unspecified (administrative configuration) |
| Get reverse-proxy snippet | none | proxy configuration as text | unspecified (administrative configuration) |

**UI.** Administration, general section, HTTP settings.

**Limits and edge cases.**
- Available only for self-hosted deployments.
- NGINX must be at least version 1.3.9 to proxy container registry traffic.
- Apache needs the proxy, HTTP proxy, balancer, rewrite, headers, deflate, SSL, connect, HTML
  proxy, by-request balancing and shared-memory slot modules. Its `ProxyPassReverse` must be set,
  otherwise redirects carry the internal URL.

**Interactions.** HA-024, HA-025, HA-027, [ui-inventory.md](ui-inventory.md).

### HA-027 · Container-registry routing at the edge
Priority: **P1**

**Behavior.** Container clients address registries at the host root (`/v2/...`), while the system
serves container repositories under a repository-scoped API path. The edge proxy therefore
rewrites registry requests. Three access methods exist:

1. **Repository path.** The repository name is the first path segment after `/v2/`
   (`host/<repo>/<image>:<tag>`). The proxy rewrites the token request and `/v2/<repo>/...` to the
   repository's registry API. This is the system's default method.
2. **Subdomain (recommended for a reverse proxy).** The repository is the left-most host label
   (`<repo>.host`). The proxy extracts it from `Host` and rewrites to the repository's registry API.
   It needs a wildcard certificate that covers both the apex and `*.` names (TLS is negotiated
   before routing), a wildcard DNS record, and the system's registry routing method set to
   subdomain.
3. **Port per repository.** Each repository gets its own port on the public server name. The
   recommendation is a virtual repository that aggregates the others, so only one port is needed.

Direct access without a reverse proxy is also possible.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Registry routing method | Repository path, subdomain or port | repository path |
| Registry port (per repository) | Port for the port method | unset |

**Operations.** Read and update the web-server section of the system configuration (System
administrator). A read-modify-write cycle can fail validation: the read returns a placeholder value
for the unused HTTP port, which must be replaced with a positive number before writing back.

**UI.** Administration, reverse-proxy settings; container repository advanced settings.

**Limits and edge cases.**
- Subdomain rewriting with the routing method still on repository path breaks pushes: blobs
  upload, then the manifest upload fails with an invalid-manifest error. The returned `Location`
  header already contains the repository name, and the rewrite prepends it a second time. Reads
  keep working, so the setup looks healthy.
- With Gateway API exposure, filters are bound to the router path rule alone, so registry rewrites
  skip anything sent to the direct application path (HA-040). With the repository-path method
  this is harmless: the `Location` headers returned during a blob upload already have the
  rewritten form, and pushes work without changing the routing method.
- The certificate must cover as many subdomain levels as are used.

**Interactions.** HA-024, HA-025, [formats/README.md](formats/README.md) (container format page).

### HA-028 · Client-certificate forwarding from the reverse proxy
Priority: **P2**

**Behavior.** Mutual TLS for clients is terminated at the reverse proxy. The proxy verifies the
client certificate against the trusted client CAs. On success it passes the certificate (PEM,
escaped) to the system in a configurable request header. It must always remove that header from
incoming requests, so a client cannot inject it. The system then validates the certificate, checks
it has not been revoked, and maps it to a user (see AUTHN-034 in
[authentication-providers.md](authentication-providers.md)). A certificate that fails validation
blocks the request. If no user identity can be extracted, the request falls back to its other
credentials (basic or bearer). A request without a certificate is blocked only when the
deployment is configured to require client certificates.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Client-certificate header name | Request header the proxy uses to forward the verified client certificate | a product-specific name |
| Proxy verification depth | Certificate chain depth the proxy verifies | unspecified (example: 2) |

**Operations.** None in this page.

**UI.** None.

**Limits and edge cases.** A proxy that does not strip the header lets any client authenticate as
any user. This is the critical misconfiguration of this feature.

**Interactions.** AUTHN-034, AUTHN-031 in [authentication-providers.md](authentication-providers.md).

## Helm deployment

### HA-029 · Helm chart as the Kubernetes deployment
Priority: **MVP**

**Behavior.** On Kubernetes and OpenShift, the recommended deployment is a Helm chart. A single
release driven by a single values file serves every size from a trial install to a large
production cluster.
Optional companion components are switched on and off with an enabled flag each. Configuration
changes are made in the values file and applied with `helm upgrade --install`; values not set fall
back to the chart defaults. The chart ships pre-configured probes, resource limits, security
contexts and sizing overlays (HA-052), and runs pre-upgrade validation (HA-030).

Typical install flow: add the chart repository; for production, create the external database and
users, configure external object storage, write the values file, create the key Secrets, choose a
sizing profile and run a pre-install check; then install. Afterwards check health (HA-023), log in
to the UI, change the built-in administrator password and finish the first-run wizard (UI-007 in
[ui-inventory.md](ui-inventory.md)).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Chart version | Pinned chart version for reproducible installs | unset (latest); pinning recommended |
| Values files | Layered values; the last file wins on overlapping keys | chart defaults |
| Component enabled flags | Turn optional components on or off | per component |
| OpenShift overlay | Values overlay that relaxes security contexts for OpenShift; applied last | not applied |
| Name overrides | Partial or full override of the generated resource name prefix | unset |

**Operations.** None (Helm operations).

**UI.** None.

**Limits and edge cases.**
- `helm upgrade` without `--install` fails on the first deployment with "release not found".
- `helm show values` prints every parameter with comments and is the authoritative list of current
  defaults.
- Pre-install checklist: the external database answers from within the cluster network; each
  component's database and user exist; the cluster has a default StorageClass; the key Secrets
  exist; with external object storage, the Secret holding the storage configuration exists too.
- Resource names derive from the release name and chart name. Helm drops the chart name when the
  release name already contains it, so actual names should be checked rather than assumed.

**Interactions.** HA-030 – HA-057, HA-068, HA-069.

### HA-030 · Fail-fast chart validation
Priority: **MVP**

**Behavior.** The chart refuses to render, failing `helm install` or `helm upgrade` with a
descriptive error before anything is deployed, when the configuration is unsafe or inconsistent:

| Condition | Result |
|---|---|
| Cluster encryption key or service trust key cannot be resolved | Install or upgrade fails, naming the missing key or keys |
| Service trust key equals a previously published default | Install or upgrade fails |
| Single-container mode requested | Install or upgrade fails; the mode is no longer supported |
| Gateway API enabled without a gateway class name | Install fails |
| Gateway API enabled but the Gateway API CRDs are not installed | Install fails before creating anything |
| Fresh install with HTTPS enabled on the bundled proxy, but no TLS Secret and no explicit self-signed option | Install fails |
| Upgrade with the bundled database enabled without the explicit "database upgrade ready" acknowledgement | Upgrade blocked, even for changes that do not touch the database (for example Ingress annotations) |
| Upgrade of the multi-component chart from certain earlier chart lines without a second explicit acknowledgement flag | Upgrade blocked |

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Keys already in configuration | Skip key validation because the keys are supplied through the configuration override (HA-036) | off |
| Database upgrade ready | Acknowledge that the bundled database may be upgraded | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** The database-upgrade error message mentions databases even when the
change was only to routing, so the cause is easy to miss.

**Interactions.** HA-031, HA-034, HA-040, HA-042, HA-070.

### HA-031 · Cluster key provisioning in the chart
Priority: **MVP**

**Behavior.** The chart has no default cluster encryption key or service trust key; every install
and upgrade must supply both. They are given at the chart's global level, so all components and
companion charts receive the same keys. They can be given as references to existing Kubernetes
Secrets (recommended for production) or as inline values (development and testing only). The same
keys must be passed on every later install and upgrade. Omitting them makes the chart generate new
keys, which breaks the existing installation. Older values files that set the keys per component
keep working; the chart reads them only when the global keys are absent.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Encryption key (inline) | Cluster encryption key value | none (required) |
| Trust key (inline) | Service trust key value | none (required) |
| Encryption key Secret | Existing Secret holding the encryption key | unset |
| Trust key Secret | Existing Secret holding the trust key | unset |
| Keys passed via configuration | Keys are inside the configuration override; skip validation | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** A deployment that ran with a published default key is a security
incident. The trust key must be rotated, while the encryption key must be left as it is, because it
cannot be rotated (HA-016).

**Interactions.** HA-016, HA-017, HA-030, HA-035.

### HA-032 · Replica count and clustered mode in the chart
Priority: **MVP**

**Behavior.** The same chart deploys a single node or a cluster; a replica count of two or more
makes it a cluster. The core application runs as a StatefulSet with rolling updates. A legacy
dedicated cluster chart distinguished primary and member pods. There, every pod should be
primary-capable, so scaling sets the replica count of primary-capable pods.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Replica count | Number of application pods | 1; HA needs 2 or more, 3 recommended |
| Update strategy | StatefulSet update strategy | RollingUpdate |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- With more than one replica, the plain local file-system storage type must not be used (HA-004).
- Separately deployed components keep their own replica counts (HA-015).

**Interactions.** HA-001, HA-009, HA-043, HA-052.

### HA-033 · External database configuration in the chart
Priority: **MVP**

**Behavior.** For production, the chart connects to an external PostgreSQL database with a fixed
password. The bundled database must be disabled; while it is enabled, the chart ignores all
external database parameters. The connection is set either as an explicit URL with user and
password, or as references to an existing Secret, with one key each for user, password and URL. A
multi-component chart can derive per-component connection URLs from one global host, port and TLS
mode. In that case each component's database must be named after the component, unless the URL is
overridden per component. The chart also disables its own initial database creation when an
external database is used. An init container waits until the database is reachable before the
application starts.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Bundled database enabled | Deploy the evaluation database subchart | on |
| Database type | Engine type | unset in the single-product chart (set to PostgreSQL for an external database; rendered automatically for the bundled one); preset to PostgreSQL in the multi-component chart |
| Database driver | Driver identifier | same as the type: unset in the single-product chart, preset in the multi-component chart |
| Database URL | Connection URL; when set, host and port stay empty | unset |
| Database user / password | Credentials (inline) | unset |
| Credential Secret references | Secret name and key for each of user, password, URL | unset |
| Global database host / port / TLS mode | Used to derive per-component URLs | unset / 5432 (example) / unspecified |
| Initial database creation | Let the chart create databases | must be off with an external database |
| Wait for database | Init container that blocks startup until the database answers | on |
| Allow non-PostgreSQL | Required to start with another engine (HA-059) | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- A common misconfiguration places the database block one level too deep. The application then
  keeps trying to reach the bundled database host and fails with an unknown-host error.
- The container image contains only the PostgreSQL driver.
- Databases and users must exist before install (HA-060).

**Interactions.** HA-034, HA-060, HA-061, HA-063.

### HA-034 · Bundled evaluation database
Priority: **P2**

**Behavior.** By default, the chart deploys a PostgreSQL subchart as a single pod with a persistent
volume, for evaluation only. Its password is generated if not given (a random 10-character
alphanumeric string). Because the password must stay the same across upgrades and scale operations,
it must be set explicitly or read back from the generated Secret and passed on every upgrade.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Database name / user | Names created in the bundled database | product name |
| Password | Database password | generated (10 alphanumeric characters) |
| Maximum connections | PostgreSQL `max_connections` of the bundled server | 1500 (sizing profiles: 50, 100, 200, 600, 2000, 5000 from smallest to largest) |
| Listen addresses | PostgreSQL `listen_addresses` | `*` |
| Volume size | Persistent volume of the database | 200 GiB in the single-product chart; 500 GiB in the multi-component chart |
| Image tag | Database image version | chart-specific |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Not for production.
- Before every upgrade, pin the database image tag to the running version. Otherwise a newer image
  can hit incompatible data or loop on restart.
- Upgrades are blocked until the database-upgrade acknowledgement is set (HA-030).
- Subchart major changes rename value keys (for example the password key) and require deleting the
  database StatefulSet with orphan cascading, plus its Secret, before upgrading (HA-070).

**Interactions.** HA-030, HA-033, HA-070.

### HA-035 · Secrets handling in the chart
Priority: **MVP**

**Behavior.** Every credential the chart consumes can come from an existing Kubernetes Secret
instead of a plain value: cluster keys, database credentials, storage credentials, TLS
certificates, license material and the administrator bootstrap password. The chart can also create
custom Secrets from values: a list of entries, one Secret per file, each with a name, a key (the
file name) and the content. These are consumed by declaring a volume of type secret and mounting
it, read-only, into custom init or sidecar containers. A unified-secret mode collects all
chart-generated and custom secrets into one Secret; custom volumes must then reference the unified
Secret's name.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Custom secrets | List of {name, key, content}; one Secret per entry | empty |
| Unified secret | Put all chart-managed secrets into a single Secret (externally supplied Secrets are untouched) | open: on (chart values) or off; the spec decides |
| Prefix unified secret with release name | Prepend the release name to the unified Secret's name | on |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Custom secret names should be clearly distinguishable inside the unified Secret.
- The chart integrates with external secret stores (for example Vault) and cloud workload
  identities (for example IAM roles for service accounts) instead of static keys.

**Interactions.** HA-031, HA-033, HA-048, HA-053.

### HA-036 · Configuration rendering and override precedence
Priority: **MVP**

**Behavior.** In Kubernetes, the system's configuration file is rendered by the chart from a base
template plus chart values; configuration files on individual nodes must never be edited by hand.
For settings the chart does not expose as values, three override methods exist, in order of
preference:

1. **Extra configuration map.** A nested map that is merged over the rendered base configuration.
2. **Environment variables.** Any configuration key can be set as an environment variable named
   with a fixed product prefix followed by the upper-cased key path joined with underscores (for
   example `<PREFIX>_SHARED_SECURITY_<KEY>`).
3. **Complete external configuration file** from an existing Secret. Not recommended: the operator
   then owns the whole file, including everything the chart would otherwise render. Intended for
   legacy cases only.

A custom init container that writes the configuration file takes precedence over all of these.
Entries in the extra map override both the base template and a fully specified configuration value.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Extra configuration | Map merged over the rendered configuration | empty |
| Extra environment variables | List of name/value (or Secret reference) pairs per container | empty |
| Configuration override Secret / key | Secret holding a complete configuration file | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- The system does not validate the extra map. A key with a typo or an unknown path is dropped
  without any error, so the intended change quietly never happens.
- Sizing profiles set some values through the rendered configuration; a complete override file
  replaces them.

**Interactions.** HA-031, HA-037, HA-051, HA-052.

### HA-037 · First-start bootstrap
Priority: **MVP**

**Behavior.** Several things can be preset so that a new deployment starts configured:
- **Administrator credential.** Username, password and the source addresses from which the
  bootstrap credential is accepted. The pod must be restarted after the values change.
- **Initial global and security configuration**, imported from files in a ConfigMap at first start.
- **Files copied on start.** Extra configuration files are copied into the configuration directory
  only on the first start by default. Later changes are not applied unless the file is listed for
  copying on every start (source path to target directory).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Bootstrap admin username | Built-in administrator to (re)create | `admin` |
| Bootstrap admin password | Password, inline or from a Secret | unset |
| Bootstrap admin allowed source | Address range from which the bootstrap is accepted (`*` = anywhere) | loopback address |
| Initial configuration ConfigMap | ConfigMap holding the initial global and security configuration | unset |
| Copy on every start | List of {source, target} files to copy at every pod start | empty |

**Operations.** None.

**UI.** First-run wizard (UI-007 in [ui-inventory.md](ui-inventory.md)).

**Limits and edge cases.** Without a bootstrap credential, the built-in administrator starts with a
well-known default password that the first-run wizard forces the operator to change.

**Interactions.** IAM-010 in [identity-and-access.md](identity-and-access.md) (administrator
recovery), TOK-041 in [access-tokens.md](access-tokens.md) (bootstrap token),
[operations-and-monitoring.md](operations-and-monitoring.md) (configuration as code).

### HA-038 · Kubernetes Service exposure
Priority: **MVP**

**Behavior.** The chart exposes the deployment through Kubernetes Services. The application Service
is internal (ClusterIP) by default. The bundled edge proxy (HA-041) gets an externally reachable
Service, whose settings control how the cloud load balancer is provisioned.

<details>
<summary>Service settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Application Service type | Type of the core application Service | ClusterIP |
| Proxy Service type | LoadBalancer (cloud), NodePort (bare metal behind an external balancer) or ClusterIP (behind your own Ingress) | LoadBalancer |
| Static load balancer IP | Pre-reserved IP; ignored unless the type is LoadBalancer | empty |
| Source ranges | CIDRs allowed to reach the load balancer, enforced as cloud firewall rules; empty allows all | empty |
| External traffic policy | Cluster (spread over all nodes, client IP lost) or Local (only nodes with a pod, client IP preserved) | Cluster |
| Annotations | Cloud-specific load balancer options (internal LB, LB type, cloud certificate, DNS label, address pool) | empty |
| TLS offload | Terminate TLS at the cloud load balancer and send HTTP to the proxy | off |
| Force HTTPS upstream | With offload on, still use HTTPS from proxy to application | off |
| Node port | Fixed node port for NodePort | assigned from 30000–32767 |
| IP family policy / IP families | Dual-stack options | empty |
| Additional ports | Extra Service ports | empty |
| HTTP listener enabled / external port | Plain HTTP on the Service | on / 80 |
| HTTPS listener enabled / external port | HTTPS on the Service | on / 443 |

</details>

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- If the static IP is not passed again on an upgrade, the field reverts to empty and the cloud
  provider may assign a different address.
- Source ranges and the external traffic policy are independent settings: the first restricts who
  can reach the load balancer, the second controls whether pods see client IPs.
- With TLS offload, the HTTPS listener inside the proxy must be disabled.
- An external load balancer IP can take minutes to appear.

**Interactions.** HA-022, HA-041, HA-042.

### HA-039 · Ingress exposure
Priority: **MVP**

**Behavior.** The chart can create a Kubernetes Ingress for an existing Ingress controller. The
bundled edge proxy should then be disabled: it is on by default with a LoadBalancer Service, so
leaving it on gives the cluster an extra, unused (and billed) cloud load balancer. The Ingress has two path rules: the router path and the direct
application path (HA-012). Its backend is the application Service, or optionally the bundled
proxy as last hop. TLS uses Secrets created manually in the namespace, or certificates issued
automatically by a certificate controller through an annotation. A separate Ingress for gRPC
(HTTP/2) traffic keeps HTTP/1.1 keep-alive and gRPC on separate connections.

<details>
<summary>Ingress settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Create the Ingress | off |
| Hosts | Host names | empty |
| Class name | Ingress class; must match a registered class exactly | empty |
| Path type | Kubernetes path type | ImplementationSpecific |
| Router path | Path to the router | `/` |
| Direct application path | Path to the core application | the application's context prefix |
| Disable router bypass | Remove the direct path rule | off |
| Default backend enabled | Create a default backend | on |
| Backend service | Application Service or bundled proxy | application Service |
| Proxy backend port | Port when the proxy is the backend | the proxy's HTTP port |
| Annotations / labels | Controller-specific settings (body size, timeouts, rewrites) | empty |
| TLS | List of {Secret, hosts} | empty |
| Additional rules / additional direct routes | Extra rules and paths, rendered as templates | empty |
| Custom Ingress | Free-form Ingress manifest | empty |
| gRPC Ingress enabled / Service only / path prefix | Separate Ingress (or only its Service) for gRPC traffic | off / off / a product-specific prefix |

</details>

**Operations.** None.

**UI.** None.

**Limits and edge cases.** See HA-024 (body size, timeouts, snippets), HA-027 (registry rewrites).
Rewrite snippets on the Ingress apply to every generated path, unlike Gateway API filters. Once a
TLS block is configured, the controller redirects plain HTTP to HTTPS. Without a TLS block it
emits no redirect, so turning the redirect off explicitly is only defensive there. It becomes
necessary when an upstream balancer terminates TLS and the controller trusts its forwarded
headers, or when TLS is added later while traffic should stay on HTTP; otherwise clients see
redirect loops or refused connections.

**Interactions.** HA-024, HA-027, HA-040, HA-042.

### HA-040 · Gateway API exposure
Priority: **P1**

**Behavior.** As an alternative to Ingress, the chart creates a Gateway, an HTTPRoute and
optionally a controller-specific extension filter (for example a snippets filter) wired into the
route. The Gateway controller provisions the external load balancer. The chart detects whether the
cluster serves the `v1` or `v1beta1` Gateway API and emits matching resources. Ingress and Gateway
can be enabled together during a migration, so DNS can be switched gradually and rolled back
without a redeploy.

<details>
<summary>Gateway API settings</summary>

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Create Gateway API resources | off |
| Gateway class name | Existing GatewayClass; required when enabled | empty |
| Gateway name | Override; defaults to the full name plus a gateway suffix | empty |
| Hosts | Host names of the HTTPRoute; empty matches any host | empty |
| HTTP listener port | Port of the always-present HTTP listener | 80 |
| HTTPS listener port | Port of each HTTPS listener | 443 |
| Router path / direct application path | Path rules | `/` / the application's context prefix |
| Path type | PathPrefix, Exact or RegularExpression | PathPrefix |
| Disable router bypass | Remove the direct path rule | off |
| Additional direct routes / additional rules | Extra HTTPRoute rules, rendered as templates | empty |
| TLS | List of {Secret, hosts}; one HTTPS listener per host name | empty |
| Labels / annotations | On the Gateway | empty |
| HTTPRoute enabled | Create the route (off also suppresses the extension filter) | on |
| HTTPRoute name / labels / annotations / filters | Route metadata and filters on the router rule | empty |
| Extension filter enabled / name / API version / kind / spec | Controller-specific filter created and referenced automatically | off / empty |

</details>

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Host names for HTTPS listeners resolve from the TLS entry's hosts, then the global hosts, then a
  wildcard listener without a host name. Every TLS entry needs an existing Secret. TLS mode is
  always Terminate, so traffic inside the cluster is plain HTTP.
- Filters attach only to the router path rule, not to the direct application path.
- The Gateway accepts routes from its own namespace and no other.
- The bundled proxy cannot serve as last hop behind a Gateway.
- The Ingress default path type is rejected by the Gateway API.
- Filter contents are passed through unvalidated.
- The Gateway name suffix avoids collisions with data-plane Services that some controllers create.

**Interactions.** HA-024, HA-027, HA-039.

### HA-041 · Bundled edge reverse proxy
Priority: **P2**

**Behavior.** The chart can deploy its own NGINX-based reverse proxy in front of the application,
exposed through a LoadBalancer Service (HA-038). It applies the transfer settings of HA-024 and the
header settings of HA-025, and adds an HSTS header (max-age one year, including subdomains). It can
route container registry subdomains (HA-027), accept PROXY-protocol connections, and take its whole
configuration from a custom ConfigMap.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Deploy the proxy | on |
| Replica count | Proxy pods | 1 (sizing profiles: 1, 1, 2, 2, 2, 3 from smallest to largest) |
| Minimum available | Disruption budget of the proxy | 0 |
| TLS Secret | Kubernetes TLS Secret for HTTPS | unset |
| Generate self-signed certificate | Let the chart generate a self-signed certificate (testing only) | off |
| Disable proxy buffering | Turn off request and response buffering | off (every sizing profile turns it on) |
| PROXY protocol on HTTP / HTTPS | Expect PROXY-protocol headers on a listener | off |
| Port in redirects / absolute redirects | NGINX redirect behavior | NGINX defaults |
| Preserve encoded slashes | Pass the raw request URI, including encoded slashes, upstream | off |
| IPv6-only listeners | Bind the listeners to IPv6 only, for single-stack IPv6 clusters | off |
| Custom server snippet | Extra proxy directives appended to the generated server block (rendered as a template) | empty |
| Custom configuration ConfigMap | Replace the generated proxy configuration | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- A first install that keeps HTTPS on needs one of three things: a TLS Secret, the explicit
  self-signed option, or HTTPS switched off (HA-030).
- On upgrade, a previously chart-generated certificate is found and reused unchanged. It is marked
  to be kept when the chart is removed, and must be deleted by hand once replaced. If a custom
  certificate is supplied on that first upgrade, the legacy Secret is deleted.
- A listener with PROXY protocol switched on expects the PROXY header on each incoming
  connection.

**Interactions.** HA-024, HA-038, HA-039, HA-042.

### HA-042 · TLS termination topology in Kubernetes
Priority: **MVP**

**Behavior.** Supported places to terminate client TLS:

| Option | Where TLS ends | Notes |
|---|---|---|
| Cloud load balancer offload | Cloud LB with a cloud-managed certificate | Proxy HTTPS disabled, offload enabled on the Service (HA-038) |
| Ingress or Gateway | Ingress controller or Gateway listener | Certificate as a TLS Secret; Gateway always terminates |
| Bundled proxy | Edge proxy with a custom certificate Secret | Recommended over internal TLS |
| End to end | Internal TLS inside the deployment (HA-018) | Pod-IP certificates are impractical; Service-name SAN required |

When TLS ends at the edge, traffic to the application is plain HTTP inside the cluster. The plain
HTTP listener of the proxy is kept for that case and disabled only when the application itself is
HTTPS-only. A self-signed test certificate is made of an RSA 2048-bit key and a certificate valid
for 365 days, both generated with OpenSSL and loaded into a TLS Secret.

**Settings.** See HA-018, HA-038, HA-039, HA-040, HA-041.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** The load balancer may need to trust the proxy's certificate when it
re-encrypts to the proxy.

**Interactions.** HA-018, HA-038 – HA-041.

### HA-043 · Horizontal pod autoscaling
Priority: **MVP**

**Behavior.** The chart can create a HorizontalPodAutoscaler for the application pods, scaling on
CPU utilization and optionally on memory or other metrics. Separately deployed components have
their own autoscalers (HA-015). In the two extra-large sizing profiles, each autoscaler's upper
bound is set to the profile's replica count, so switching autoscaling on cannot shrink a workload's
ceiling below the size the profile plans for.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Enabled | Create the autoscaler | off |
| Minimum replicas | Lower bound | 1 |
| Maximum replicas | Upper bound | 3 |
| Target CPU utilization | Average CPU utilization target | 70 % |
| Additional metrics | Extra metric specs, for example memory utilization at 70 % | empty |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** With the single-product chart, autoscaling requires a minimum of 2
replicas (clustered mode); the legacy cluster chart has no such restriction.

**Interactions.** HA-009, HA-015, HA-032, HA-052.

### HA-044 · Scheduling and disruption controls
Priority: **MVP**

**Behavior.** The chart exposes the standard Kubernetes scheduling controls for the application
pods: pod anti-affinity (so replicas spread across hosts), topology spread constraints, node
selectors, tolerations, explicit affinity rules, a priority class and a custom scheduler. A
disruption budget is available for the edge proxy.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Pod anti-affinity type | soft (preferred) or hard (required); any other value disables it; used only when no explicit affinity is set | soft (sizing profiles use hard) |
| Anti-affinity topology key | Spread domain | `kubernetes.io/hostname` |
| Topology spread constraints | Kubernetes topology spread rules | empty |
| Node selector / tolerations / affinity | Placement constraints (a global node selector also applies to the proxy) | empty |
| Priority class | Create a priority class, or use an existing one | off; value 1,000,000,000 when created (the maximum for user-defined classes) |
| Scheduler name | Custom scheduler | unset |
| Application disruption budget (minimum available) | Disruption budget of the application pods | unset (the values file carries a commented example of 1) |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** In every sizing profile the bundled database pod is required to run on a
different node than the application pods.

**Interactions.** HA-001, HA-014, HA-052.

### HA-045 · Container security hardening
Priority: **MVP**

**Behavior.** The application containers run hardened by default: non-root with a fixed UID, GID
and fsGroup, no privilege escalation, not privileged, all Linux capabilities dropped, and the
runtime-default seccomp profile. Optionally the root file system is mounted read-only. The
application directory is then immutable, and only the mounted data volume is writable. On
OpenShift, which enforces stricter security policies of its own, the pod and container security
contexts of the application, the bundled proxy and the bundled database are switched off through
a values overlay (cluster-admin rights are needed to configure this). The chart's service account and RBAC role are not created by
default.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Container security context enabled | Apply the container security context | on |
| Run as non-root | Kubernetes runAsNonRoot | on |
| Privileged / allow privilege escalation | Kubernetes flags | off / off |
| Capabilities dropped | Linux capabilities removed | ALL |
| Seccomp profile | Kubernetes seccomp profile type | RuntimeDefault |
| Read-only root file system | Mount the root file system read-only | off |
| Pod security context enabled | Apply UID/GID/fsGroup | on (fixed non-root IDs) |
| Create service account / name / annotations | Service account for the pods | off / generated / empty |
| Automount service account token | Mount the Kubernetes API token | off |
| Create RBAC role | Role allowing get, watch and list on Services, Endpoints and Pods | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Custom sidecars may run as a different user, including root, if their
template says so (HA-050). The application image is built on a minimal base image that carries only
a small set of utilities (network tools, a cron daemon, archive tools, an HTTP client and similar);
further debugging tools must be downloaded into the writable data volume and run from there.

**Interactions.** HA-021 (no privileged ports), HA-050, HA-053 (workload identity through service
account annotations).

### HA-046 · Health probes in the chart
Priority: **MVP**

**Behavior.** Every container has liveness, readiness and startup probes. The core application's
probes call its own liveness and readiness endpoints on the separate maintenance port. The routing
component's probes call the platform liveness and readiness endpoints on the entry port, over HTTPS
when internal TLS is on. The bundled proxy probes through itself. Each probe can be disabled or
replaced by a custom probe definition.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Probe timeout (all probes) | Seconds before a probe attempt fails | 5 |
| Application liveness | Initial delay / period / failure threshold | 0 s / 10 s / 5 |
| Application readiness | Initial delay / period / failure threshold | 0 s / 10 s / 5 |
| Application startup | Initial delay / period / failure threshold | 10 s / 5 s / 90 (up to about 7.5 minutes) |
| Router liveness / readiness | Initial delay / period / failure threshold | 10 s / 10 s / 5 |
| Router startup | Initial delay / period / failure threshold | 10 s / 5 s / 30 |
| Probe enabled / custom definition | Per probe | enabled / chart template |

**Operations.** Uses the endpoints of HA-023.

**UI.** None.

**Limits and edge cases.** The router's liveness probe asks the router to ignore unhealthy
downstream services, so one failing internal service does not restart the router.

**Interactions.** HA-018, HA-023, HA-047.

### HA-047 · Graceful termination and connection draining
Priority: **MVP**

**Behavior.** When a pod is stopped, open uploads and downloads are allowed to finish. A pre-stop
hook on the routing container polls the application's liveness endpoint every 2 seconds and keeps
the pod alive while the application still answers OK. The application's maximum graceful-shutdown
request duration is derived from the pod's termination grace period, so in-flight requests get the
whole grace period to complete.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Termination grace period | Seconds Kubernetes waits before killing the pod; also the maximum time in-flight requests get | 30 |
| Lifecycle hooks | Custom post-start and pre-stop hooks per container | the router's pre-stop drain hook; none on the application |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** A transfer that outlasts the grace period is cut off.

**Interactions.** HA-046, HA-069.

### HA-048 · Custom volumes and volume mounts
Priority: **P1**

**Behavior.** Any Kubernetes volume type (ConfigMap, Secret, PersistentVolumeClaim and others) can
be added to the pods, with mounts into specific containers. Volumes and mounts can be declared
globally (all pods) and per component simultaneously; mount names must match volume names. A
pre-start command can act on mounted content before the application starts. A namespace-wide
custom PersistentVolumeClaim can also be created and mounted.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Custom volumes (global / per component / per sidecar) | Kubernetes volume list | empty |
| Custom volume mounts (global / per component / per sidecar) | Kubernetes volume-mount list | empty |
| Custom persistent volume claim | Name, mount path, access modes, size, storage class | empty |
| Pre-start command | Shell command run in the application container before start | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** A mount without a matching volume in the same pod is rejected by
Kubernetes (HA-015).

**Interactions.** HA-005 (fast cache volume), HA-035, HA-049, HA-050.

### HA-049 · Custom init containers
Priority: **P1**

**Behavior.** Custom init containers run before the application starts, for example to check the
file system, run a pre-start test, or fetch the configuration file from an external source such as
a vault or another repository. Two lists exist: one that runs before the chart's own init
containers and one that runs after them.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Init containers (before built-in) | Container templates run first | empty |
| Init containers (after built-in) | Container templates run after the chart's init containers | empty |
| Init container image and resources | Image and requests/limits of the chart's own init containers | chart image; requests 10m CPU / 50 MiB, limits 1 CPU / 1 GiB |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** A configuration file written by an init container overrides every other
configuration source (HA-036).

**Interactions.** HA-035, HA-036, HA-048.

### HA-050 · Custom sidecar containers
Priority: **P1**

**Behavior.** Additional containers run next to the application in the same pod, for example
monitoring agents or log shippers. They can mount the application's data volume, custom volumes and
custom secrets, and have their own resources and security context.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Sidecar containers | Container templates | empty |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Templates are rendered by the chart, so they can reference chart values.

**Interactions.** HA-035, HA-045, HA-048, HA-055.

### HA-051 · Environment variables, lifecycle hooks and ConfigMaps
Priority: **P1**

**Behavior.** Extra environment variables can be injected into each container, as plain values or
Secret references. Custom post-start and pre-stop hooks can be attached. A chart-managed ConfigMap
can carry arbitrary files (for example a logging configuration, a MIME-type table or a hook
script). It is mounted as a volume, and its files can be copied into the configuration directory at
every start (HA-037).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Extra environment variables (per container) | Name/value or Secret reference | empty |
| Lifecycle hooks (per container) | Post-start / pre-stop commands | empty (router has a pre-stop drain hook) |
| ConfigMap files | Map of file name to content | empty |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** HA-036, HA-037, HA-047.

### HA-052 · Resource requests, limits and sizing profiles
Priority: **MVP**

**Behavior.** Resource requests and limits for the application, proxy, database and init containers
are unset by default. The recommendation is to start from a pre-built sizing profile and adjust
it. A profile is a values file layered before the operator's own values (the last file wins). It
sets replica counts, autoscaling bounds, requests and limits, runtime memory options, request
worker threads and database pool sizes. Runtime memory settings must match the container limits.

<details>
<summary>Sizing profiles of the core application</summary>

| Profile | Replicas | CPU request | Memory request / limit | Request worker threads | Accept backlog | DB pool per node, each of core, identity and metadata service |
|---|---|---|---|---|---|---|
| Extra small | 1 | 1 | 3 GiB / 4 GiB | 50 | 200 | 15 |
| Small | 1 | 1 | 4 GiB / 5 GiB | 300 | 600 | 50 |
| Medium | 2 | 1 | 4 GiB / 5 GiB | 300 | 600 | 50 |
| Large | 3 | 2 | 10 GiB / 12 GiB | 500 | 800 | 100 |
| Extra large | 4 | 2 | 14 GiB / 16 GiB | 600 | 1200 | 150 |
| 2× extra large | 6 | 4 | 20 GiB / 24 GiB | 800 | 1200 | 200 |

Every profile also turns proxy buffering off, uses hard pod anti-affinity, and keeps the bundled
database on a separate node. The proxy replica counts are 1, 1, 2, 2, 2 and 3, and the bundled
database's connection limits are 50, 100, 200, 600, 2000 and 5000, from smallest to largest
profile. The separately deployed components (HA-015) get the same replica count as the core
application in each profile. The two extra-large profiles set each autoscaler's maximum to the
profile's replica count.

</details>

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Resources (per container) | Kubernetes requests and limits | unset |
| Runtime memory options | Initial and maximum heap, or percentages of the container memory | unset |
| Request worker threads | Maximum concurrent request threads of the core application | 200 |
| Connection accept backlog | Queued connections beyond the worker threads | 400 |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Profiles with more than one replica need clustered mode (HA-001).
- Some profile values are written into the rendered configuration file, so a complete
  configuration override (HA-036) discards them.
- Outside the profiles, the smallest recommended runtime heap is 512 MB initial and
  2 GB maximum, to be raised with repository size and the number of concurrent users.

**Interactions.** HA-043, HA-062, HA-067.

### HA-053 · Persistence and storage back-end selection in the chart
Priority: **MVP**

**Behavior.** One chart value selects the storage back end, and the chart renders the matching
storage configuration. Back ends: local file system (default in the chart values; see HA-004 for
the open question on the default), cluster-replicated file system, NFS,
S3 (direct or through a cache, plus cluster and archive variants), Google Cloud Storage and Azure
Blob Storage. S3 access can use a workload identity (a service account annotated with a cloud IAM
role) or static access keys, given inline or from a Secret. A fully custom storage configuration
can be supplied inline or from an existing Secret; it replaces everything the chart would render.
Redirecting downloads to signed object-store URLs is a toggle.

**Settings.** See [storage-and-blobs.md](storage-and-blobs.md) for the back-end parameters. Chart
specifics:

| Setting | Meaning | Default |
|---|---|---|
| Storage type | Back end | local file system |
| Custom storage configuration (inline or Secret) | Replaces the rendered storage configuration | unset |
| Workload identity | Use the pod's cloud identity for object storage | on for S3 |
| Signed-URL download redirect | Redirect downloads to the object store | off |
| Object-store connection test | Test the connection at startup | off |
| NFS server address / data path / backup path / capacity | NFS back-end parameters | unset / unspecified / unspecified / 200 GiB |
| Existing shared claims (legacy cluster chart only) | Use pre-created ReadWriteMany claims (one data claim per ordinal plus one backup claim) and their number | off |
| Node-local cache on a separate volume | Enable the file-system cache and point its directory at a custom fast volume (HA-005, HA-048) | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Storage configuration files copied from the chart are written at every start, so changes take
  effect after a restart.
- An empty inline object-store configuration produces an invalid storage configuration error;
  use the Secret method instead.

**Interactions.** HA-004, HA-005, HA-035.

### HA-054 · Network policies
Priority: **MVP**

**Behavior.** The chart can render a list of Kubernetes NetworkPolicies that restrict ingress and
egress of the application and database pods (for example: only application pods may connect to the
database pod, and the database pod has no egress).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Network policies | List of {name, pod selector, ingress rules, egress rules} | empty (none rendered) |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** An entry that omits the pod selector, ingress or egress gets an
allow-all rule for the omitted part. Policies must be updated when components move to separate pods
(HA-015).

**Interactions.** HA-015, HA-033.

### HA-055 · Log shipping and metrics integration in the chart
Priority: **P1**

**Behavior.** The chart offers several ways to get logs and metrics out of the cluster:
- logger sidecars, each tailing one selected log file to its standard output;
- a log-shipper sidecar that forwards logs to a central log stack;
- JSON-formatted console logs of all services;
- a metrics endpoint toggle, with companion values that deploy a metrics collector, a
  ServiceMonitor and dashboards;
- a runtime management interface on its own port, reachable through a LoadBalancer Service for
  remote inspection.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Logger sidecars | List of log files to tail, with resources | empty |
| Log-shipper sidecar | Enabled, target URL, resources, custom shipper configuration | off |
| JSON console logs | Write all service logs to standard output as JSON | off |
| Metrics | Expose application metrics | off |
| Runtime management interface | Enabled, port, host name, TLS, authentication | off |

**Operations.** None in this page; see [operations-and-monitoring.md](operations-and-monitoring.md).

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** HA-050, [operations-and-monitoring.md](operations-and-monitoring.md).

### HA-056 · Private image registry and air-gapped installation
Priority: **P1**

**Behavior.** All images can be pulled from a private registry: a global registry override, image
pull Secrets, and per-image tags or digests. For air-gapped installation, the chart archive and all
images are mirrored to an internal registry first, and the values point to it.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Image registry (global) | Registry host for all images | the publisher's public registry |
| Image pull Secrets | Kubernetes docker-registry Secrets | empty |
| Image version / digest overrides | Per image | chart app version |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Image tag precedence: global version override, then per-image tag, then
the chart's application version.

**Interactions.** HA-029, HA-068.

### HA-057 · Server-side extension scripts delivered through the chart
Priority: **Later**

**Behavior.** Server-side extension scripts are packaged as Kubernetes Secrets, one per script
including its configuration file, and listed in the values. The chart mounts them into a bootstrap
location; the operator adds a copy-on-every-start entry (HA-037), or a pre-start command in the
legacy cluster chart, that copies them into the extension directory at each pod start and
overwrites earlier copies. Secret names can be template expressions for use from an umbrella
chart.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Extension script Secrets | List of Secret names | empty |

**Operations.** None in this page.

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** EVT-063 in [events-and-extensibility.md](events-and-extensibility.md).

### HA-073 · Message-broker TLS for the scanning component
Priority: **Later**

**Behavior.** The separately deployed scanning component talks to its services through a bundled
RabbitMQ message broker. Its chart can switch the broker's connections to TLS. The broker either
generates its own certificates or uses operator-supplied ones: a CA certificate, a server
certificate and an RSA server key, given as an existing Kubernetes Secret or inline in the
values (the instructions for the multi-component chart show only the Secret form). In the multi-component chart the TLS
switch and the generate-or-supply choice exist twice, at the global level and on the broker
subchart, and both copies must carry the same value so the components that share the broker
agree. An extra broker setting serves the broker's management interface over HTTPS as well.
Switching TLS off again is a values change followed by an upgrade.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Broker TLS enabled (global and subchart) | Use TLS for broker connections | unspecified |
| Generate broker certificates | Broker creates its own certificates instead of using supplied ones | unspecified |
| Broker certificate Secret / inline CA, certificate, key | Operator-supplied TLS material | unset |

**Operations.** None (Helm values).

**UI.** None.

**Limits and edge cases.** Requires a minimum release of the scanning component, or of the
multi-component chart; the exact versions are unspecified here.

**Interactions.** HA-018 (internal TLS), [security-scanning-and-curation.md](security-scanning-and-curation.md).

## Database

### HA-058 · Supported database engine and versions
Priority: **MVP**

**Behavior.** PostgreSQL is the recommended engine for every component; the companion products
accept no other engine. New installations accept only PostgreSQL unless another engine is
explicitly allowed (HA-059). PostgreSQL-specific features (advisory locks, streaming replication)
are used, and managed services (Amazon RDS, Azure Database for PostgreSQL flexible server, Google
Cloud SQL) provide high availability, backups and scaling.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Supported PostgreSQL versions | Major versions accepted | 13.x, 14.x, 15.x, 16.x, 17.x |
| Recommended PostgreSQL versions | | 15.x – 17.x |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Some companion services support only PostgreSQL 15 to 17.

**Interactions.** HA-003, HA-007, HA-064.

### HA-059 · Non-PostgreSQL database engines
Priority: **Out of scope**

**Behavior.** For the core application only, other engines are supported: Oracle (19 and 23c),
MySQL 8.x (InnoDB), Microsoft SQL Server 2019 and later (no Kerberos single sign-on), MariaDB
(10.5, 10.6, 10.11.x and 11.4.x) and an embedded database for evaluation. A new installation starts
on such an engine only with an explicit "allow non-PostgreSQL" setting. The drivers are not in the
image and must be added before start. Existing installations migrate to PostgreSQL through a full
export and import. Excluded by the owner constraint that fixes PostgreSQL as the database.

### HA-060 · Database provisioning
Priority: **MVP**

**Behavior.** Before installation, the operator creates a dedicated database user and a database
owned by that user with UTF-8 encoding, and grants the user all privileges on the database. The
application needs full privileges on its database. With a restricted superuser role, as on managed
services, the new user is given the right to create databases, and the database is created while
connected as that user. To reach a self-managed PostgreSQL server from the application nodes, the
server must listen on a reachable address and have a host-based authentication rule for the
database, user and client network, with password authentication. The server then needs a restart
or reload.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Database name / user | Names of the database and owner | unspecified (example: the application's name) |
| Encoding | Database encoding | UTF8 |
| Client network | CIDR allowed by the host-based authentication rule | unspecified |

**Operations.** None (SQL performed by the operator).

**UI.** None.

**Limits and edge cases.**
- The default tablespace setting must be empty or name a tablespace created for the purpose.
  Setting it to a built-in tablespace (`pg_default`, `pg_global`) makes creating partitioned tables
  fail, and the system uses partitioned tables.
- Example scripts use placeholder passwords that must be replaced with strong ones.

**Interactions.** HA-033, HA-061, HA-063.

### HA-061 · Database connection settings
Priority: **MVP**

**Behavior.** Each component that has its own database connection (core application, identity
service, metadata service) is configured with engine type, driver, connection URL, user name and
password, plus its own pool limits. Some components use a different database client library than
the core application. Their connection string is derived automatically from the core
application's URL, but the conversion can fail. Such components then need an explicit connection
string in libpq keyword format (host, port, dbname, user, sslmode and certificate paths).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Type / driver | Engine and driver | PostgreSQL |
| URL | Connection URL, may list several hosts (HA-064) | unset |
| User name / password | Credentials | unset |
| Maximum open connections (per component, per node) | Pool upper bound | 100 (the chart renders 80 when not set by a profile) |
| Maximum idle connections | Idle connections kept in the pool (servlet-container deployments only) | 10 |
| Component connection string override | Explicit connection string for a component using a different client library | derived |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** The application cannot start until the database is reachable. In the
chart, an init container waits for it (HA-033).

**Interactions.** HA-062, HA-063, HA-064.

### HA-062 · Connection pool sizing and the cluster-wide connection budget
Priority: **MVP**

**Behavior.** Every node opens up to its configured pool sizes, so the database must accept the
sum over all nodes. With the lock-table mechanism, the core application's locking pool doubles its
share (HA-007). The required connection limit follows this formula:

`total = nodes × (2 × core pool + identity-service pool + metadata-service pool) + 50`

The extra 50 connections are headroom for the moment when all pools are exhausted. Example: three
nodes with 100 connections per pool need 3 × (200 + 100 + 100) + 50 = 1250 connections. Under heavy
load the per-node pools are raised, for example to 300 each, in all three components.

**Settings.** Per-component maximum open connections (HA-061); database `max_connections`.

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Scaling out (HA-009, HA-043) raises the demand. The database limit must be raised first.
- The advisory-lock pool (HA-007) is not part of this formula. Its size is unspecified
  relative to the budget.

**Interactions.** HA-007, HA-009, HA-052, HA-061.

### HA-063 · TLS to the database
Priority: **MVP**

**Behavior.** Connections to PostgreSQL can be encrypted and verified: the connection URL sets the
SSL mode to full verification (`verify-full`) and names the trusted root certificate. Client
certificate and key paths can be added for certificate-based authentication. Every component with
its own connection needs the same settings, using its own connection-string format (HA-061).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| SSL mode | PostgreSQL SSL mode | unspecified (examples: verify-full; disable in a non-TLS example) |
| Root certificate | CA used to verify the server | unset |
| Client certificate / key | For certificate authentication | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Server certificates without subject alternative names fail verification. Some older managed
  database certificates lack them and must be reissued.
- Some managed services require TLS for every connection.

**Interactions.** HA-033, HA-061.

### HA-064 · PostgreSQL high availability and failover
Priority: **MVP**

**Behavior.** For a highly available PostgreSQL setup, the connection URL lists all database nodes
(primary and replicas), separated by commas, with a connection-level parameter that requires the
target to be the primary. The driver finds the current primary. After a failover it reaches the new
primary on the next connection attempt. Every component with its own connection uses the same
multi-host URL. The database setup must give every application node a consistent view at all times
(HA-003).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Host list | All PostgreSQL nodes in the URL | single host |
| Target server type | Connect only to the primary | unset |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** How failover and advisory locks interact is unspecified. A
failover drops the open connections, which is the situation the advisory-lock recovery task
handles by re-acquiring held locks on a new connection (HA-007).

**Interactions.** HA-003, HA-007, HA-061.

### HA-065 · Database password rotation without downtime
Priority: **P1**

**Behavior.** The following procedure changes the database password while the service keeps
running:
1. Create a temporary database user with the same privileges, as a member of the application's
   role.
2. Switch every node to the temporary credentials and restart the nodes one at a time.
3. Wait until the original user has no sessions left (checked in `pg_stat_activity`).
4. Change the original user's password.
5. Switch every node back to the original user with the new password, again restarting one node
   at a time.
6. Wait until the temporary user has no sessions left, then drop it.

The whole procedure runs on one product version.

**Settings.** Database user and password (HA-061).

**Operations.** None (operator procedure).

**UI.** None.

**Limits and edge cases.** None beyond the ordering.

**Interactions.** HA-035, HA-061, HA-069.

### HA-066 · Schema migration on upgrade
Priority: **MVP**

**Behavior.** The first start after an upgrade may run database converters that change the schema.
In a rolling upgrade, nodes of the old and new version briefly run against the same database.

**Settings.** None.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** How mixed-version operation is kept safe during conversion is
unspecified.

**Interactions.** HA-069, HA-070.

## Sizing and requirements

### HA-067 · Hardware sizing tiers
Priority: **MVP**

**Behavior.** The main sizing input is how many clients are active at once. The table gives a
starting point, which the operator then adjusts for repository size, the package types in use and
how bursty the load is. Beyond 200 active clients no figures are given; such deployments need
individual sizing. Whether the figures apply per node or per deployment is unspecified.

| Active clients | CPU cores | Memory |
|---|---|---|
| 0 – 20 | 4 | 6 GB |
| 21 – 100 | 6 | 12 GB |
| 101 – 200 | 8 | 18 GB |
| More than 200 | unspecified (individual sizing) | unspecified |

The separately deployed UI server (HA-015) is stateless, needs no disk, and scales with the
profile:

| Profile | CPU request | Memory request | Memory limit |
|---|---|---|---|
| Small | 100m | 250 MiB | 500 MiB |
| Medium | 100m | 500 MiB | 1 GiB |
| Large | 200m | 800 MiB | 2 GiB |
| Extra large | 1 core | 1 GiB | 2 GiB |

The separately deployed event bus and event-collection pipeline use the same figures in every
profile: event bus 500m CPU request, 3500 MiB memory request and limit; event pipeline 500m CPU
request, 500 MiB memory request, 1 GiB memory limit.

**Settings.** See HA-052.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** HA-052.

### HA-068 · Platform prerequisites
Priority: **MVP**

**Behavior.** Requirements for the Helm deployment:

| Requirement | Value |
|---|---|
| Kubernetes | 1.27 or later |
| OpenShift | 4.14 or later |
| Helm | 3.17 or later (Helm 2 not supported) |
| Storage | Dynamic volume provisioning enabled; a default StorageClass that provides persistent storage |
| Tools | `kubectl` configured for the target cluster |
| Architectures | x86-64 and ARM64 |
| Amazon EKS | EBS CSI driver installed (not part of the chart) |

**Settings.** None.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Distributions that add their own hardening on top of Kubernetes may not
be fully supported.

**Interactions.** HA-029, HA-056.

### HA-074 · Preflight environment check
Priority: **P2**

**Behavior.** A standalone diagnostic command, run on the target host before installing outside
Kubernetes (HA-077, HA-078), tells the operator whether the machine and its surroundings meet the
minimum requirements. It has four checks, each runnable on its own:

| Check | What it verifies |
|---|---|
| System | CPU cores, memory and disk space against the minimum of the selected component; operating system and distribution support; write access to the chosen installation directory; a measurement of disk IOPS |
| Connectivity | That the ports the component needs are free, and that the external addresses it must reach are reachable (HA-076) |
| Database | That a connection with the given user, password and connection URL succeeds |
| All | The three checks above in one run |

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Component | Which component's requirements are checked (repository manager, scanner, distribution service) | the repository manager |
| Installation directory | Directory whose access is checked | a fixed system path *(unspecified here)* |
| Download host | Host name probed for binary downloads | the publisher's download host |
| Database type | PostgreSQL or one of the other engines of HA-059 | PostgreSQL |
| Database user, password, URL | Connection to test | required for the database check |

**Operations.** None (a command-line tool shipped separately from the product).

**UI.** None.

**Limits and edge cases.** The database check accepts only a plain connection URL; URLs with
extra parameters or options are rejected. The check is specified for virtual machines and bare
metal, not for Kubernetes.

**Interactions.** HA-067, HA-068, HA-076.

For trove the same checks fit a `preflight` subcommand of the server binary and a Helm
pre-install hook: PostgreSQL connection and version, S3 bucket write and read, reachability of
the OIDC issuer.

### HA-075 · Deployment-wide outbound proxy for service traffic
Priority: **P1**

**Behavior.** In networks where every outbound connection must pass a corporate forward proxy,
each node is given proxy settings that all its internal services use for external destinations.
This sits below the named proxy definitions that remote repositories select (RMT-048, RMT-049 in
[remote-repositories.md](remote-repositories.md)): it covers every outbound call a service makes,
not only repository traffic.

- Services never use the proxy to reach the node's own routing component (HA-012); they use it
  for third-party destinations such as the upstreams of remote repositories.
- The same settings are repeated for every service of the node.
- The standard `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` environment variables are honoured; when
  the node configuration sets the proxy, the configuration wins.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| HTTP proxy URL | Proxy for outbound plain-HTTP requests | unset |
| HTTPS proxy URL | Proxy for outbound HTTPS requests | unset |
| Bypass list | Destinations reached directly. Accepted forms: exact host name; a leading dot for a domain suffix that matches all subdomains (`.example.com` *(example)*); a single IP address; a CIDR range. `localhost` is always added. | empty plus `localhost` |

Each setting can also be given as an environment variable of the node.

**Operations.** None.

**UI.** None.

**Limits and edge cases.** Asterisk wildcards in the bypass list (`*.example.com`) are not
supported.

**Interactions.** HA-012, HA-019 (a TLS-intercepting proxy needs its CA in the trust store),
RMT-048 and RMT-049.

For trove, Go's standard environment-based proxy selection already understands these variables,
including domain suffixes and CIDR ranges in `NO_PROXY`; the remaining work is to route every
outbound client (upstreams, OIDC discovery, S3, webhooks) through one configured transport.

### HA-076 · Outbound destinations and air-gapped operation
Priority: **P1**

**Behavior.** A self-hosted installation must be allowed to reach a list of external destinations
through firewalls and proxies, grouped by purpose:

| Purpose | Used for |
|---|---|
| Release downloads | Installers, container images and Helm charts of the product, plus a CDN host; one download host must be reached directly, without a proxy, by the AI features |
| Security data feeds | Vulnerability database and package-intelligence synchronization (SEC-002 in [security-scanning-and-curation.md](security-scanning-and-curation.md)) |
| Entitlements and usage | License entitlements and consumption reports (OPS-097, OPS-099 in [operations-and-monitoring.md](operations-and-monitoring.md)) |
| Support | Upload of support bundles (OPS-031) |
| Integrations | A central integration service for third-party connectors and an extension store |
| AI services | Hosted model runtime and AI asset services |

All destinations use port 443 with HTTPS; some also speak gRPC over TLS, and one AI tunnel endpoint uses a second port.

**Air-gapped mode.** A single deployment switch, identical in Helm values and in the node
configuration, stops the entitlement connector from calling out; entitlements are then loaded by
hand (OPS-099). The other gaps are closed separately: images from a private registry (HA-056),
security data through offline synchronization (SEC-003), release sets through archive transfer
(DIST-042 in [distribution-and-lifecycle.md](distribution-and-lifecycle.md)).

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Air-gapped mode | Disable outbound entitlement calls | off |

**Operations.** None.

**UI.** None.

**Limits and edge cases.** None.

**Interactions.** HA-056, HA-074 (connectivity check), HA-075.

For trove the list shrinks to destinations the operator configures: upstream registries of
remote repositories, OIDC issuers and their key endpoints, the S3 endpoint, webhook targets and
the mail server. A self-hosted product with no call-home needs no air-gap switch; it needs this
list documented, and every one of these destinations must work through the proxy of HA-075.

## Other installation methods

### HA-077 · Container installation without Kubernetes
Priority: **P1**

**Behavior.** The application's container image also runs on a plain container host, with Docker,
Docker Compose or Podman, as a single node or, with restrictions, as a cluster. This path fits
existing container hosts and development environments, with limited production readiness and
limited cluster support.

- **Compose bundle.** A downloadable archive holds a compose file for the application and optional
  add-on compose files for a bundled PostgreSQL and a bundled reverse proxy; the add-ons are used
  together with the main file, not instead of it. An environment file in the bundle records the
  data root; install and upgrade rewrite it, so local edits must be backed up before an upgrade.
- **Interactive setup script.** Creates the directory tree with the ownership the containers
  need and asks for the installation directory, the host's IP address (IPv6 in brackets),
  whether this node joins an existing cluster, and whether to install the bundled database
  (no for production). The script is required for a working cluster; without it the operator
  creates the directories and ownership by hand.
- **Volume variant.** Named container volumes for application data and database data instead of
  host directories, suggested for single-node production.
- **Plain container run.** One host data directory is mounted into the container and holds the
  node configuration; the container runs under a fixed non-root UID and GID, which must own the
  directory.
- **Node identity.** With Podman and other engines the node's ID and the host's IP address must be
  set explicitly in the node configuration; otherwise they resolve to the container's address
  and other nodes and services cannot reach the node.
- **Cluster.** Every node carries identical external-database settings (HA-033) and reaches the
  same shared store (NFS or object storage, HA-004); clustering is switched on with every node
  allowed to run cluster tasks (HA-002); the first node must be fully up and its generated cluster
  key (HA-016) copied to every other node before they start, or the cluster does not form. At least
  three nodes are recommended; clients go through a load balancer (HA-022) to each node's HTTP
  port. Several nodes on one host need distinct host ports.
- **Order of start.** A bundled database container must be up before the application starts.
- **First start.** The onboarding wizard asks for a new administrator password (the default
  credentials are well known), the base URL (OPS-051 in
  [operations-and-monitoring.md](operations-and-monitoring.md)) and the license.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Installation directory | Host directory for configuration and data | a directory under the administrator's home *(unspecified here)* |
| Host IP address | Address announced to other nodes | detected address |
| Join existing cluster | Prepare the node as an additional cluster node | no |
| Install bundled database | Add the PostgreSQL container (evaluation only) | yes |

**Operations.** None (container engine commands).

**UI.** None.

**Limits and edge cases.**
- Required engine version: open, either Docker Engine 20.10.10 or later, or Docker Engine 25.0 or
  later with Compose v2; the spec decides.
- On macOS hosts the setup opens the data directory's permissions completely so that the
  container user can write; that is a workaround, not a model.
- The image is built on a minimal base image (HA-045); common debugging tools are missing and
  must be brought in separately.

**Interactions.** HA-002, HA-004, HA-010, HA-016, HA-022, HA-033, HA-074.

For trove a single static Go binary in a minimal image makes this path nearly free; a compose
file with PostgreSQL and an S3-compatible store is the natural evaluation and development setup
next to the Helm chart.

### HA-078 · Host installation from OS packages, archives and playbooks
Priority: **P2**

**Behavior.** For machines without Kubernetes there are per-component host installers and an
automation collection:

| Method | Targets | Production / cluster |
|---|---|---|
| RPM and Debian packages | RPM-based and Debian-based distributions, installed through the OS package manager | yes / manual |
| Linux archive | Any Linux distribution | yes / manual |
| Windows archive | Windows Server, repository manager only | limited / no |
| [Ansible](https://docs.ansible.com/) collection | Virtual machines and bare metal | yes / built in |

- **Linux archive.** Extracted into a chosen home directory; a bundled script registers it as a
  systemd or init.d service (recommended for production); alternatively a control script runs it
  in the foreground or as a background process and reports its status.
- **Cluster sequence.** As in HA-077: identical database settings on every node, shared storage,
  first node up and initialized, its cluster key copied to the others, then the other nodes.
- **Ansible collection.** Roles for the repository manager, the scanner and the distribution
  service, optionally PostgreSQL and a reverse proxy. Variables switch the bundled database off
  in favour of an external one, enable clustering, enable TLS on the reverse proxy (certificate,
  key, server name, worker processes), select an upgrade-only run and a target version, and
  re-apply the node configuration on every run (off by default, so later edits on the host are not
  overwritten). Secrets live in an Ansible Vault-encrypted variables file. Cluster installs and
  upgrades must run serially, first node alone and then the rest, because starting all nodes at
  once is not supported and fails. For PostgreSQL failover the connection URLs list several hosts
  and target the primary or a read-write session (HA-064).

**Settings.** Per method; see above.

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Moving from a playbook installation to the Helm chart is a migration, not an in-place change.
- Some newer components exist only for Helm or containers, not as host packages.
- The recommendation is Helm on Kubernetes whenever possible, the playbooks otherwise, and the
  per-component installers only for existing installations or special constraints.

**Interactions.** HA-010, HA-064, HA-077, HA-074; signed packages and package repository: OPS-076 in
[operations-and-monitoring.md](operations-and-monitoring.md).

### HA-079 · Single-host evaluation installer
Priority: **P2**

**Behavior.** A self-contained script turns one Linux machine into a complete evaluation
installation: it installs a lightweight Kubernetes distribution
([K3s](https://k3s.io/)), starts it, installs Helm, and deploys every component from the Helm
chart (HA-029) with the bundled database (HA-034). Components cannot be selected; an optional
component may be switched on. After the script ends, the pods need about 15 minutes to pull their
images; the script prints the follow-up commands: wait until all pods run, forward the bundled
edge proxy's port to the host, open the UI and log in with the default administrator
credentials. The port forward has to be started again after every reboot.

**Settings.**

| Setting | Meaning | Default |
|---|---|---|
| Kubernetes distribution version | Override through an environment variable | the one version the installer is tested with |

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- Evaluation only; production use is not permitted.
- Needs internet access (images and charts are pulled from the publisher), root or sudo, and
  recommends 16 GB of memory and 8 CPU cores; other Kubernetes versions than the tested one may
  fail.
- Removal: the distribution's own uninstall script removes everything; `helm uninstall` removes
  only the application and leaves the persistent volumes, which must be deleted separately.

**Interactions.** HA-029, HA-034, HA-071, UI onboarding (UI-007 in
[ui-inventory.md](ui-inventory.md)).

## Upgrade and removal

### HA-069 · Rolling upgrade without downtime
Priority: **MVP**

**Behavior.** In a cluster, an upgrade is rolled out one node at a time, and the service stays
available throughout. A single-node deployment is unavailable during its upgrade. An upgrade
without downtime needs at least three nodes (HA-001). In Kubernetes, an upgrade is a
`helm upgrade` with the new chart version and the same values files and keys. The chart validates
the configuration before it changes anything (HA-030).

**Settings.** None beyond the chart values.

**Operations.** None.

**UI.** None.

**Limits and edge cases.**
- The same custom values file must be passed on every upgrade.
- The cluster keys must be passed on every upgrade (HA-031), and so must a static load balancer IP
  (HA-038) and an auto-generated bundled-database password (HA-034).
- Clusters upgraded from a primary-based release keep the legacy primary flag until it is switched
  (HA-002).

**Interactions.** HA-030, HA-047, HA-066, HA-070.

### HA-070 · Upgrade preparation
Priority: **MVP**

**Behavior.** Before an upgrade, the following is required:
- a full backup of the data directory and the database;
- reading the release notes of the target version for breaking changes and extra steps;
- making sure both cluster keys are available. Upgrades from releases that did not require them
  must first read the existing keys (the trust key from the UI's security settings after
  re-entering the password, the encryption key from a running node) and pass them in.

With the bundled evaluation database, additional steps apply:
- pin the database image tag to the running version;
- set the database-upgrade acknowledgement (HA-030);
- when the database subchart changes its major structure: back up credentials and volume claims,
  delete the database StatefulSet with orphan cascading and its Secret, and move renamed value keys
  (for example the password key) to the new structure.

**Settings.** See HA-030, HA-034.

**Operations.** None.

**UI.** Security settings, connection details (to read the service trust key).

**Limits and edge cases.** Value keys left under their old names make the upgrade fail.

**Interactions.** HA-016, HA-030, HA-034, HA-069.

### HA-071 · Uninstall and data cleanup
Priority: **MVP**

**Behavior.** Two levels of removal:
- **Uninstall.** `helm uninstall` removes the release. The persistent volume claims of the
  StatefulSet survive it. After a short wait (example: 90 seconds), they are
  deleted explicitly by label. External resources (the object-storage bucket and the external
  database) are removed separately with the cloud provider's tools.
- **Delete.** Removes the deployment including its data volumes, for a single node or a cluster.
  All data is lost, so a backup is mandatory first. Uninstalling first is not required.

**Settings.** None.

**Operations.** None (Helm and kubectl operations).

**UI.** None.

**Limits and edge cases.**
- Open: uninstall either preserves the volume claims or is followed by deleting them by label; the
  spec decides.
- A chart-generated proxy certificate Secret marked "keep" survives removal (HA-041).

**Interactions.** HA-005, HA-041, [operations-and-monitoring.md](operations-and-monitoring.md)
(backup).

### HA-072 · License installation and license gating of clustering
Priority: **Out of scope**

**Behavior.** With license gating, clustering, replica counts above one and some sizing profiles
require an installed license. Licenses are installed through the UI, an API, or a Kubernetes
Secret at first deployment. All licenses for a cluster must be entered at once: entering them one
at a time can redirect the administrator to a node that has no license yet. Mixing installation
methods on a running cluster can disable clustering. A self-hosted open product has no license
gating.

## Notes for implementers

- Load balancers act on readiness. A readiness check that only covers the core process reports OK
  while the UI or routing layer is down (HA-023). Readiness must cover everything a request needs:
  database, object storage, and every in-process subsystem that serves traffic.
- Proxies in front of the system must not limit body size, must not buffer requests, must allow
  long transfers, and must not cache responses (HA-024). The defaults of common Ingress
  controllers violate all four, and the failures (413, stalls, stale metadata) appear long after
  the installation looked healthy.
- Encoded slashes (for example `%2f` in scoped npm package names) must reach the application
  unchanged through every proxy hop. The reference proxy configurations forward the original URI.
  The bundled proxy normalizes by default and needs an opt-in to pass the raw URI (HA-024,
  HA-041), so a proxy normalizing paths is a realistic deployment state the application must
  diagnose.
- Absolute URLs in responses (upload `Location` headers, package metadata, redirects) come from
  the public base URL. When the forwarded headers or the base URL are wrong, reads keep working and
  only multi-step writes break (HA-025, HA-027).
- The cluster encryption key must be identical on every replica and must survive a reinstall
  against an existing database. A new key against an old database fails at startup, and no rotation
  path exists (HA-016). A design that encrypts stored secrets should support key identifiers and
  re-encryption from the start.
- Size the database connection limit for the cluster, not for one node: per-node pools times nodes,
  plus the locking pools, plus headroom. Scaling out without raising the limit exhausts connections
  (HA-062).
- Advisory locks belong to a database session. A transaction-mode pooler breaks them, and a
  failover or reconnect drops them. Holders must re-acquire them, or treat a dropped connection as
  a lost lease (HA-007, HA-064).
- Silently ignoring unknown configuration keys (HA-036) is a known trap. Strict validation that
  rejects unknown keys at startup avoids it.
- A chart that generates secrets at install time (passwords, keys, certificates) breaks on the next
  upgrade unless the generated value is looked up and reused. Failing fast on missing secrets is
  the safer contract (HA-030, HA-031, HA-034).
- StatefulSet volume claims survive scale-down and uninstall. Operators expect explicit cleanup by
  label, and a reinstall silently reattaches the old claims (HA-005, HA-071).
