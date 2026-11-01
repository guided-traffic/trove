package nuget

import (
	"context"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// Search is the search read of the local repositories (R-NUGET-010). The package that
// builds the search replaces this file; until then the method answers errs.NotImplemented,
// as the skeleton's did.
func (*Store) Search(context.Context, rows.SearchQuery) ([]rows.SearchHit, error) {
	return nil, errs.E(errs.NotImplemented, errs.CodeNuGetNotImplemented, "The NuGet data port does not implement Search yet.")
}
