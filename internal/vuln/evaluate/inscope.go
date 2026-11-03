package evaluate

import (
	"context"

	"github.com/guided-traffic/trove/internal/repository"
)

// InScope reports whether a version of the package in the repository is one that public advisories apply to (R-VULN-010): the
// evaluation state `evaluated` or `pending_data`, not `internal` and not `not_covered`. It is the rule of the remote miss
// (EvaluateMiss, Q-VULN-38) as a question, for the download of a stored version: the data rule `onMissingData: block` refuses a
// version only where the advisory data could decide something, so the first request of a version and the later ones answer
// alike (Q-VULN-65). A repository the scanning does not apply to is not in scope.
func (s *Service) InScope(ctx context.Context, repo repository.Repository, name string) (bool, error) {
	if !s.o.Enabled {
		return false, nil
	}
	rv, ok := viewOf(repo)
	if !ok || !rv.Settings.Enabled {
		return false, nil
	}
	if _, known := s.eco[rv.Ecosystem]; !known {
		return false, nil
	}
	sc, _, err := s.scopeOf(ctx, rv, name, s.rule)
	if err != nil {
		return false, err
	}
	return sc == ScopeMatched, nil
}
