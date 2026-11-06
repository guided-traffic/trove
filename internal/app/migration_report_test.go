package app

import (
	"testing"

	nugetdata "github.com/guided-traffic/trove/internal/formatdata/nuget"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func TestBackgroundMigrations(t *testing.T) {
	t.Run("R-HA-022 every background migration a step of this binary depends on is declared, and the declarations are valid", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		if err := postgres.CheckBackgroundDependencies(steps, BackgroundMigrations()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-022 the NuGet SemVer 2 recomputation is declared under the name of its job kind", func(t *testing.T) {
		var found bool
		for _, m := range BackgroundMigrations() {
			if m.Name == "nuget_semver2" {
				found = true
				if m.Kind() != nugetdata.Semver2JobKind || m.DoneKey != nugetdata.Semver2StateKey || m.Release != "0.10.0" {
					t.Fatalf("declaration = %+v (kind %s)", m, m.Kind())
				}
			}
		}
		if !found {
			t.Fatalf("nuget_semver2 is not among %+v", BackgroundMigrations())
		}
	})
}
