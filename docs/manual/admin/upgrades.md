# Upgrades

This page explains how trove versions its releases, what an upgrade can and cannot keep before the first stable release, how to upgrade an installation with Helm, how the database migrations run during an upgrade, which corrections of stored data run afterwards, what to check afterwards, and which parts of the upgrade path are not verified yet.

## Versions and release notes

trove is released from its main branch by an automated pipeline that derives the version from the commit messages:

| Change in the release | Version step |
|---|---|
| A fix or a performance improvement | patch, for example `0.5.0` to `0.5.1` |
| A new feature | minor, for example `0.5.1` to `0.6.0` |
| A breaking change | minor as well, while the major version is 0 |

trove is pre-1.0 and stays on major version 0 until a 1.0 release is decided. Under major version 0 a minor release can contain a breaking change, such as a renamed configuration key or a changed default. The release notes, generated from the same commit messages, list breaking changes in a section of their own. Read the notes of every release between your version and the target on the [releases page](https://github.com/guided-traffic/trove/releases) before you upgrade. Automated dependency updates are released as patch releases.

The image `docker.io/guidedtraffic/trove` and the Helm chart carry the same version as the release. A chart installs the image of its own version unless you set `image.tag` or `image.digest`, so upgrading the chart upgrades trove.

## Before the first stable release

trove is pre-alpha, and until its first stable release it keeps no backward compatibility. Plan every upgrade with this in mind:

- **An upgrade may require a fresh database.** A release may change a migration that an earlier release already applied. The migration Job then refuses your database with `migration <name>.sql was changed after this database applied it (checksum mismatch)`, applies nothing, and `helm upgrade` fails while the old pods keep serving. The way forward is a new, empty database for the new release; the data of the old one is not carried over.
- **No rollback is supported.** Once the migration Job of the new release has run, nothing guarantees that the previous release works against the schema it left. `helm rollback` is not a way back; the backup you took before the upgrade is (see [Rolling back](#rolling-back)).
- **Old and new pods are not guaranteed to work side by side.** During the rolling update both versions serve requests, and a change between them may break requests for that time. Upgrade in a maintenance window if an interruption matters to you.
- **Anything can change.** A release may rename or remove a configuration key or a chart value, change the Management API or change a stored format, without a deprecation period or an alias. Read the release notes, as above.

This ends with the first stable release.

## Before you upgrade

1. Read the release notes, as above, and adjust your values file to any renamed or removed key.
2. Back up the PostgreSQL database with the tools of your database platform, such as the backups of [CloudNativePG](https://cloudnative-pg.io/documentation/). Migrations are not reversible, and trove has no backup command in this version. The object store needs no extra step: migrations do not touch it.
3. Check that the new chart accepts your values. Rendering runs the chart's schema and its checks without changing anything:

   ```shell
   helm repo update trove
   helm search repo trove/trove --versions
   helm template trove trove/trove -n trove -f values.yaml --version <version> > /dev/null   # example: the target version
   ```

   This does not check the keys inside `config`; the new binary checks them when the migration Job starts (see below).

## Upgrade from a release before the split into roles

The releases before the split into `api` and `packages` ran one Deployment, `trove-web`, behind the Service `trove`. A Deployment cannot be renamed, and Helm applies a release in one pass, so one `helm upgrade` from that shape would leave the edge without a ready backend while the new pods start. The upgrade takes two steps, joined by the value `web.enabled`:

Before step 1, check what your installation admits by the pods' labels outside the chart: a NetworkPolicy of your PostgreSQL, Valkey and Sentinels, or of anything else trove calls, that selects `app.kubernetes.io/component: web` (and `worker`) must also select `api` and `packages`, or the new pods cannot reach the database and stay not ready. Keep `web` in it until step 2 is done. The same holds for labels your cluster-wide policies key on (`web.podLabels`): give `api.podLabels` and `packages.podLabels` the ones each role needs (`api` signs users in through the identity provider and reaches the object store for the UI's transfers; `packages` reaches the object store and the upstreams of remote repositories). Size the connection budget for step 1 as well: `web`, `api` and `packages` run side by side, and the migration Job of step 1 refuses a PostgreSQL whose `max_connections` is below the budget the chart's NOTES print.

1. `helm upgrade … --set web.enabled=true` with your values otherwise unchanged. The chart keeps `trove-web` (rolled to the new release) and the Service `trove`, and the edge keeps sending every host whole to it. The Deployments and Services of `api`, `packages` and `worker` start beside it. Wait until `kubectl -n trove rollout status` reports `trove-api` and `trove-packages` ready, move in-cluster callers from the Service `trove` to `trove-api` and `trove-packages`, and add the `api.*` and `packages.*` sizing you want.
2. `helm upgrade … --set web.enabled=false` (the default). The edge switches to the path routing onto the role Services, which have been ready since step 1, and Helm deletes `trove-web`, its endpoint pools and the Service `trove`; their pods drain first.

**Admin area › System › System status** shows a group `web` between the steps, next to `api`, `packages` and `worker`, and none after step 2. Where the chart can read the cluster (`helm upgrade`, `helm install --dry-run=server`), it refuses `web.enabled: false` while `trove-web` exists and `trove-api` or `trove-packages` is not ready, and names the two steps; under `helm template` there is nothing to read, so follow the steps. Rolling back from step 2 recreates `trove-web` while the edge already points at the Service `trove`, so the host answers `503` while the restored pods start; rolling back step 1 has no gap.

## Upgrade with Helm

```shell
helm upgrade trove trove/trove -n trove -f values.yaml --version <version>     # example: the target version
kubectl -n trove logs job/trove-migrate
kubectl -n trove rollout status deployment/trove-api
kubectl -n trove rollout status deployment/trove-packages
kubectl -n trove rollout status deployment/trove-worker
```

`helm upgrade` runs these steps in order:

1. Helm validates the values. A value the new chart does not know, or a required value that is missing, stops the upgrade before anything changes.
2. Helm deletes the previous migration Job and starts a new `trove-migrate` Job with the new image and the new configuration. With `secrets.databaseMigration` set, the Job signs in as that schema owner.
3. The Job loads the configuration (an invalid key stops it here), checks that the database's `max_connections` covers the connection pools of the configured replica counts, takes the migration lock, waiting up to `migrate.advisory_lock_wait` (5 minutes) for another run to finish, and applies every pending migration in order. It exits successfully when nothing was pending.
4. Only after the Job has succeeded does Helm update the Deployments. Kubernetes then replaces the `api`, `packages` and `worker` pods one at a time: a new pod is started before an old one stops, and an old pod drains its requests or jobs before it exits.

If the Job fails, `helm upgrade` fails and the Deployments stay as they were: the replicas of the previous version keep serving. The Job retries a failed run twice (`migrations.backoffLimit`) within 30 minutes (`migrations.activeDeadlineSeconds`); a run that a node drain evicts does not count as a failure, and a refusal (exit status 3) fails the Job at once because retrying cannot change it. The log of the Job names the cause.

### Without the migration hook

With `migrations.mode: disabled` the chart renders no Job, and you run the migration yourself, with the new image and the same configuration and database credentials, before you roll the Deployments:

```shell
trove migrate up --config /etc/trove/trove.yaml      # example path; prints a JSON summary on success
```

A new replica that starts before the migration has run stays unready and waits until the schema is current.

## How the migrations work

- The migrations are embedded in the binary and numbered. Each runs in its own transaction and is recorded with a checksum; a recorded migration whose file has changed is refused.
- One run at a time: the migration lock is a PostgreSQL advisory lock, so a second Job or a manual run waits for the first.
- A migration may add, drop, rename or tighten anything. Before the first stable release nothing keeps a replica of the previous version working after it (see above).
- A migration can carry the minimum trove version it needs. The runner refuses it (exit status 3, nothing applied) while a live replica runs an older version, and once it is applied, a replica of an older version exits with `schema requires trove >= <version>`. One migration of this version carries such a minimum, `0.1.0`, which every release meets.
- A replica checks the schema when it starts and keeps checking it: if the schema is behind the newest migration its binary embeds, the replica stays or becomes unready until the migration has run.

`trove migrate status` shows the state without taking the lock: the applied and pending migrations, the schema version this binary requires and whether it may serve.

```shell
kubectl -n trove exec deploy/trove-api -- /usr/local/bin/trove migrate status
```

## Background data migrations

A correction of stored data runs after the upgrade as a job on a worker, not in the migration Job, so the Job does not take longer with more data. This version has one, `data_migration_nuget_semver2`. It finds the NuGet versions stored by 0.9.0 or older that have a dependency range with a SemVer 2.0.0 bound, such as `[2.0.0-rc.1, )`, and marks them SemVer 2.0.0. NuGet clients older than 4.3 then no longer get these versions in the SemVer 1 registrations or in a search without `semVerLevel=2.0.0`.

- The first worker that starts after the upgrade queues it. While a pod of 0.9.0 or older is still live, it waits: the result says `state: waiting` and names those pods, and it tries again a minute later. Once the rollout has finished, it runs.
- Follow it under **Admin area › System › Jobs** with the kind `data_migration_nuget_semver2`. A run that reached the end shows `state: recomputed` with the number of versions it `examined` and `changed` and the `packages` they belong to. A version it could not read is counted as `unreadable` and named in the worker's log.
- It runs once per installation: trove records the completion in the database, and a worker that starts later queues nothing. A package it changed gets a new revision, so clients and caches see the change.

## After the upgrade

1. `helm status trove -n trove` shows `deployed`, and `helm history trove -n trove` lists the new revision.
2. The Job log ends with a JSON summary: `applied` lists the migrations of this run, `nothingToDo` is `true` when none was pending, and `schemaApplied` is the schema version now.
3. Every pod of `trove-api`, `trove-packages` and `trove-worker` is ready and none restarts repeatedly.
4. **Admin area › System › System status** shows **All ready**; every replica in the replica table shows the new **Version**, and the **Database** part shows the new **Schema version**.
5. Sign in, open a repository and download a file, as a quick check of the full request path.

[Monitoring and operations](operations.md) describes the metrics and logs to watch.

## Rolling back

Before the first stable release no rollback is supported. `helm rollback` runs no migration job: the migration hook runs on install and upgrade only. The schema therefore stays at the newer version, and the older pods start whenever the schema is at least the one their binary needs and no applied migration requires a newer trove, but nothing guarantees that they work against the newer schema. To go back, restore the database backup you took before the upgrade along with the rollback; everything written to trove since that backup is lost.

After a rollback to 0.9.0 or older, NuGet versions pushed during the rollback are classified the old way again, and `data_migration_nuget_semver2` does not run again by itself when you upgrade once more. Remove its record, then restart a worker pod, which queues it:

```shell
psql "$DATABASE_URL" -c "DELETE FROM trove.system_state WHERE key = 'nuget_semver2_recomputed'"   # example connection and schema
kubectl -n trove rollout restart deploy/trove-worker
```

## What is not verified yet

- **Mixed versions during the rollout.** For the length of a rolling update, replicas of the old and the new version serve requests side by side. Before the first stable release this is not supported (see above); if an interruption matters to you, upgrade in a maintenance window.
- **Skipping releases.** The runner applies every pending migration in order, so an upgrade across several releases is a single migration run. An upgrade path across several published releases is not covered by a test, and before the first stable release it may require a fresh database like any other upgrade.
- **Rollback**, which is not supported before the first stable release, as described above.

[Known limitations](limitations.md) lists the other gaps of this version.
