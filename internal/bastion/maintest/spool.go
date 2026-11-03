package maintest

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/guided-traffic/trove/internal/bastion/cache"
)

// SpoolWrite implements the audit spool: the file is named by a counter.
func (s *MemSpool) SpoolWrite(_ context.Context, replica string, jsonl []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailWrites {
		return errors.New("maintest: the spool cannot be written")
	}
	s.n++
	s.files[fmt.Sprintf("%06d.jsonl", s.n)] = memFile{replica: replica, data: append([]byte(nil), jsonl...)}
	return nil
}

// SpoolList lists the files in name order.
func (s *MemSpool) SpoolList(context.Context) ([]cache.SpoolFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []cache.SpoolFile
	for k, f := range s.files {
		out = append(out, cache.SpoolFile{Replica: f.replica, Name: k, Size: int64(len(f.data))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SpoolRead returns a file, or cache.ErrNotFound.
func (s *MemSpool) SpoolRead(_ context.Context, f cache.SpoolFile) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if file, ok := s.files[f.Name]; ok {
		return append([]byte(nil), file.data...), nil
	}
	return nil, cache.ErrNotFound
}

// SpoolDelete removes a file.
func (s *MemSpool) SpoolDelete(_ context.Context, f cache.SpoolFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, f.Name)
	return nil
}
