package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// analyzeDB is a Database that records the statements of Read and can fail them.
type analyzeDB struct {
	statements []string
	fail       error
	reads      atomic.Int32
}

func (d *analyzeDB) InTx(context.Context, func(meta.Tx) error) error {
	return errors.New("no transaction expected")
}

func (d *analyzeDB) Read(_ context.Context, fn func(q.DBTX) error) error {
	d.reads.Add(1)
	return fn(d)
}

func (d *analyzeDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	d.statements = append(d.statements, sql)
	return pgconn.CommandTag{}, d.fail
}

func (d *analyzeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("no query expected")
}

func (d *analyzeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func analyzeService(db *analyzeDB) *Service {
	return &Service{
		o:   Options{DB: db, Now: time.Now},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Q-VULN-96: a run that wrote a large import refreshes the statistics of the advisory tables, a run that wrote a change file does
// not, and a failing ANALYZE does not fail the run.
func TestR_VULN_028_AnImportRefreshesThePlannerStatistics(t *testing.T) {
	t.Run("a first import of an ecosystem analyzes the tables it filled", func(t *testing.T) {
		db := &analyzeDB{}
		analyzeService(db).analyzeAfterImport(t.Context(), "npm", 231012)
		if len(db.statements) != 1 || !strings.Contains(db.statements[0], "ANALYZE vuln_advisory") || !strings.Contains(db.statements[0], "vuln_affected") {
			t.Errorf("statements %q", db.statements)
		}
	})
	t.Run("the few records of a change file do not", func(t *testing.T) {
		db := &analyzeDB{}
		analyzeService(db).analyzeAfterImport(t.Context(), "npm", analyzeMinWritten-1)
		if db.reads.Load() != 0 {
			t.Errorf("%d reads for %d records", db.reads.Load(), analyzeMinWritten-1)
		}
	})
	t.Run("a failure is logged and not returned: the statements of the sync do not depend on statistics", func(t *testing.T) {
		db := &analyzeDB{fail: errors.New("permission denied")}
		analyzeService(db).analyzeAfterImport(t.Context(), "npm", analyzeMinWritten)
		if len(db.statements) != 1 {
			t.Errorf("statements %q", db.statements)
		}
	})
}
