package repository

// Bump writes the change log from outside internal/cluster.
const Bump = `UPDATE cluster_epoch SET epoch = epoch + 1 WHERE area = $1`

// Clear deletes change rows.
const Clear = "DELETE FROM cluster_change WHERE id < $1"
