// Package tables is the table registry of R-BAK-004: every table of trove's
// schema has an export class, registered by its owning package.
//
// It is a leaf package so that internal/meta/postgres, internal/jobs,
// internal/cluster, internal/blob and internal/backup can all register and read
// without importing each other. The registry is filled at start-up and frozen
// (R-ARCH-032).
package tables

import (
	"fmt"
	"sort"
	"sync"
)

// Class says how a table travels through a backup (R-BAK-004).
type Class uint8

// The export classes. Data is the zero value because it is the default.
const (
	// Data tables export every row of the snapshot and load as exported.
	Data Class = iota
	// Blobs is the class of the blob table alone: only rows of digests
	// referenced in the snapshot are exported.
	Blobs
	// Transient tables are not exported and are empty after a restore.
	Transient
	// Schema tables are not exported; migrations recreate them.
	Schema
)

func (c Class) String() string {
	switch c {
	case Data:
		return "data"
	case Blobs:
		return "blobs"
	case Transient:
		return "transient"
	case Schema:
		return "schema"
	}
	return fmt.Sprintf("class(%d)", uint8(c))
}

// Exported reports whether rows of the class are part of a backup.
func (c Class) Exported() bool { return c == Data || c == Blobs }

// Options are the optional parts of a registration.
type Options struct {
	// CycleBreak names a nullable foreign-key column that a restore loads as
	// NULL and fills afterwards, for tables in a foreign-key cycle.
	CycleBreak string
}

// Table is one registration.
type Table struct {
	Name       string
	Class      Class
	CycleBreak string
}

// Registry holds the registrations. The zero value is not usable; use New.
type Registry struct {
	mu     sync.Mutex
	byName map[string]Table
	frozen bool
}

// New returns an empty registry; tests use their own, the process uses Default.
func New() *Registry { return &Registry{byName: map[string]Table{}} }

// Register adds a table. It panics on an empty name, a duplicate or a
// registration after Freeze: all are programming errors found at start-up.
func (r *Registry) Register(name string, class Class, opts Options) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" {
		panic("tables: Register with an empty table name")
	}
	if r.frozen {
		panic(fmt.Sprintf("tables: Register(%q) after the registry was frozen", name))
	}
	if _, dup := r.byName[name]; dup {
		panic(fmt.Sprintf("tables: table %q registered twice", name))
	}
	r.byName[name] = Table{Name: name, Class: class, CycleBreak: opts.CycleBreak}
}

// Freeze ends registration.
func (r *Registry) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// Lookup returns the registration of a table.
func (r *Registry) Lookup(name string) (Table, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.byName[name]
	return t, ok
}

// All returns every registration ordered by name.
func (r *Registry) All() []Table {
	r.mu.Lock()
	out := make([]Table, 0, len(r.byName))
	for _, t := range r.byName {
		out = append(out, t)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Clone returns an unfrozen copy, so a test can add tables to the real set.
func (r *Registry) Clone() *Registry {
	c := New()
	for _, t := range r.All() {
		c.byName[t.Name] = t
	}
	return c
}

var std = New()

// Default is the process-wide registry.
func Default() *Registry { return std }

// Register registers a table in the process-wide registry.
func Register(name string, class Class, opts Options) { std.Register(name, class, opts) }

// Freeze ends registration in the process-wide registry; the composition root
// calls it before it starts serving.
func Freeze() { std.Freeze() }

// All lists the process-wide registry.
func All() []Table { return std.All() }

// Lookup reads the process-wide registry.
func Lookup(name string) (Table, bool) { return std.Lookup(name) }
