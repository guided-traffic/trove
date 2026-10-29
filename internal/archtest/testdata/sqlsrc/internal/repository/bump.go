package repository

// Queries stands in for the generated type.
type Queries struct{}

// Run calls a generated statement that writes the change log.
func Run(q *Queries) { _ = q.BumpEpoch }
