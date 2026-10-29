package postgres

import "fmt"

// Refs is the named catalog exception.
func Refs(table string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s WHERE sha256 = $1)", table)
}
