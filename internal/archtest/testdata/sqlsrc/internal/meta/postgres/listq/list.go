package listq

import "fmt"

// Build is the confined list builder: it may assemble SQL.
func Build(table string) string { return fmt.Sprintf("SELECT id FROM %s WHERE x = $1", table) }
