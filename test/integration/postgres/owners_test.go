//go:build integration

package postgres

// The packages that own tables register them in their init functions (R-BAK-004; the
// NuGet data port owns the tables of 00017_nuget). The registry tests of this package compare
// the migrated schema with the registry, so the owners are linked in.
import (
	_ "github.com/guided-traffic/trove/internal/blob"
	_ "github.com/guided-traffic/trove/internal/cluster"
	_ "github.com/guided-traffic/trove/internal/formatdata/nuget"
	_ "github.com/guided-traffic/trove/internal/jobs"
)
