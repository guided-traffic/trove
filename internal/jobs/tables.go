package jobs

import "github.com/guided-traffic/trove/internal/backup/tables"

// The job table has the backup export class data (R-BAK-004): a restore returns
// its running rows to queued without counting the attempt, as a graceful release
// does (R-BAK-022).
func init() {
	tables.Register("job", tables.Data, tables.Options{})
}
