package secrets

import (
	"fmt"
	"sort"
	"sync"
)

// Column identifies a column that holds sealed values: the table, its primary
// key column and the column itself. The zero value is invalid; a Column comes
// from Register only, so every sealed column is in the registry.
type Column struct {
	table, pk, column string
	canary            bool
}

// Table returns the table name.
func (c Column) Table() string { return c.table }

// PrimaryKey returns the name of the primary key column.
func (c Column) PrimaryKey() string { return c.pk }

// Name returns the column name.
func (c Column) Name() string { return c.column }

// String renders table.column.
func (c Column) String() string { return c.table + "." + c.column }

func (c Column) valid() bool { return c.table != "" && c.pk != "" && c.column != "" }

// registry is filled once at start-up and frozen (R-ARCH-032): packages
// register their sealed columns from init or from their constructor before
// Freeze, which the composition root calls before it serves.
var reg = struct {
	mu     sync.Mutex
	cols   []Column
	frozen bool
}{}

// Register adds a sealed column to the registry and returns its handle. It
// panics on an empty name, on a duplicate and after Freeze, because each is a
// programming error that start-up must surface.
func Register(table, primaryKey, column string) Column {
	c := Column{table: table, pk: primaryKey, column: column}
	if !c.valid() {
		panic("secrets: Register needs table, primary key column and column")
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
