# Upgrades

This page explains how trove versions its releases, how to upgrade an installation with Helm, how the database migrations run during an upgrade, what to check afterwards, and which parts of the upgrade path are not verified yet.

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

## Upgrade with Helm

```shell
helm upgrade trove trove/trove -n trove -f values.yaml --version <version>     # example: the target version
kubectl -n trove logs job/trove-migrate
kubectl -n trove rollout status deployment/trove-web
kubectl -n trove rollout status deployment/trove-worker
```

`helm upgrade` runs these steps in order:

1. Helm validates the values. A value the new chart does not know, or a required value that is missing, stops the upgrade before anything changes.
2. Helm deletes the previous migration Job and starts a new `trove-migrate` Job with the new image and the new configuration. With `secrets.databaseMigration` set, the Job signs in as that schema owner.
3. The Job loads the configuration (an invalid key stops it here), checks that the database's `max_connections` covers the connection pools of the configured replica counts, takes the migration lock, waiting up to `migrate.advisory_lock_wait` (5 minutes) for another run to finish, and applies every pending migration in order. It exits successfully when nothing was pending.
4. Only after the Job has succeeded does Helm update the Deployments. Kubernetes then replaces the `web` and `worker` pods one at a time: a new pod is started before an old one stops, and an old pod drains its requests or jobs before it exits.

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
- Additive migrations never drop, rename or tighten anything; the runner rejects such statements in them. A replica of an older version stays ready after them, because it still finds everything it uses.
- A migration that removes something carries the minimum trove version it needs. The runner refuses it (exit status 3, nothing applied) while a live replica runs an older version, and once it is applied, a replica of an older version exits with `schema requires trove >= <version>`. Every migration of this version is additive.
- A replica checks the schema when it starts and keeps checking it: if the schema is behind the binary, the replica stays or becomes unready until the migration has run.

`trove migrate status` shows the state without taking the lock: the applied and pending migrations, the schema version this binary requires and whether it may serve.

```shell
kubectl -n trove exec deploy/trove-web -- /usr/local/bin/trove migrate status
```

## After the upgrade

1. `helm status trove -n trove` shows `deployed`, and `helm history trove -n trove` lists the new revision.
2. The Job log ends with a JSON summary: `applied` lists the migrations of this run, `nothingToDo` is `true` when none was pending, and `schemaApplied` is the schema version now.
3. Every pod of `trove-web` and `trove-worker` is ready and none restarts repeatedly.
4. **Administration › System › System status** shows **All ready**; every replica in the replica table shows the new **Version**, and the **Database** part shows the new **Schema version**.
5. Sign in, open a repository and download a file, as a quick check of the full request path.

[Monitoring and operations](operations.md) describes the metrics and logs to watch.

## Rolling back

`helm rollback` runs no migration job: the migration hook runs on install and upgrade only. The schema therefore stays at the newer version, and the older pods start only if the compatibility check admits them, which it does after additive migrations. Rolling back is not tested in this version. If the older version must also run on the older schema, restore the database backup you took before the upgrade along with the rollback; everything written to trove since that backup is lost.

## What is not verified yet

- **Mixed versions during the rollout.** For the length of a rolling update, replicas of the old and the new version serve requests side by side. The schema check is built for that, but a rolling upgrade from the previous release under load has not been tested yet. If an interruption matters to you, upgrade in a maintenance window.
- **Skipping releases.** The runner applies every pending migration in order, so an upgrade across several releases is a single migration run. An upgrade path across several published releases is not covered by a test.
- **Rollback**, as described above.

[Known limitations](limitations.md) lists the other gaps of this version.
