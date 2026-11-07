package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// This file is the database half of `trove backup create` (R-BAK-005, R-BAK-006): the backup role's connection pool, the
// consistent snapshot with the shared migration lock and the table locks, the parallel export of every table under that one
// snapshot, the sorted stream of referenced blobs and the sequences. It is the one place of the module that opens a
// REPEATABLE READ transaction for a backup and the one that assembles COPY statements from catalog names; every identifier
// is quoted with pgx.Identifier and no value is ever put into the text. The architecture test lists the file as the
// exception for both.
//
// The package cannot import internal/backup (a domain package), so the types here are plain; internal/backup adapts them to
// the Snapshotter port and maps the errors below to the exit codes of R-BAK-013.

// Errors the snapshot reports. internal/backup maps them: the standby to exit 2, the others to exit 75.
var (
	// ErrBackupStandby is a server in recovery: the hold and the pins are writes, and the snapshot must follow the hold on
	// the same timeline (R-BAK-005 step 1).
	ErrBackupStandby = errors.New("the database server is a standby (pg_is_in_recovery() is true): a backup runs against the primary")
	// ErrBackupMigrationWait is a migration (or restore) that held the migration lock for longer than
	// backup.database.migration_wait.
	ErrBackupMigrationWait = errors.New("a migration held the migration lock for longer than backup.database.migration_wait")
	// ErrBackupTableLock is a table lock that could not be taken within backup.database.lock_timeout.
	ErrBackupTableLock = errors.New("a table lock could not be taken within backup.database.lock_timeout")
	// ErrBackupCatalogChanged is a table created or dropped by hand while the snapshot was being taken, twice in a row.
	ErrBackupCatalogChanged = errors.New("the table list changed while the snapshot was being taken")
	// ErrBackupInvariant is a referenced blob row in state deleting (R-BAK-006).
	ErrBackupInvariant = errors.New("a referenced blob row is in state deleting")
)

// refuseStandby is the check every backup process makes before it opens its pool: it connects once with any target
// session attribute and refuses a server in recovery with ErrBackupStandby. A failure to connect is not its business; Open
// reports it with the retry of R-HA-015.
func refuseStandby(ctx context.Context, cfg config.Database, instance string) error {
	probe := cfg
	probe.TargetSessionAttrs = "any"
	cc, err := connectConfig(probe, sessionEndpoint(probe), instance, classBackup, false)
	if err != nil {
		return nil //nolint:nilerr // a configuration error is Open's to report
	}
	pctx, cancel := context.WithTimeout(ctx, max(cfg.ConnectTimeout, time.Second)*2)
	defer cancel()
	conn, err := pgx.ConnectConfig(pctx, cc)
	if err != nil {
		return nil //nolint:nilerr // unreachable is Open's to report, with its retry
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var recovering bool
	if err := conn.QueryRow(pctx, "SELECT pg_is_in_recovery()").Scan(&recovering); err == nil && recovering {
		return ErrBackupStandby
	}
	return nil
}

// openBackupPool opens the one pooled connection of the backup role: class backup, session host.
func (db *DB) openBackupPool(_ context.Context, common poolSpec, pool func(poolSpec, string) (*pgxpool.Pool, error)) error {
	s := common
	s.kind, s.ep, s.maxConns, s.minConns = connKind{class: classBackup}, sessionEndpoint(db.cfg), 1, 0
	p, err := pool(s, poolRequest)
	if err != nil {
		return err
	}
	db.main, db.control = p, p
	return nil
}

// BackupSnapshotParams are the parameters of OpenBackupSnapshot.
type BackupSnapshotParams struct {
	// Registry classifies the tables; nil means tables.Default().
	Registry *tables.Registry
	// ExcludeRemoteCaches leaves out of the referenced set the blobs that only remote-repository caches reference.
	ExcludeRemoteCaches bool
	// Parallel is backup.database.parallel: the export connections besides the snapshot connection.
	Parallel int
	// LockTimeout is backup.database.lock_timeout, MigrationWait backup.database.migration_wait.
	LockTimeout, MigrationWait time.Duration
	// KeyIDs reads the key IDs of the sealed values and the canary rows inside the snapshot (R-BAK-003). The sealed-column
	// registry is the caller's; this package does not import it.
	KeyIDs func(ctx context.Context, db q.DBTX) ([]string, error)
}

// SnapshotTable is one exported table of the catalog list.
type SnapshotTable struct {
	Name string
	// Class is the export class: Data or Blobs.
	Class tables.Class
	// Columns are the exported [name, type] pairs in catalog order, generated columns left out.
	Columns [][2]string
	// Registered is false for a table the registry does not know; it is exported as data.
	Registered     bool
	EstimatedBytes int64
}

// SnapshotStep is an applied migration step with the SHA-256 of its file (lowercase hex).
type SnapshotStep struct {
	Step   int
	Name   string
	SHA256 string
}

// SnapshotInfo is what the run reads once inside the snapshot.
type SnapshotInfo struct {
	At            time.Time
	ServerVersion string
	Steps         []SnapshotStep
	KeyIDs        []string
	// Tables are the tables to export, the largest first.
	Tables []SnapshotTable
}

// SnapshotSequence is last_value and is_called of one sequence.
type SnapshotSequence struct {
	Name      string
	LastValue int64
	IsCalled  bool
}

// refColumn is a column that references blobs: schema, table, column.
type refColumn struct{ schema, table, column string }

// BackupSnapshot is the open snapshot: a connection that holds the shared migration lock, the table locks and the
// REPEATABLE READ transaction, and up to Parallel export connections that imported its snapshot.
type BackupSnapshot struct {
	db   *DB
	p    BackupSnapshotParams
	reg  *tables.Registry
	conn *pgx.Conn
	tx   pgx.Tx
	id   string
	info SnapshotInfo
	refs []refColumn

	lockHeld bool

	mu     sync.Mutex
	slots  chan struct{}
	idle   []*exportConn
	opened []*exportConn
	closed bool
}

type exportConn struct {
	conn *pgx.Conn
	tx   pgx.Tx
}

// Info returns what the run read inside the snapshot.
func (s *BackupSnapshot) Info() SnapshotInfo { return s.info }

var snapshotID = regexp.MustCompile(`^[0-9A-Fa-f-]+$`)

const migrationPoll = 200 * time.Millisecond

// connectBackup opens one session connection of class backup on the session host with the session settings of R-BAK-005
// step 2: no statement timeout, no idle-in-transaction timeout, the table-lock timeout. They travel as startup parameters,
// because session-level SET is reserved for the migration runner (R-ARCH-019).
func (db *DB) connectBackup(ctx context.Context, lockTimeout time.Duration) (*pgx.Conn, error) {
	cc, err := connectConfig(db.cfg, sessionEndpoint(db.cfg), db.instance, classBackup, true)
	if err != nil {
		return nil, err
	}
	cc.RuntimeParams["idle_in_transaction_session_timeout"] = "0"
	cc.RuntimeParams["lock_timeout"] = fmt.Sprint(lockTimeout.Milliseconds())
	var conn *pgx.Conn
	err = db.retry.do(ctx, func() error {
		var cerr error
		conn, cerr = pgx.ConnectConfig(ctx, cc)
		return cerr
	})
	if err != nil {
		return nil, fmt.Errorf("cannot connect to %s: %w", sessionEndpoint(db.cfg), err)
	}
	return conn, nil
}

// OpenBackupSnapshot takes the snapshot of R-BAK-005. It holds the migration lock shared (waiting up to MigrationWait,
// then ErrBackupMigrationWait), calls onLocked, which sets the garbage-collection hold 'all' and commits it (R-BAK-007
// step 1), then locks every table of the catalog list in ACCESS SHARE mode before the first SELECT, fixes the snapshot and
// reads the schema state, the column lists and the key IDs inside it. A table list that differs before and after the locks
// aborts the attempt; the second difference is ErrBackupCatalogChanged.
func (db *DB) OpenBackupSnapshot(ctx context.Context, p BackupSnapshotParams, onLocked func(context.Context) error) (*BackupSnapshot, error) {
	if p.Registry == nil {
		p.Registry = tables.Default()
	}
	if p.Parallel < 1 {
		p.Parallel = 1
	}
	conn, err := db.connectBackup(ctx, p.LockTimeout)
	if err != nil {
		return nil, err
	}
	s := &BackupSnapshot{db: db, p: p, reg: p.Registry, conn: conn, slots: make(chan struct{}, p.Parallel)}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close(context.WithoutCancel(ctx))
		}
	}()

	var recovering bool
	if err := conn.QueryRow(ctx, "SELECT pg_is_in_recovery()").Scan(&recovering); err != nil {
		return nil, err
	}
	if recovering {
		return nil, ErrBackupStandby
	}
	if err := s.lockMigrations(ctx); err != nil {
		return nil, err
	}
	if err := onLocked(ctx); err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		err := s.snapshot(ctx)
		if err == nil {
			break
		}
		if IsLockTimeout(err) && !errors.Is(err, ErrBackupTableLock) {
			// The size estimate of the catalog read takes a table lock too.
			err = fmt.Errorf("%w: %w", ErrBackupTableLock, err)
		}
		if !errors.Is(err, ErrBackupCatalogChanged) || attempt >= 2 {
			return nil, err
		}
		db.log.Warn("the table list changed while the backup snapshot was taken; trying once more")
	}
	ok = true
	return s, nil
}

// lockMigrations takes pg_advisory_lock_shared(3, 0) by polling the try form, so the wait is bounded and can name the
// holder.
func (s *BackupSnapshot) lockMigrations(ctx context.Context) error {
	start := time.Now()
	for {
		var got bool
		if err := s.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock_shared($1::int, 0)", lockMigration).Scan(&got); err != nil {
			return err
		}
		if got {
			s.lockHeld = true
			return nil
		}
		if time.Since(start) >= s.p.MigrationWait {
			return fmt.Errorf("%w (held by %s)", ErrBackupMigrationWait, lockHolders(ctx, s.conn))
		}
		if err := sleepCtx(ctx, migrationPoll); err != nil {
			return err
		}
	}
}

// snapshot is one attempt of steps 2 to 4 of R-BAK-005's snapshot transaction.
func (s *BackupSnapshot) snapshot(ctx context.Context) error {
	schema := s.db.cfg.Schema
	catalog, err := q.New(s.conn).BackupCatalogTables(ctx, schema)
	if err != nil {
		return err
	}
	names := make([]string, len(catalog))
	for i, t := range catalog {
		names[i] = t.Name
	}
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if len(names) > 0 {
		ids := make([]string, len(names))
		for i, n := range names {
			ids[i] = pgx.Identifier{schema, n}.Sanitize()
		}
		// The locks come before the first SELECT, because the view of a REPEATABLE READ transaction is frozen when its first
		// SELECT begins (R-BAK-005); a partitioned table's partitions are locked with it.
		if _, err := tx.Exec(ctx, "LOCK TABLE "+strings.Join(ids, ", ")+" IN ACCESS SHARE MODE"); err != nil {
			switch {
			case IsLockTimeout(err):
				return fmt.Errorf("%w: %w", ErrBackupTableLock, err)
			case IsState(err, "42P01"):
				return fmt.Errorf("%w: %w", ErrBackupCatalogChanged, err)
			}
			return err
		}
	}
	qs := q.New(tx)
	exp, err := qs.BackupExportSnapshot(ctx)
	if err != nil {
		return err
	}
	if !snapshotID.MatchString(exp.SnapshotID) {
		return fmt.Errorf("unexpected snapshot identifier %q", exp.SnapshotID)
	}
	again, err := qs.BackupCatalogTables(ctx, schema)
	if err != nil {
		return err
	}
	if len(again) != len(names) {
		return ErrBackupCatalogChanged
	}
	for i := range again {
		if again[i].Name != names[i] {
			return ErrBackupCatalogChanged
		}
	}
	info := SnapshotInfo{At: exp.At.Time, ServerVersion: exp.ServerVersion}
	if info.Tables, err = s.catalogTables(ctx, qs, again); err != nil {
		return err
	}
	steps, err := qs.ListAppliedSteps(ctx)
	if err != nil {
		return err
	}
	for _, st := range steps {
		info.Steps = append(info.Steps, SnapshotStep{Step: int(st.Version), Name: st.Name, SHA256: fmt.Sprintf("%x", st.Checksum)})
	}
	if s.p.KeyIDs != nil {
		if info.KeyIDs, err = s.p.KeyIDs(ctx, tx); err != nil {
			return err
		}
	}
	if s.refs, err = s.referencingColumns(ctx, qs); err != nil {
		return err
	}
	s.tx, s.id, s.info = tx, exp.SnapshotID, info
	done = true
	return nil
}

// catalogTables joins the catalog list with the registry and the column lists, the largest table first.
func (s *BackupSnapshot) catalogTables(ctx context.Context, qs *q.Queries, cat []q.BackupCatalogTablesRow) ([]SnapshotTable, error) {
	cols, err := qs.BackupTableColumns(ctx, s.db.cfg.Schema)
	if err != nil {
		return nil, err
	}
	byTable := map[string][][2]string{}
	for _, c := range cols {
		byTable[c.TableName] = append(byTable[c.TableName], [2]string{c.ColumnName, c.ColumnType})
	}
	var out []SnapshotTable
	for _, c := range cat {
		class, registered := tables.Data, false
		if t, ok := s.reg.Lookup(c.Name); ok {
			class, registered = t.Class, true
		}
		if !class.Exported() {
			continue
		}
		out = append(out, SnapshotTable{Name: c.Name, Class: class, Columns: byTable[c.Name], Registered: registered, EstimatedBytes: c.EstimatedBytes})
	}
	slices.SortStableFunc(out, func(a, b SnapshotTable) int {
		if a.EstimatedBytes != b.EstimatedBytes {
			if a.EstimatedBytes > b.EstimatedBytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

// referencingColumns lists the columns of data tables that reference blobs: the foreign keys to blobs of the catalog, the
// rule garbage collection uses (R-STOR-024 rule 3), so a table added by a newer migration is included. Transient tables do
// not count: their rows are not in the backup.
func (s *BackupSnapshot) referencingColumns(ctx context.Context, qs *q.Queries) ([]refColumn, error) {
	rows, err := qs.BlobReferencingColumns(ctx)
	if err != nil {
		return nil, err
	}
	var out []refColumn
	for _, r := range rows {
		if t, ok := s.reg.Lookup(r.TableName); ok && t.Class != tables.Data {
			continue
		}
		out = append(out, refColumn{schema: r.SchemaName, table: r.TableName, column: r.ColumnName})
	}
	return out, nil
}

// referencedPredicate is the condition on the blobs row b that says "a row of a data table references it". With
// excludeCaches, the rows of remote-repository caches (the items of remote repositories and remote_cache_entries) do not
// count (R-BAK-008).
func (s *BackupSnapshot) referencedPredicate(excludeCaches bool) string {
	var parts []string
	for _, c := range s.refs {
		tbl := pgx.Identifier{c.schema, c.table}
		col := pgx.Identifier{c.column}.Sanitize()
		switch {
		case excludeCaches && c.table == "remote_cache_entries":
			continue
		case excludeCaches && c.table == "items":
			repos := pgx.Identifier{c.schema, "repositories"}.Sanitize()
			parts = append(parts, fmt.Sprintf("EXISTS (SELECT 1 FROM %s r WHERE r.%s = b.sha256 AND NOT EXISTS (SELECT 1 FROM %s p WHERE p.id = r.repo_id AND p.class = 'remote'))",
				tbl.Sanitize(), col, repos))
		default:
			parts = append(parts, fmt.Sprintf("EXISTS (SELECT 1 FROM %s r WHERE r.%s = b.sha256)", tbl.Sanitize(), col))
		}
	}
	if len(parts) == 0 {
		return "false"
	}
	return strings.Join(parts, " OR ")
}

// BlobStream is the sorted stream of referenced blobs read inside the snapshot.
type BlobStream struct {
	rows pgx.Rows
}

// Next returns the next digest, size and content status ("ok", "missing" or "corrupt") in ascending digest order, io.EOF after
// the last. A referenced row in state deleting is ErrBackupInvariant.
func (b *BlobStream) Next() (digest [32]byte, size int64, status string, err error) {
	if b.rows == nil {
		return digest, 0, "", io.EOF
	}
	if !b.rows.Next() {
		err = b.rows.Err()
		b.rows.Close()
		b.rows = nil
		if err == nil {
			err = io.EOF
		}
		return digest, 0, "", err
	}
	var sha []byte
	var state string
	if err := b.rows.Scan(&sha, &size, &state, &status); err != nil {
		b.rows.Close()
		b.rows = nil
		return digest, 0, "", err
	}
	if len(sha) != len(digest) {
		b.rows.Close()
		b.rows = nil
		return digest, 0, "", fmt.Errorf("a blobs row has a digest of %d bytes", len(sha))
	}
	copy(digest[:], sha)
	if state == "deleting" {
		b.rows.Close()
		b.rows = nil
		return digest, 0, "", fmt.Errorf("%w: %x", ErrBackupInvariant, sha)
	}
	return digest, size, status, nil
}

// Close releases the stream before the end.
func (b *BlobStream) Close() {
	if b.rows != nil {
		b.rows.Close()
		b.rows = nil
	}
}

// Referenced streams the digests and sizes of every blobs row referenced by at least one row of a data table, ordered by
// digest, on the snapshot connection (R-BAK-006). The stream must be read to its end or closed before the snapshot
// connection serves another call.
func (s *BackupSnapshot) Referenced(ctx context.Context) (*BlobStream, error) {
	blobs := pgx.Identifier{s.db.cfg.Schema, "blobs"}.Sanitize()
	sql := fmt.Sprintf("SELECT b.sha256, b.size, b.state, b.content_status FROM %s b WHERE %s ORDER BY b.sha256", blobs, s.referencedPredicate(s.p.ExcludeRemoteCaches))
	rows, err := s.tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return &BlobStream{rows: rows}, nil
}

// exportSQL is the COPY statement of one table: the SELECT form, which partitioned tables and the blobs filter need.
func (s *BackupSnapshot) exportSQL(t SnapshotTable) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = pgx.Identifier{c[0]}.Sanitize()
	}
	from := pgx.Identifier{s.db.cfg.Schema, t.Name}.Sanitize()
	if t.Class == tables.Blobs {
		// Only the rows of digests the snapshot references (R-BAK-004). Remote caches count here: the database is exported in
		// full and only the copy changes with the option (R-BAK-008).
		return fmt.Sprintf("COPY (SELECT %s FROM %s b WHERE %s) TO STDOUT", strings.Join(cols, ", "), from, s.referencedPredicate(false))
	}
	return fmt.Sprintf("COPY (SELECT %s FROM %s) TO STDOUT", strings.Join(cols, ", "), from)
}

// acquire hands out an export connection that has imported the snapshot, opening it on first use.
func (s *BackupSnapshot) acquire(ctx context.Context) (*exportConn, error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.slots
		return nil, errors.New("the backup snapshot is closed")
	}
	if n := len(s.idle); n > 0 {
		c := s.idle[n-1]
		s.idle = s.idle[:n-1]
		s.mu.Unlock()
		return c, nil
	}
	s.mu.Unlock()
	c, err := s.openExport(ctx)
	if err != nil {
		<-s.slots
		return nil, err
	}
	return c, nil
}

func (s *BackupSnapshot) release(c *exportConn, broken bool) {
	if broken {
		s.mu.Lock()
		s.opened = slices.DeleteFunc(s.opened, func(x *exportConn) bool { return x == c })
		s.mu.Unlock()
		_ = c.conn.Close(context.Background())
	} else {
		s.mu.Lock()
		s.idle = append(s.idle, c)
		s.mu.Unlock()
	}
	<-s.slots
}

// openExport opens an export connection: BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY, then SET TRANSACTION SNAPSHOT as
// its first statement (R-BAK-005 step 3).
func (s *BackupSnapshot) openExport(ctx context.Context) (*exportConn, error) {
	conn, err := s.db.connectBackup(ctx, s.p.LockTimeout)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err == nil {
		_, err = tx.Exec(ctx, "SET TRANSACTION SNAPSHOT '"+s.id+"'", pgx.QueryExecModeSimpleProtocol)
	}
	if err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("import the backup snapshot: %w", err)
	}
	c := &exportConn{conn: conn, tx: tx}
	s.mu.Lock()
	s.opened = append(s.opened, c)
	s.mu.Unlock()
	return c, nil
}

// ExportTable streams COPY (SELECT ...) TO STDOUT of one table, text format, to w on one of the export connections and
// returns the row count (R-BAK-005 step 4). A table lock wait that times out is ErrBackupTableLock.
func (s *BackupSnapshot) ExportTable(ctx context.Context, t SnapshotTable, w io.Writer) (int64, error) {
	c, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := c.conn.PgConn().CopyTo(ctx, w, s.exportSQL(t))
	s.release(c, err != nil)
	if err != nil {
		if IsLockTimeout(err) {
			return 0, fmt.Errorf("%w: %w", ErrBackupTableLock, err)
		}
		return 0, fmt.Errorf("export %s: %w", t.Name, err)
	}
	return tag.RowsAffected(), nil
}

// Sequences reads last_value and is_called of every sequence of the schema (R-BAK-005 step 6). Sequences are not
// transactional, so the values are at least as high as every value used by a row of the snapshot.
func (s *BackupSnapshot) Sequences(ctx context.Context) ([]SnapshotSequence, error) {
	rows, err := q.New(s.tx).BackupSequences(ctx, s.db.cfg.Schema)
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotSequence, len(rows))
	for i, r := range rows {
		out[i] = SnapshotSequence{Name: r.Name, LastValue: r.StartValue.Int64, IsCalled: r.LastValue.Valid}
		if r.LastValue.Valid {
			out[i].LastValue = r.LastValue.Int64
		}
	}
	return out, nil
}

// ReleaseMigrationLock releases the shared migration lock; the snapshot stays open until Close.
func (s *BackupSnapshot) ReleaseMigrationLock(ctx context.Context) error {
	if !s.lockHeld {
		return nil
	}
	var released bool
	if err := s.conn.QueryRow(ctx, "SELECT pg_advisory_unlock_shared($1::int, 0)", lockMigration).Scan(&released); err != nil {
		return err
	}
	s.lockHeld = false
	return nil
}

// Close ends the snapshot: the export transactions and connections, then the snapshot transaction, the lock and its
// connection. It is safe to call more than once.
func (s *BackupSnapshot) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := s.opened
	s.opened, s.idle = nil, nil
	s.mu.Unlock()
	var errs []error
	for _, c := range conns {
		_ = c.tx.Rollback(ctx)
		if err := c.conn.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if s.tx != nil {
		_ = s.tx.Rollback(ctx)
	}
	if s.conn != nil {
		if s.lockHeld {
			_, _ = s.conn.Exec(ctx, "SELECT pg_advisory_unlock_shared($1::int, 0)", lockMigration)
			s.lockHeld = false
		}
		if err := s.conn.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
