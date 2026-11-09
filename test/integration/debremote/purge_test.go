//go:build integration

package debremote

import (
	"context"
	"testing"
	"time"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A restore without remote caches deletes the cache rows of every Debian remote: the releases, the files they list, the parsed indexes
// and their entries (R-BAK-008, R-DEB-023). The purger empties the cached items first, through the storage delete path, and the rows
// after.
func TestR_BAK_008_ARestoreWithoutRemoteCachesDeletesTheRowsOfADebianRemote(t *testing.T) {
	e := newEnv(t)
	e.fetchIndexes(rKeyed)
	eventually(t, 10*time.Second, "the rows", func() bool { return e.rows("deb_remote_entries", rKeyed) > 0 })
	for _, table := range []string{"deb_remote_releases", "deb_remote_files", "deb_remote_indexes", "deb_remote_entries"} {
		if e.rows(table, rKeyed) == 0 {
			t.Fatalf("%s holds no row before the purge", table)
		}
	}
	db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "debremote-purge", meta.RoleWeb, testenv.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var erased []string
	err = debdata.PurgeRemoteCaches(context.Background(), db, func(_ context.Context, _ int64, name qname.Repo, format string) error {
		if format != "deb" {
			t.Errorf("the purger erases a repository of the format %s", format)
		}
		erased = append(erased, name.Key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(erased) == 0 {
		t.Fatal("no cache was erased")
	}
	for _, table := range []string{"deb_remote_releases", "deb_remote_files", "deb_remote_indexes", "deb_remote_entries"} {
		if n := e.st.Number(`SELECT count(*) FROM `+table, nil...); n != 0 {
			t.Errorf("%s holds %d rows after the purge", table, n)
		}
	}
	// A second run finds nothing to do.
	if err := debdata.PurgeRemoteCaches(context.Background(), db, func(context.Context, int64, qname.Repo, string) error { return nil }); err != nil {
		t.Errorf("second run: %v", err)
	}
}
