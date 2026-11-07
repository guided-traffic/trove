//go:build integration

package backup

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// mustEncode is the JSON of a manifest.
func mustEncode(t testing.TB, m *format.Manifest) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// validateManifest checks a manifest against the JSON Schema of R-BAK-003.
func validateManifest(t testing.TB, raw []byte) {
	t.Helper()
	c := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(format.ManifestSchema))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("manifest.schema.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(inst); err != nil {
		t.Fatalf("the manifest does not match its schema: %v", err)
	}
}

// dial opens a superuser session on the installation's database.
func dial(t testing.TB, in *install) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), testenv.PgDSN(t, in.dbName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// migrateOnce runs `trove migrate up` in the library: the runner waits up to wait for the migration lock.
func migrateOnce(in *install, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := postgres.Migrate(ctx, in.dbCfg, postgres.MigrateOptions{
		Instance: "it-bak-upgrade", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: wait, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	})
	return err
}

// listManifests lists the manifests of the rig's target.
func listManifests(t testing.TB, r *rig) []string {
	t.Helper()
	var out []string
	err := r.tgt.List(context.Background(), format.Layout{Prefix: r.cfg.Target.Prefix}.BackupsPrefix(), func(o target.Object) error {
		if strings.HasSuffix(o.Key, "/"+format.ManifestName) {
			out = append(out, o.Key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var _ = fs.ErrNotExist

// dialDB opens a superuser session on a database of the stack.
func dialDB(t testing.TB, database string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), testenv.PgDSN(t, database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// dialAs opens a session of a role (password pw) on the installation's database.
func dialAs(t testing.TB, in *install, role string) *pgx.Conn {
	t.Helper()
	cc, err := pgx.ParseConfig(testenv.PgDSN(t, in.dbName))
	if err != nil {
		t.Fatal(err)
	}
	cc.User, cc.Password = role, "pw"
	c, err := pgx.ConnectConfig(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}
