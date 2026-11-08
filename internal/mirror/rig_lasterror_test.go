package mirror

// lastError is the error recorded with a pending package, "" when it has none.
func (s *memStore) lastError(id int64, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pending[id][name]; p != nil {
		return p.lastError
	}
	return ""
}
