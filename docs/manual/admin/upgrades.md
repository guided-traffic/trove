# Upgrades

This page explains how trove versions its releases, how to upgrade an installation with Helm, how the database migrations run during an upgrade, which corrections of stored data run afterwards, what to check afterwards, and which parts of the upgrade path are not verified yet.

## Versions and release notes

trove is released from its main branch by an automated pipeline that derives the version from the commit messages:

| Change in the release | Version step |
|---|---|
| A fix or a performance improvement | patch, for example `0.5.0` to `0.5.1` |
| A new feature | minor, for example `0.5.1` to `0.6.0` |
| A breaking change | minor as well, while the major version is 0 |

trove is pre-1.0 and stays on major version 0 until a 1.0 release is decided. Under major version 0 a minor release can contain a breaking change, such as a renamed configuration key or a changed default. The release notes, generated from the same commit messages, list breaking changes in a section of their own. Read the notes of every release between your version and the target on the [releases page](https://github.com/guided-traffic/trove/releases) before you upgrade. Automated dependency updates are released as patch releases.

The image `docker.io/guidedtraffic/trove` and the Helm chart carry the same version as the release. A chart installs the image of its own version unless you set `image.tag` or `image.digest`, so upgrading the chart upgrades trove.

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
4. Check the background data migrations of the running version. A migration of the target that removes stored data can depend on one of them; its release notes name it, and it must show `succeeded`:

   ```shell
   curl -s -H "Authorization: Bearer $TROVE_TOKEN" https://trove.example.com/api/v1/system/migrations   # example: a token with settings:read and your base URL
   ```

   The answer lists each background migration under `background` with its `state` (`pending`, `running`, `failed` or `succeeded`), the rows it has `processed` and when it started. The migration Job of the target checks the same: it refuses such a migration while the background migration it depends on has not succeeded (exit status 3, nothing applied), and the previous version keeps serving. No migration of this version depends on a background migration.

## Upgrade from a release before the split into roles

The releases before the split into `api` and `packages` ran one Deployment, `trove-web`, behind the Service `trove`. A Deployment cannot be renamed, and Helm applies a release in one pass, so one `helm upgrade` from that shape would leave the edge without a ready backend while the new pods start. An ingress controller also applies a removed backend sooner than a moved route, so an upgrade that moves the routes and removes the old Service at once answers `503` for a second or two. The upgrade therefore takes three steps, each of which either moves the edge or adds or removes backends, joined by the values `web.enabled` and `web.routed`:

Before step 1, check what your installation admits by the pods' labels outside the chart: a NetworkPolicy of your PostgreSQL, Valkey and Sentinels, or of anything else trove calls, that selects `app.kubernetes.io/component: web` (and `worker`) must also select `api` and `packages`, or the new pods cannot reach the database and stay not ready. Keep `web` in it until step 3 is done. The same holds for labels your cluster-wide policies key on (`web.podLabels`): give `api.podLabels` and `packages.podLabels` the ones each role needs (`api` signs users in through the identity provider and reaches the object store for the UI's transfers; `packages` reaches the object store and the upstreams of remote repositories). Size the connection budget for steps 1 and 2 as well: `web`, `api` and `packages` run side by side, and the migration Job of step 1 refuses a PostgreSQL whose `max_connections` is below the budget the chart's NOTES print.

1. `helm upgrade … --set web.enabled=true` with your values otherwise unchanged. The chart keeps `trove-web` (rolled to the new release) and the Service `trove`, and the edge keeps sending every host whole to it. The Deployments and Services of `api`, `packages` and `worker` start beside it. Wait until `kubectl -n trove rollout status` reports `trove-api` and `trove-packages` ready, and add the `api.*` and `packages.*` sizing you want.
2. `helm upgrade … --set web.enabled=true --set web.routed=false`. The edge switches to the path routing onto the role Services, which have been ready since step 1, and a further endpoint's Service to the pods of its role. Nothing is removed: `trove-web` and the Service `trove` keep running, so a request the ingress controller still sends by the old route is served. If you route with your own Gateway API HTTPRoute instead of the chart's Ingress, move it to `trove-api` and `trove-packages` now. Move in-cluster callers from the Service `trove` to `trove-api` and `trove-packages`.
3. `helm upgrade … --set web.enabled=false` (the default). Helm deletes `trove-web`, its endpoint pools and the Service `trove`, which no route names any more; their pods drain first.

**Administration › System › System status** shows a group `web` until step 3, next to `api`, `packages` and `worker`, and none after it. `helm status` prints which step the release is at and the next one. Where the chart can read the cluster (`helm upgrade`, `helm install --dry-run=server`), it reads where the edge points while `trove-web` exists: the chart's Ingress routing to the Service `trove`, or the Service of a further endpoint selecting the `web` pods, means the edge has not moved yet. It refuses the upgrade that moves the edge while `trove-api`, `trove-packages` or one of their pools is missing or not ready, and it refuses step 3 while the edge has not moved (step 2 was skipped); both messages name the three steps. Once the edge has moved, any upgrade with the values of step 2 or 3 goes through even while a Deployment of the target shape is not ready (a crash loop, an autoscaler scaling up, a rollout in progress): it moves nothing, and you need not go back to step 1 to apply a fix. Under `helm template` there is nothing to read, and a route outside the chart is never read, so follow the steps.

Roll back one step at a time: from step 3 to the revision of step 2, then from step 2 to the revision of step 1 (`helm history trove` lists the revisions). Each of these rollbacks changes one kind of thing and has no gap: rolling back step 3 brings `trove-web` back without a route to it, rolling back step 2 moves the edge back to `trove`, whose pods still run, and rolling back step 1 rolls `trove-web` back. `helm rollback` applies a stored release without rendering it, so no guard can stop a rollback from step 3 straight to a revision of step 1: it recreates `trove-web` and points the Ingress at the Service `trove` at once, and the host answers `503` until the restored `web` pods are ready.

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

### The web UI during the rollout

While the rollout runs, a browser's requests reach pods of both versions, and each version's UI consists of files named after their content. So that a page loads whichever pod answers, every `api` pod (and `web` pod of the bridge) stores the UI files of its version in the object store before it reports ready, and serves the files of the other version from there. You need to do nothing for it:

- A new `api` pod logs `the UI's hashed files are in the shared asset store` with the number of files once it has stored them. The first pod of a new version stores about 270 files (about 5 MB in both languages); the pods after it find them there.
- The files of a version stay in the object store for 7 days after its last pod stopped, so that a browser tab opened before the upgrade keeps working, and are then removed by the garbage collection of the object store. The database table `ui_assets` names them; it is not part of a backup, and the pods store their files again after a restore.
- If the object store cannot be reached during the rollout, a page of one version can fail to load a file from a pod of the other one: the browser shows an empty page, the pod answers `503` and logs `a hashed file of another release could not be read from the shared asset store`. Reloading the page fixes it. A pod that cannot store its files within a minute starts anyway and logs an error; it tries again every minute.
- The first version that has this store cannot serve the files of the version you upgrade from, which never stored them: during that one upgrade a browser can still show an empty page once, until it is reloaded. Upgrades from that version on are not affected.

### Without the migration hook

With `migrations.mode: disabled` the chart renders no Job, and you run the migration yourself, with the new image and the same configuration and database credentials, before you roll the Deployments:

```shell
trove migrate up --config /etc/trove/trove.yaml      # example path; prints a JSON summary on success
```

A new replica that starts before the migration has run stays unready and waits until the schema is current.

## How the migrations work

- The migrations are embedded in the binary and numbered. Each runs in its own transaction and is recorded with a checksum; a recorded migration whose file has changed is refused.
- One run at a time: the migration lock is a PostgreSQL advisory lock, so a second Job or a manual run waits for the first.
- Additive migrations never drop, rename or tighten anything; the runner rejects such statements in them. A replica of an older version stays ready after them, because it still finds everything it uses.
- A migration that removes something carries the minimum trove version it needs. The runner refuses it (exit status 3, nothing applied) while a live replica runs an older version, and once it is applied, a replica of an older version exits with `schema requires trove >= <version>`. Every migration of this version is additive.
- A migration that removes something a background data migration converts also names that background migration. The runner refuses it (exit status 3, nothing applied) until the background migration has succeeded, and names it with its state.
- A replica checks the schema when it starts and keeps checking it: if the schema is behind the binary, the replica stays or becomes unready until the migration has run.

`GET /api/v1/system/migrations` shows the state without taking the lock, to a caller with `settings:read`: the schema version and its compatibility floor, the version and required schema of the replica that answers, the migrations that replica's binary has and the database has not applied (`pending`, each with what the runner checks before it), every applied migration with its release and time, and the background data migrations with their states. During a rolling upgrade, replicas of both versions answer, each for its own binary. `trove migrate status` prints the same report from the same code and adds whether that binary may serve:

```shell
curl -s -H "Authorization: Bearer $TROVE_TOKEN" https://trove.example.com/api/v1/system/migrations   # example: a token with settings:read
kubectl -n trove exec deploy/trove-worker -- /usr/local/bin/trove migrate status
```

## Background data migrations

A correction of stored data runs after the upgrade as a job on a worker, not in the migration Job, so the Job does not take longer with more data. This version has one, `data_migration_nuget_semver2`. It finds the NuGet versions stored by 0.9.0 or older that have a dependency range with a SemVer 2.0.0 bound, such as `[2.0.0-rc.1, )`, and marks them SemVer 2.0.0. NuGet clients older than 4.3 then no longer get these versions in the SemVer 1 registrations or in a search without `semVerLevel=2.0.0`.

- The first worker that starts after the upgrade queues it. While a pod of 0.9.0 or older is still live, it waits: the result says `state: waiting` and names those pods, and it tries again a minute later. Once the rollout has finished, it runs.
- Follow it under **Administration › System › Jobs** with the kind `data_migration_nuget_semver2`. A run that reached the end shows `state: recomputed` with the number of versions it `examined` and `changed` and the `packages` they belong to. A version it could not read is counted as `unreadable` and named in the worker's log.
- `GET /api/v1/system/migrations` lists it under `background` as `nuget_semver2` (release `0.10.0`): `pending` until a worker runs it and while it waits for older pods, `running` while it runs, `failed` when its last run failed, and `succeeded` once its completion is recorded, with the number of versions it examined.
- It runs once per installation: trove records the completion in the database, and a worker that starts later queues nothing. A package it changed gets a new revision, so clients and caches see the change.

## After the upgrade

1. `helm status trove -n trove` shows `deployed`, and `helm history trove -n trove` lists the new revision.
2. The Job log ends with a JSON summary: `applied` lists the migrations of this run, `nothingToDo` is `true` when none was pending, and `schemaApplied` is the schema version now.
3. `GET /api/v1/system/migrations` (see [How the migrations work](#how-the-migrations-work)) answers with the new version in `binary.version`, an empty `pending`, and every entry of `background` `succeeded`. A background migration that is still `pending` or `running` finishes on its own; one that shows `failed` has a failed job under **Administration › System › Jobs** whose error names the cause.
4. Every pod of `trove-api`, `trove-packages` and `trove-worker` is ready and none restarts repeatedly.
5. **Administration › System › System status** shows **All ready**; every replica in the replica table shows the new **Version**, and the **Database** part shows the new **Schema version**.
6. Sign in, open a repository and download a file, as a quick check of the full request path.

[Monitoring and operations](operations.md) describes the metrics and logs to watch.

## Rolling back

`helm rollback` runs no migration job: the migration hook runs on install and upgrade only. The schema therefore stays at the newer version, and the older pods start only if the compatibility check admits them, which it does after additive migrations. Rolling back is not tested in this version. If the older version must also run on the older schema, restore the database backup you took before the upgrade along with the rollback; everything written to trove since that backup is lost.

After a rollback to 0.9.0 or older, NuGet versions pushed during the rollback are classified the old way again, and `data_migration_nuget_semver2` does not run again by itself when you upgrade once more. Remove its record, then restart a worker pod, which queues it:

```shell
psql "$DATABASE_URL" -c "DELETE FROM trove.system_state WHERE key = 'nuget_semver2_recomputed'"   # example connection and schema
kubectl -n trove rollout restart deploy/trove-worker
```

## What is not verified yet

- **Mixed versions during the rollout.** For the length of a rolling update, replicas of the old and the new version serve requests side by side. The schema check and the UI's shared files are built for that, and the rolling upgrade from the previous release is tested under npm, NuGet and browser load on a test cluster; it has not been run on other clusters. If an interruption matters to you, upgrade in a maintenance window.
- **Skipping releases.** The runner applies every pending migration in order, so an upgrade across several releases is a single migration run. An upgrade path across several published releases is not covered by a test.
- **Rollback**, as described above.

[Known limitations](limitations.md) lists the other gaps of this version.
