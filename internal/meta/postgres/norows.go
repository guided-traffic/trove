package postgres

// IsNoRows reports whether err is the result of a query that returns one row and found none. Packages above the
// store use it so that they need not import the driver (R-ARCH-002).
func IsNoRows(err error) bool { return isNoRows(err) }
