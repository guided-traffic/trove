package blob

// The statements of the store, for the fake database of the external tests.
const (
	SQLLockShare   = sqlLockShare
	SQLInsertBlob  = sqlInsertBlob
	SQLBlobExists  = sqlBlobExists
	SQLMarkOK      = sqlMarkOK
	SQLEnqueue     = sqlEnqueue
	SQLMarkDamaged = sqlMarkDamaged
)

// FullSweepSQL exposes the full-sweep query builder.
func FullSweepSQL(tables, columns []string) string {
	refs := make([]refTable, len(tables))
	for i := range tables {
		refs[i] = refTable{tables[i], columns[i]}
	}
	return fullSweepSQL(refs)
}
