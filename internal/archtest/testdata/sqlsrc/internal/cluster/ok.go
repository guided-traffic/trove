package cluster

// Bump is the change log's own write.
const Bump = `UPDATE cluster_epoch SET epoch = epoch + 1 WHERE area = $1`

// Queries stands in for the generated type.
type Queries struct{}

// Run calls the generated writer: allowed inside internal/cluster.
func Run(q *Queries) { _ = q.BumpEpoch }
