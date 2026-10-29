package postgres

import "fmt"

// Other is a second builder, which is not allowed.
func Other(table string) string { return fmt.Sprintf("SELECT id FROM %s", table) }
