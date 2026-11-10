package ansible

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/mirror/discover"
)

func admitAcme(n string) bool { return strings.HasPrefix(n, "acme.") }

func TestR_ANS_021_TheNamesMethodTakesTheLiteralPatternsThatReadAsCollectionNames(t *testing.T) {
	sc := discover.Scope{
		Include: []string{"acme.tools", "Acme.Base", "acme.*", "acme", "other.stuff", "acme.tools", "acme.a.b"},
		Admits:  admitAcme,
	}
	page, err := mirrorExactNames(discover.PageRequest{Method: discover.MethodNames, Scope: sc, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Folded, sorted, once; a pattern with a wildcard, a name without a dot, a name the scope refuses and one with two dots are left out.
	if want := []string{"acme.base", "acme.tools"}; !slices.Equal(page.Names, want) || !page.Done {
		t.Errorf("names %v done %v, want %v", page.Names, page.Done, want)
	}
	first, err := mirrorExactNames(discover.PageRequest{Scope: sc, Limit: 1})
	if err != nil || len(first.Names) != 1 || first.Done {
		t.Fatalf("first page %+v %v", first, err)
	}
	second, err := mirrorExactNames(discover.PageRequest{Scope: sc, Limit: 1, Cursor: first.Cursor})
	if err != nil || !slices.Equal(second.Names, []string{"acme.tools"}) || !second.Done {
		t.Errorf("second page %+v %v", second, err)
	}
	// A scope without the check admits nothing: a name that was not checked is never returned.
	if p, _ := mirrorExactNames(discover.PageRequest{Scope: discover.Scope{Include: []string{"acme.tools"}}}); len(p.Names) != 0 {
		t.Errorf("names without a check: %v", p.Names)
	}
}

func TestR_ANS_021_TheCursorOfTheListingIsAnOffsetAndNothingElse(t *testing.T) {
	if n, err := offsetCursor(cursorAt(250)); err != nil || n != 250 {
		t.Errorf("%d %v", n, err)
	}
	if n, err := offsetCursor(nil); err != nil || n != 0 {
		t.Errorf("no cursor: %d %v", n, err)
	}
	for _, bad := range []string{`{"offset":-1}`, `{"offset":2000000}`, `[1]`, `x`} {
		if _, err := offsetCursor(discover.Cursor(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

func TestR_ANS_021_TheGalaxyOffersListingAndNamesAndNoFeed(t *testing.T) {
	a := remoteAdapter{}
	got, err := a.MirrorMethods(context.Background(), nil)
	if err != nil || !slices.Equal(got, []discover.Method{discover.MethodListing, discover.MethodNames}) {
		t.Errorf("%v %v", got, err)
	}
	if _, err := a.MirrorNames(context.Background(), nil, discover.PageRequest{Method: discover.MethodFeed}); !errors.Is(err, discover.ErrMethodUnavailable) {
		t.Errorf("feed: %v", err)
	}
	if hosts := a.MirrorHosts(nil); len(hosts) != 0 {
		t.Errorf("mirror hosts %v", hosts)
	}
}
