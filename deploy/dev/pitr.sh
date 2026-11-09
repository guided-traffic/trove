#!/usr/bin/env bash
# The PostgreSQL side of the point-in-time restore drill (R-HA-023, R-HA-025 step 2, docs/developer/e2e.md#the-point-in-time-restore-drill)
# on a cluster that TROVE_PITR=1 brought up (deploy/dev/up.sh):
#
#   pitr.sh status                    archive_mode, the last archived WAL and the base backups of trove-db
#   pitr.sh backup                    a base backup of trove-db through the Barman Cloud plugin; waits until it is completed and prints its name
#   pitr.sh archive-wal               switches the WAL segment on the primary and waits until the archiver has shipped it (and every segment before)
#   pitr.sh restore <time>            recovers the archive into the new cluster trove-db-pitr up to <time> (a PostgreSQL timestamp, for
#                                     example "2026-11-09 12:00:00.123456+00") and waits until it is ready
#   pitr.sh down                      deletes trove-db-pitr and its volumes
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
require_cluster

primary() { kn get cluster trove-db -o jsonpath='{.status.currentPrimary}'; }
psql_primary() { kn exec "$(primary)" -c postgres -- psql -U postgres -d trove -At -v ON_ERROR_STOP=1 "$@"; }
backup_phase() { kn get backups.postgresql.cnpg.io "$1" -o jsonpath='{.status.phase}'; }

case "${1:-}" in
  status)
    psql_primary -c "SHOW archive_mode" -c "SELECT last_archived_wal, failed_count FROM pg_stat_archiver"
    kn get backups.postgresql.cnpg.io -o wide
    ;;
  backup)
    name="trove-db-base-$(date +%Y%m%d%H%M%S)"
    kn apply -f - >/dev/null <<YAML
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: $name
  labels:
    app.kubernetes.io/part-of: trove-dev
spec:
  method: plugin
  cluster:
    name: trove-db
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
YAML
    deadline=$(( $(date +%s) + 600 ))
    until [ "$(backup_phase "$name")" = "completed" ]; do
      phase="$(backup_phase "$name")"
      [ "$phase" != "failed" ] || { kn get backups.postgresql.cnpg.io "$name" -o jsonpath='{.status.error}' >&2; die "the base backup $name failed"; }
      [ "$(date +%s)" -lt "$deadline" ] || die "the base backup $name is still '$phase' after 600s"
      sleep 3
    done
    printf '%s\n' "$name"
    ;;
  archive-wal)
    # pg_switch_wal returns the end of the closed segment; the archiver has caught up once its last archived segment is that one or a later
    # one (the file names sort in WAL order within a timeline).
    seg="$(psql_primary -c "SELECT pg_walfile_name(pg_switch_wal() - 1)" | tail -1)"
    deadline=$(( $(date +%s) + 180 ))
    until [ "$(psql_primary -c "SELECT coalesce(last_archived_wal, '') >= '$seg' FROM pg_stat_archiver")" = "t" ]; do
      [ "$(date +%s)" -lt "$deadline" ] || { psql_primary -c "SELECT * FROM pg_stat_archiver" >&2; die "segment $seg was not archived after 180s"; }
      sleep 2
    done
    printf '%s\n' "$seg"
    ;;
  restore)
    at="${2:?usage: pitr.sh restore <time>}"
    render "$DEV_DIR/pitr-restore.yaml" | sed -e "s|\${PITR_TARGET_TIME}|$at|g" | kn apply -f -
    until kn get cluster trove-db-pitr >/dev/null 2>&1; do sleep 1; done
    kn wait --for=condition=Ready cluster/trove-db-pitr --timeout=15m
    ;;
  down)
    kn delete cluster trove-db-pitr --ignore-not-found --wait=true
    ;;
  *)
    die "usage: pitr.sh status | backup | archive-wal | restore <time> | down"
    ;;
esac
