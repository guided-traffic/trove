package proxy

import (
	"fmt"
	"strings"
)

// Build assembles SQL three ways.
func Build(table string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "DELETE FROM %s WHERE id = $1", table)
	q := fmt.Sprintf("SELECT id FROM %s WHERE x = $1", table)
	return q + " ORDER BY id; " + "SELECT 1 FROM " + table
}

// Plain concatenation of constants is not assembly.
const Static = "SELECT id " + "FROM repo"

// Message formatting that is no SQL.
func Message(n int) string { return fmt.Sprintf("%d replicas selected from the pool", n) }
