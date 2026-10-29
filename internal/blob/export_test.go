package blob

// WithRows replaces the store's access to the blobs table with a simulation;
// the external tests of the protocol's branches use it.
func WithRows(r blobRows) Option { return func(s *Store) { s.rows = r } }
