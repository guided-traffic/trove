package main

// The packages that own tables register them with internal/backup/tables in
// their init functions (R-BAK-004). `trove migrate` checks the migrated schema
// against that registry, so the command links every owner whether or not its
// own code path reaches them: a table nobody registered would be missing from
// every backup.
import (
	_ "github.com/guided-traffic/trove/internal/blob"
	_ "github.com/guided-traffic/trove/internal/cluster"
	_ "github.com/guided-traffic/trove/internal/jobs"
)
