package nuget

import (
	"context"
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// R-ARCH-027 (Q-ARCH-291): the key of the rows the serve check wants is the version row and the tombstone of a local repository,
// and nothing for a path that names no version or a class the check does not apply to.
func TestR_ARCH_027_TheNuGetServeFactsKeyNamesTheRowsOfTheCheck(t *testing.T) {
	f := New()
	local := formats.RepoRef{ID: 3, Name: qr("l"), Class: formats.Local, Format: ID}
	remote := formats.RepoRef{ID: 4, Name: qr("r"), Class: formats.Remote, Format: ID}
	nupkg := formats.Path{Path: "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"}
	if got, want := f.ServeFactsKey(local, nupkg), (formats.ServeKey{Kind: formats.ServeNuGetLocal, Package: "acme.widgets", Version: "1.2.3"}); got != want {
		t.Errorf("key = %+v, want %+v", got, want)
	}
	for name, c := range map[string]struct {
		repo formats.RepoRef
		path formats.Path
	}{
		"a remote repository": {remote, nupkg},
		"a document":          {local, formats.Path{Path: "acme.widgets/index.json"}},
	} {
		if got := f.ServeFactsKey(c.repo, c.path); !got.Zero() {
			t.Errorf("%s: key = %+v, want none", name, got)
		}
	}
	if formats.CapabilitiesOf(f).ServeFacts == nil {
		t.Error("the format does not offer the key as a capability")
	}
}

// R-ARCH-027, R-NUGET-007: the serve check decides on the rows the lookup of the download read with the item: a listed and an
// unlisted version are served, a tombstoned one and one without a NuGet row are the 404 of a missing resource. The package the
// facts speak of is not in the data port, so the answer can only come from the facts; facts read for another version are not used.
func TestR_ARCH_027_TheNuGetServeCheckDecidesOnTheFactsOfTheLookup(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	ref := h.Ref()
	path := "ghost/1.0.0/ghost.1.0.0.nupkg"
	key := h.f.ServeFactsKey(ref, formats.Path{Path: path})
	check := func(facts formats.ServeFacts) error {
		return h.f.ServeCheck(context.Background(), h, ref, formats.ItemRef{Repo: ref, Path: path, Facts: facts}, formats.ServeStored)
	}
	for _, c := range []struct {
		name    string
		facts   formats.ServeFacts
		refused bool
	}{
		{"listed", formats.ServeFacts{Key: key, Stored: true, Listed: true}, false},
		{"unlisted", formats.ServeFacts{Key: key, Stored: true}, false},
		{"tombstoned", formats.ServeFacts{Key: key, Stored: true, Tombstoned: true}, true},
		{"without a NuGet row", formats.ServeFacts{Key: key}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := check(c.facts)
			var e *errs.Error
			if c.refused != (errors.As(err, &e) && e.Kind == errs.NotFound) || (!c.refused && err != nil) {
				t.Errorf("err = %v, want refused %v", err, c.refused)
			}
		})
	}
	// Facts read for another version do not decide: the data port knows no such package, so the 404 comes from it.
	other := formats.ServeKey{Kind: formats.ServeNuGetLocal, Package: "ghost", Version: "2.0.0"}
	err := check(formats.ServeFacts{Key: other, Stored: true, Listed: true})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.NotFound {
		t.Errorf("facts of another version were used: %v", err)
	}
}
