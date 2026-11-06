package blob

// WithRows replaces the store's access to the blobs table with a simulation;
// the external tests of the protocol's branches use it.
func WithRows(r blobRows) Option { return func(s *Store) { s.rows = r } }

// MapStoreErr exposes the mapping of a store failure to the answer of a request.
func MapStoreErr(err error) error { return mapStoreErr(err) }
