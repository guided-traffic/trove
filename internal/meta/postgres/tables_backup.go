package postgres

import "github.com/guided-traffic/trove/internal/backup/tables"

// Registration of the tables of 00046_backup (R-BAK-004, R-BAK-009):
//
//   - backup_run, backup_blob_pin: transient. The target, not the database, is the record of backups, and a restored
//     database has no run and no pin; the hold and the pins describe a copy that ended with the old installation.
func init() {
	tables.Register("backup_run", tables.Transient, tables.Options{})
	tables.Register("backup_blob_pin", tables.Transient, tables.Options{})
}
