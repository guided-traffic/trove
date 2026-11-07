package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// The load transaction of `trove backup restore` (R-BAK-021 step 4, R-BAK-004). One transaction on the restore's session
// connection: the user triggers off, one TRUNCATE, `COPY FROM STDIN` per table in foreign-key order with the CycleBreak
// column filled afterwards, the sequences, the damaged blobs, the row counts against the manifest, the triggers on, the
// marker with phase "loaded", COMMIT. The statements are assembled from names of the catalog and the manifest, every
// identifier quoted with pgx.Identifier; no value is put into a statement text.

// bookkeeping are the tables the migrations own and a load leaves alone (export class schema, R-BAK-004).
func isBookkeeping(name string) bool { return name == "schema_migration" || name == "schema_state" }

// auditTable is the one partitioned table; its rows are staged so that the monthly partitions they need can be created from
// the range they span (R-OPS-015) before they are inserted.
const auditTable = "audit_event"

// stageTable is the temporary table a staged load copies into; a table with a CycleBreak column keeps its own until every
// table is loaded.
func stageTable(table string) string { return pgx.Identifier{"_restore_stage_" + table}.Sanitize() }

// LoadTable is one table of the backup with what the manifest records.
type LoadTable struct {
	Name string
	// Columns are the [name, type] pairs of the file, in file order.
	Columns [][2]string
	// Rows is the manifest's row count.
	Rows int64
	// CycleBreak is the registered column loaded as NULL and filled afterwards, empty for most tables.
	CycleBreak string
}

// LoadSequence is a sequence of the manifest.
type LoadSequence struct {
	Name      string
	LastValue int64
	IsCalled  bool
}

// LoadPlan is what the load needs of the backup.
type LoadPlan struct {
	Tables    []LoadTable
	Sequences []LoadSequence
	// Damaged maps the hex digest of a blob that was damaged in the source to the content_status it is restored with.
	Damaged map[string]string
	// Marker is committed again, with phase "loaded", because the TRUNCATE removed the row the first migration committed.
	Marker RestoreMarker
	// Registry classifies the tables of the schema that the backup does not carry: a transient or schema table is expected
	// to be absent, any other is a mismatch. nil means tables.Default().
	Registry *tables.Registry
	// LockTimeout bounds the wait for the table locks of the TRUNCATE (backup.database.lock_timeout).
	LockTimeout time.Duration
	// OnTable is called before each table is copied, for the progress log and the tests.
	OnTable func(name string)
}

// LoadSource opens the COPY text of a table: the decompressed bytes of its file. The reader's Close reports whether what it
// delivered matches the manifest's size and SHA-256 of the file; it is called after the table was copied, and an error
// aborts the load.
type LoadSource interface {
	Open(ctx context.Context, table string) (io.ReadCloser, error)
}

// LoadResult reports the load.
type LoadResult struct {
	// Rows is the loaded row count per table, equal to the manifest's.
	Rows map[string]int64
	// Order is the order the tables were loaded in.
	Order []string
}

// RestoreSchemaError is a schema that differs from the backup: a table, a column list or a sequence the manifest names does
// not match what the migrations built. It is exit 6 (R-BAK-021 step 4).
type RestoreSchemaError struct{ Problems []string }

func (e *RestoreSchemaError) Error() string {
	return "the schema does not match the backup: " + strings.Join(e.Problems, "; ")
}

// RowCountError is a table whose loaded rows differ from the manifest's count: the file does not hold what the backup wrote.
type RowCountError struct {
	Table      string
	Loaded     int64
	Manifested int64
}

func (e *RowCountError) Error() string {
	return fmt.Sprintf("table %s: %d rows loaded, the manifest says %d", e.Table, e.Loaded, e.Manifested)
}

// Load runs the load transaction. On any error the transaction rolls back and the database is as before.
func (s *RestoreSession) Load(ctx context.Context, plan LoadPlan, src LoadSource) (LoadResult, error) {
	res := LoadResult{Rows: map[string]int64{}}
	reg := plan.Registry
	if reg == nil {
		reg = tables.Default()
	}
	cat, err := s.loadCatalog(ctx)
	if err != nil {
		return res, err
	}
	if problems := checkLoad(plan, reg, cat); len(problems) > 0 {
		return res, &RestoreSchemaError{Problems: problems}
	}
	order, err := loadOrder(plan.Tables, cat.fks)
	if err != nil {
		return res, err
	}
	res.Order = order
	byName := map[string]LoadTable{}
	for _, t := range plan.Tables {
		byName[t.Name] = t
	}
	var truncate []string
	for _, t := range cat.tables {
		if !isBookkeeping(t) {
			truncate = append(truncate, t)
		}
	}

	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, set := range []string{
		"SET LOCAL statement_timeout = 0",
		"SET LOCAL idle_in_transaction_session_timeout = 0",
		fmt.Sprintf("SET LOCAL lock_timeout = %d", plan.LockTimeout.Milliseconds()),
	} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return res, err
		}
	}
	if err := s.setUserTriggers(ctx, tx, truncate, false); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, "TRUNCATE "+identList(s.cfg.Schema, truncate)); err != nil {
		return res, fmt.Errorf("truncate: %w", err)
	}
	var deferred []LoadTable
	for _, name := range order {
		t := byName[name]
		if plan.OnTable != nil {
			plan.OnTable(name)
		}
		started := time.Now()
		n, err := s.loadTable(ctx, tx, t, src)
		if err != nil {
			return res, fmt.Errorf("table %s: %w", name, err)
		}
		if n != t.Rows {
			return res, &RowCountError{Table: name, Loaded: n, Manifested: t.Rows}
		}
		res.Rows[name] = n
		if t.CycleBreak != "" {
			deferred = append(deferred, t)
		}
		s.log.Info("table loaded", "table", name, "rows", n, "duration_ms", time.Since(started).Milliseconds())
	}
	// The CycleBreak columns are filled once every table of the cycle exists.
	for _, t := range deferred {
		if err := s.fillCycleBreak(ctx, tx, t, pgx.Identifier{s.cfg.Schema, t.Name}.Sanitize()); err != nil {
			return res, fmt.Errorf("table %s: %w", t.Name, err)
		}
	}
	qs := q.New(tx)
	for _, sq := range plan.Sequences {
		if _, err := qs.RestoreSetSequence(ctx, q.RestoreSetSequenceParams{
			Qualified: pgx.Identifier{s.cfg.Schema, sq.Name}.Sanitize(), LastValue: sq.LastValue, IsCalled: sq.IsCalled,
		}); err != nil {
			return res, fmt.Errorf("sequence %s: %w", sq.Name, err)
		}
	}
	if err := markDamaged(ctx, qs, plan.Damaged); err != nil {
		return res, err
	}
	if err := s.setUserTriggers(ctx, tx, truncate, true); err != nil {
		return res, err
	}
	if err := insertMarker(ctx, tx, RestoreMarker{BackupID: plan.Marker.BackupID, Phase: PhaseLoaded, StartedAt: plan.Marker.StartedAt}); err != nil {
		return res, err
	}
	if err := qs.RestoreEnsureAuditPartitions(ctx); err != nil {
		return res, fmt.Errorf("audit partitions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// markDamaged gives the blobs the backup recorded as damaged their status, one UPDATE per status.
func markDamaged(ctx context.Context, qs *q.Queries, damaged map[string]string) error {
	by := map[string][][]byte{}
	for hexDigest, status := range damaged {
		d, err := parseHexDigest(hexDigest)
		if err != nil {
			return err
		}
		by[status] = append(by[status], d)
	}
	statuses := make([]string, 0, len(by))
	for st := range by {
		statuses = append(statuses, st)
	}
	sort.Strings(statuses)
	for _, st := range statuses {
		n, err := qs.RestoreMarkDamagedBlobs(ctx, q.RestoreMarkDamagedBlobsParams{Status: st, Digests: by[st]})
		if err != nil {
			return fmt.Errorf("damaged blobs: %w", err)
		}
		if int(n) != len(by[st]) {
			return fmt.Errorf("damaged blobs: %d of %d digests of the manifest have no blob row", len(by[st])-int(n), len(by[st]))
		}
	}
	return nil
}

func parseHexDigest(s string) ([]byte, error) {
	if len(s) != 64 {
		return nil, fmt.Errorf("digest %q is not 64 hex digits", s)
	}
	b := make([]byte, 32)
	for i := range b {
		hi, lo := unhex(s[2*i]), unhex(s[2*i+1])
		if hi > 15 || lo > 15 {
			return nil, fmt.Errorf("digest %q is not 64 hex digits", s)
		}
		b[i] = hi<<4 | lo
	}
	return b, nil
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return 255
}

// setUserTriggers disables or enables the user triggers of every table: the guard, release, usage-delta and audit triggers
// must not fire again for effects the data already contains (R-BAK-021 step 4). The constraint triggers of the foreign
// keys are system triggers and stay on.
func (s *RestoreSession) setUserTriggers(ctx context.Context, tx pgx.Tx, names []string, enable bool) error {
	verb := "DISABLE"
	if enable {
		verb = "ENABLE"
	}
	for _, n := range names {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{s.cfg.Schema, n}.Sanitize()+" "+verb+" TRIGGER USER"); err != nil {
			return fmt.Errorf("%s triggers of %s: %w", strings.ToLower(verb), n, err)
		}
	}
	return nil
}

func identList(schema string, names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = pgx.Identifier{schema, n}.Sanitize()
	}
	return strings.Join(parts, ", ")
}

func columnList(cols [][2]string, except string) string {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		if c[0] != except {
			parts = append(parts, pgx.Identifier{c[0]}.Sanitize())
		}
	}
	return strings.Join(parts, ", ")
}

// loadTable copies one table and returns the rows it holds. A plain table is copied directly. The audit table and a table
// with a CycleBreak column go through a temporary table: the audit rows to learn which monthly partitions they need, the
// others to set the column once every row of the cycle's other table exists.
func (s *RestoreSession) loadTable(ctx context.Context, tx pgx.Tx, t LoadTable, src LoadSource) (int64, error) {
	rc, err := src.Open(ctx, t.Name)
	if err != nil {
		return 0, err
	}
	qualified := pgx.Identifier{s.cfg.Schema, t.Name}.Sanitize()
	cols := columnList(t.Columns, "")
	staged := t.Name == auditTable || t.CycleBreak != ""
	target := qualified
	stage := stageTable(t.Name)
	if staged {
		if _, err := tx.Exec(ctx, "CREATE TEMP TABLE "+stage+" ON COMMIT DROP AS SELECT "+cols+" FROM "+qualified+" WITH NO DATA"); err != nil {
			_ = rc.Close()
			return 0, err
		}
		target = stage
	}
	tag, err := tx.Conn().PgConn().CopyFrom(ctx, rc, "COPY "+target+" ("+cols+") FROM STDIN")
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	rows := tag.RowsAffected()
	if !staged {
		return rows, nil
	}
	if t.Name == auditTable && rows > 0 {
		if _, err := tx.Exec(ctx, "SELECT audit_create_partitions((SELECT min(occurred_at) FROM "+stage+"), (SELECT max(occurred_at) FROM "+stage+"))"); err != nil {
			return 0, fmt.Errorf("audit partitions: %w", err)
		}
	}
	insertCols := columnList(t.Columns, t.CycleBreak)
	insert, err := tx.Exec(ctx, "INSERT INTO "+qualified+" ("+insertCols+") OVERRIDING SYSTEM VALUE SELECT "+insertCols+" FROM "+stage)
	if err != nil {
		return 0, err
	}
	if insert.RowsAffected() != rows {
		return 0, fmt.Errorf("%d rows staged, %d inserted", rows, insert.RowsAffected())
	}
	if t.CycleBreak == "" {
		if _, err := tx.Exec(ctx, "DROP TABLE "+stage); err != nil {
			return 0, err
		}
	}
	return rows, nil
}

// fillCycleBreak sets the CycleBreak column of every row of t from the staged copy, matched on the primary key.
func (s *RestoreSession) fillCycleBreak(ctx context.Context, tx pgx.Tx, t LoadTable, qualified string) error {
	pk, err := q.New(tx).RestoreTablePrimaryKey(ctx, qualified)
	if err != nil {
		return err
	}
	if len(pk) == 0 {
		return fmt.Errorf("table %s has no primary key to match the %s column on", t.Name, t.CycleBreak)
	}
	col := pgx.Identifier{t.CycleBreak}.Sanitize()
	join := make([]string, len(pk))
	for i, c := range pk {
		id := pgx.Identifier{c}.Sanitize()
		join[i] = "t." + id + " = s." + id
	}
	_, err = tx.Exec(ctx, "UPDATE "+qualified+" AS t SET "+col+" = s."+col+" FROM "+stageTable(t.Name)+" AS s WHERE "+strings.Join(join, " AND ")+" AND s."+col+" IS NOT NULL")
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "DROP TABLE "+stageTable(t.Name))
	return err
}

// loadCatalog is what the load compares the manifest with, read before the transaction starts.
type loadCatalog struct {
	tables    []string
	columns   map[string][][2]string
	fks       []tables.ForeignKey
	sequences map[string]bool
}

func (s *RestoreSession) loadCatalog(ctx context.Context) (loadCatalog, error) {
	qs := q.New(s.conn)
	var c loadCatalog
	var err error
	if c.tables, err = qs.SchemaTables(ctx, s.cfg.Schema); err != nil {
		return c, err
	}
	cols, err := qs.RestoreTableColumns(ctx, s.cfg.Schema)
	if err != nil {
		return c, err
	}
	c.columns = map[string][][2]string{}
	for _, r := range cols {
		c.columns[r.TableName] = append(c.columns[r.TableName], [2]string{r.ColumnName, r.ColumnType})
	}
	fks, err := qs.SchemaForeignKeys(ctx, s.cfg.Schema)
	if err != nil {
		return c, err
	}
	for _, r := range fks {
		c.fks = append(c.fks, tables.ForeignKey{Table: r.TableName, Column: r.ColumnName, RefTable: r.RefTable})
	}
	seqs, err := qs.RestoreSequences(ctx, s.cfg.Schema)
	if err != nil {
		return c, err
	}
	c.sequences = map[string]bool{}
	for _, n := range seqs {
		c.sequences[n] = true
	}
	return c, nil
}

// checkLoad compares the manifest with the schema the migrations built: every table of the manifest exists with the
// manifest's column list (names and types, in order), every other table of the schema is one the registry expects to be
// absent, and every sequence exists (R-BAK-021 step 4).
func checkLoad(plan LoadPlan, reg *tables.Registry, c loadCatalog) []string {
	var problems []string
	inManifest := map[string]bool{}
	for _, t := range plan.Tables {
		inManifest[t.Name] = true
		have, ok := c.columns[t.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("table %s of the backup is not in the schema", t.Name))
			continue
		}
		if !sameColumns(t.Columns, have) {
			problems = append(problems, fmt.Sprintf("table %s has the columns %s, the backup has %s", t.Name, columnSummary(have), columnSummary(t.Columns)))
		}
	}
	for _, name := range c.tables {
		if inManifest[name] || isBookkeeping(name) {
			continue
		}
		if cl, ok := reg.Lookup(name); ok && !cl.Class.Exported() {
			continue
		}
		problems = append(problems, fmt.Sprintf("table %s of the schema is not in the backup", name))
	}
	for _, sq := range plan.Sequences {
		if !c.sequences[sq.Name] {
			problems = append(problems, fmt.Sprintf("sequence %s of the backup is not in the schema", sq.Name))
		}
	}
	sort.Strings(problems)
	return problems
}

func sameColumns(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func columnSummary(cols [][2]string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c[0] + " " + c[1]
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// loadOrder returns the tables in foreign-key order, parents first, with the edges of the CycleBreak columns cut. Ties go by
// name, so the order is the same on every run. A cycle that remains is an error: the migration test of R-BAK-004 fails a
// schema that has one.
func loadOrder(ts []LoadTable, fks []tables.ForeignKey) ([]string, error) {
	cut := map[string]string{}
	nodes := map[string]bool{}
	for _, t := range ts {
		nodes[t.Name] = true
		if t.CycleBreak != "" {
			cut[t.Name] = t.CycleBreak
		}
	}
	parents := map[string]map[string]bool{}
	children := map[string][]string{}
	for _, fk := range fks {
		if !nodes[fk.Table] || !nodes[fk.RefTable] || fk.Table == fk.RefTable || cut[fk.Table] == fk.Column {
			continue
		}
		if parents[fk.Table] == nil {
			parents[fk.Table] = map[string]bool{}
		}
		if !parents[fk.Table][fk.RefTable] {
			parents[fk.Table][fk.RefTable] = true
			children[fk.RefTable] = append(children[fk.RefTable], fk.Table)
		}
	}
	waiting := map[string]int{}
	var ready []string
	for n := range nodes {
		waiting[n] = len(parents[n])
		if waiting[n] == 0 {
			ready = append(ready, n)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		var released []string
		for _, c := range children[n] {
			if waiting[c]--; waiting[c] == 0 {
				released = append(released, c)
			}
		}
		ready = append(ready, released...)
		sort.Strings(ready)
	}
	if len(order) != len(nodes) {
		var left []string
		for n, w := range waiting {
			if w > 0 {
				left = append(left, n)
			}
		}
		sort.Strings(left)
		return nil, errors.New("foreign-key cycle without a registered CycleBreak column among: " + strings.Join(left, ", "))
	}
	return order, nil
}
