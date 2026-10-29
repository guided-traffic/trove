package secrets

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Column identifies a column that holds sealed values: the table, its primary
// key columns and the column itself. The zero value is invalid; a Column comes
// from Register only, so every sealed column is in the registry.
type Column struct {
	table, pk, column string // pk: the primary key columns joined by ","
	canary            bool
}

// Table returns the table name.
func (c Column) Table() string { return c.table }

// PrimaryKey returns the primary key columns as registered, joined by ",".
func (c Column) PrimaryKey() string { return c.pk }

// PrimaryKeyColumns returns the primary key columns in the order a RowKey
// lists their values.
func (c Column) PrimaryKeyColumns() []string { return strings.Split(c.pk, ",") }

// Name returns the column name.
func (c Column) Name() string { return c.column }

// String renders table.column.
func (c Column) String() string { return c.table + "." + c.column }

func (c Column) valid() bool {
	if !identifier().MatchString(c.table) || !identifier().MatchString(c.column) {
		return false
	}
	for _, p := range c.PrimaryKeyColumns() {
		if !identifier().MatchString(p) {
			return false
		}
	}
	return true
}

func identifier() *regexp.Regexp { return regexp.MustCompile(`^[a-z_][a-z0-9_]*$`) }

// registry is filled once at start-up and frozen (R-ARCH-032): packages
// register their sealed columns from init or from their constructor before
// Freeze, which the composition root calls before it serves.
var reg = struct {
	mu     sync.Mutex
	cols   []Column
	frozen bool
}{}

// Register adds a sealed column to the registry and returns its handle.
// primaryKey names the primary key column, or for a composite key its columns
// in order, separated by "," ("repository_id,name"); Seal and Open then take a
// RowKey with one value per column in that order. Register panics on a name
// that is not a lower-case SQL identifier, on a duplicate and after Freeze,
// because each is a programming error that start-up must surface.
func Register(table, primaryKey, column string) Column {
	c := Column{table: table, pk: primaryKey, column: column}
	if !c.valid() {
		panic(fmt.Sprintf("secrets: Register(%q, %q, %q) needs lower-case identifiers for table, primary key columns and column", table, primaryKey, column))
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.frozen {
		panic(fmt.Sprintf("secrets: Register(%s) after Freeze: sealed columns are registered at start-up", c))
	}
	for _, x := range reg.cols {
		if x == c {
			panic(fmt.Sprintf("secrets: column %s registered twice", c))
		}
	}
	reg.cols = append(reg.cols, c)
	return c
}

// Freeze ends registration. Seal and Open work before it too; Freeze only stops
// late registrations.
func Freeze() {
	reg.mu.Lock()
	reg.frozen = true
	reg.mu.Unlock()
}

// Columns returns the registered sealed columns, sorted by table and column.
func Columns() []Column {
	reg.mu.Lock()
	out := append([]Column(nil), reg.cols...)
	reg.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func registered(c Column) bool {
	if c.canary {
		return c == canaryColumn()
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for _, x := range reg.cols {
		if x == c {
			return true
		}
	}
	return false
}
