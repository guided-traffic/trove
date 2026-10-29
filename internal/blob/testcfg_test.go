package blob

import (
	"io/fs"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
)

// testStorage is the default storage configuration with the spool at dir.
func testStorage(tb testing.TB, dir string) config.Storage {
	tb.Helper()
	l, err := config.Load(config.Options{
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Environ: []string{
			"TROVE_DATABASE_HOST=db.invalid", "TROVE_DATABASE_NAME=trove",
			"TROVE_STORAGE_S3_BUCKET=blobs", "TROVE_STORAGE_SPOOL_DIR=" + dir,
		},
		Command: config.CommandMigrate,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return l.Config.Storage
}
