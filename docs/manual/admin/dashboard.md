# Admin dashboard

The dashboard is the first screen of the admin area. It shows the state of the installation in one place: the roles and their load, the dependencies and their checks, and how much content and how many principals exist, with links to the screens that hold the details.

**Admin** in the top bar opens it for everyone who holds the capability `settings:read` (roles `admin`, `security-admin`, `auditor`). Without `settings:read`, **Admin** opens **Repositories**, or **Projects** for a holder of `projects:read`.

As an administrator of the installation (the role `admin`), you also reach the admin area from the [search field](../user/search.md):
it offers the pages of the admin area, users and groups, last and apart, each marked **Admin area**. Only choosing such an entry opens
the admin area; Enter and the results page always stay where you work. The other roles reach their screens through **Admin**.

The screen refreshes every 15 seconds; **Pause automatic refresh** stops that and **Refresh now** loads it once.

## What it shows

**The instance bar**: whether every role has a ready replica (**All roles ready**, **Degraded**, **A role is not ready**), the version, the **Base URL** and how many replicas are ready, with a link to [System status](operations.md#system-status).

**Components**: one card per role (`api`, `packages`, `worker`, and `all` on a development installation) with:

| Figure | What it is |
|---|---|
| **N of M ready** | Ready replicas of the role against the live ones; a role without any replica says **No replica of this role is running** |
| **Nodes** | Distinct Kubernetes nodes the ready replicas run on |
| **Requests**, **Server errors** | Requests per second the role's replicas answered on their client listener, and the share of them answered with a `5xx` status, summed over the replicas |
| **Latency p95** | The 95th percentile of the request duration, the worst of the role's replicas; **none in the last period** when no request was answered |
| **In flight** | Requests being answered right now, summed |
| **CPU**, **Memory** | Cores in use against the cores the replicas may use, and memory held against the containers' limits, summed over the replicas; the bar appears when every replica knows its limit |

Each replica measures these figures itself over the time since its previous heartbeat (every 10 seconds by default, `system.heartbeat_interval`) and writes them with its heartbeat; the dashboard adds them up per role. A replica that has not sent a heartbeat since it started is not counted yet, and the card says how many replicas reported. A replica whose heartbeat is older than `system.heartbeat_stale_after` is lost and left out.

**Dependencies**: PostgreSQL, Valkey and the object store, each with the state of its readiness check over all live replicas: **Connected** with the duration of the slowest check, or how many replicas fail it. PostgreSQL adds its server and schema version, the pooler mode and the connections in use of the replica that answered the page; Valkey its mode, Sentinels, TLS verification and the replicas whose view of Valkey is degraded; the object store its bucket, the content it holds and the objects queued for deletion.

**Content and access**: repositories by class, packages and their versions, endpoints, Bastions, projects, users, groups and service accounts (disabled ones included) and active tokens of every kind (neither expired nor revoked), each card with links to its screens; **New repository** for holders of `repositories:write-config`. **Operations**: running, queued and in the last 24 hours failed jobs, and the active key of the stored secrets.

## How to read the figures

- **Stored content** is what trove's metadata records: one object per distinct content, so a file in several repositories counts once. It is not what the object store bills: old object versions, incomplete uploads and the store's own overhead are not in it. A project's **Storage** is another figure: the full size of its files, counted per reference ([Projects](projects.md#what-a-project-is)).
- The counts are taken by the replica that answers and reused for 30 seconds, so two refreshes in a row can show the same numbers; the line under the cards says when they were counted.
- The figures are the current state, not a history. For trends and alerts, use the metrics of the ops listener ([Monitoring and operations](operations.md#metrics)).
