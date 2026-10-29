package tables

import (
	"fmt"
	"sort"
	"strings"
)

// ForeignKey is one foreign-key column pair as the catalog shows it. A
// composite key contributes one entry per column.
type ForeignKey struct {
	Table    string // referencing table
	Column   string // referencing column
	RefTable string // referenced table
}

// Problem is one finding of the migration test (R-BAK-004).
type Problem struct {
	Table  string
	Reason string
}

func (p Problem) String() string { return p.Table + ": " + p.Reason }

// Check applies the rules of the migration test to the catalog of a migrated
// schema: every table must be registered, and the foreign-key graph of the
// data and blobs tables must be acyclic once the registered CycleBreak columns
// are cut. tables lists every ordinary and partitioned table of the schema
// (partitions excluded, they export through their parent).
func (r *Registry) Check(tables []string, fks []ForeignKey) []Problem {
	var out []Problem
	for _, name := range tables {
		if _, ok := r.Lookup(name); !ok {
			out = append(out, Problem{name, "has no export class; register it with tables.Register (R-BAK-004)"})
		}
	}
	for _, t := range r.All() {
		if t.CycleBreak != "" && !hasColumnFK(fks, t.Name, t.CycleBreak) {
			out = append(out, Problem{t.Name, fmt.Sprintf("CycleBreak %q is not a foreign-key column", t.CycleBreak)})
		}
	}
	out = append(out, r.cycles(fks)...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func hasColumnFK(fks []ForeignKey, table, column string) bool {
	for _, fk := range fks {
		if fk.Table == table && fk.Column == column {
			return true
		}
	}
	return false
}

// cycles reports each strongly connected component of the exported tables'
// foreign-key graph that remains after the CycleBreak columns are removed.
func (r *Registry) cycles(fks []ForeignKey) []Problem {
	exported := func(name string) bool {
		t, ok := r.Lookup(name)
		return ok && t.Class.Exported()
	}
	adj := map[string][]string{}
	nodes := map[string]bool{}
	selfLoop := map[string]bool{}
	for _, fk := range fks {
		if !exported(fk.Table) || !exported(fk.RefTable) {
			continue
		}
		if t, _ := r.Lookup(fk.Table); t.CycleBreak == fk.Column {
			continue
		}
		nodes[fk.Table], nodes[fk.RefTable] = true, true
		adj[fk.Table] = append(adj[fk.Table], fk.RefTable)
		if fk.Table == fk.RefTable {
			selfLoop[fk.Table] = true
		}
	}
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)

	// Tarjan's strongly connected components.
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	next := 0
	var out []Problem
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		targets := append([]string(nil), adj[v]...)
		sort.Strings(targets)
		for _, w := range targets {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) > 1 || selfLoop[comp[0]] {
			sort.Strings(comp)
			out = append(out, Problem{comp[0], "foreign-key cycle through " + strings.Join(comp, ", ") +
				" has no registered CycleBreak column (R-BAK-004)"})
		}
	}
	for _, n := range names {
		if _, seen := index[n]; !seen {
			visit(n)
		}
	}
	return out
}
