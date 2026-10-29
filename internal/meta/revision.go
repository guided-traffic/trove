package meta

import (
	"fmt"
	"sort"
	"sync"
)

// RevisionColumn names a bigint column whose values clients hold as an ETag or
// send as If-Match, or that replicas compare as an epoch. A restore raises every
// registered column by 2^32 so no such value repeats with different content
// (R-ARCH-009, R-BAK-022).
type RevisionColumn struct {
	Table  string
	Column string
}

// RevisionRegistry is the set of revision columns. Owners register at start-up;
// Freeze ends registration (R-ARCH-032).
type RevisionRegistry struct {
	mu     sync.Mutex
	cols   []RevisionColumn
	frozen bool
}

// NewRevisionRegistry returns an empty registry; tests use their own.
func NewRevisionRegistry() *RevisionRegistry { return &RevisionRegistry{} }

// Register adds a column. It panics on a duplicate or after Freeze: both are
// programming errors found at start-up, never at request time.
func (r *RevisionRegistry) Register(table, column string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		panic(fmt.Sprintf("meta: RegisterRevisionColumn(%s, %s) after the registry was frozen", table, column))
	}
	rc := RevisionColumn{Table: table, Column: column}
	for _, c := range r.cols {
		if c == rc {
			panic(fmt.Sprintf("meta: revision column %s.%s registered twice", table, column))
		}
	}
	r.cols = append(r.cols, rc)
}

// Freeze ends registration.
func (r *RevisionRegistry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Columns returns the registered columns ordered by table and column.
func (r *RevisionRegistry) Columns() []RevisionColumn {
	r.mu.Lock()
	out := append([]RevisionColumn(nil), r.cols...)
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Column < out[j].Column
	})
	return out
}

// Has reports whether the column is registered.
func (r *RevisionRegistry) Has(table, column string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cols {
		if c.Table == table && c.Column == column {
			return true
		}
	}
	return false
}

var revisions = NewRevisionRegistry()

// RegisterRevisionColumn registers a column in the process-wide registry.
func RegisterRevisionColumn(table, column string) { revisions.Register(table, column) }

// RevisionColumns lists the process-wide registry.
func RevisionColumns() []RevisionColumn { return revisions.Columns() }

// FreezeRevisionColumns ends registration in the process-wide registry; the
// composition root calls it before it starts serving.
func FreezeRevisionColumns() { revisions.Freeze() }

// IsRevisionColumn reports whether the process-wide registry holds the column.
func IsRevisionColumn(table, column string) bool { return revisions.Has(table, column) }

// DefaultRevisionRegistry is the process-wide registry, for the migration test
// that compares it with the migrated schema.
func DefaultRevisionRegistry() *RevisionRegistry { return revisions }
