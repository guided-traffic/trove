package repository

import (
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// foldingNames is the name rule of a format whose names compare case-insensitively and whose
// folding validates: a name with a space or a slash is no name.
type foldingNames struct{}

func (foldingNames) Normalize(name string) (string, bool) {
	return strings.ToLower(name), name != "" && !strings.ContainsAny(name, " /")
}

func foldingFixture(t *testing.T) *routeFixture {
	t.Helper()
	f := newRouteFixture(t)
	f.cat = catalog.NewRegistry()
	f.cat.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "folding", Classes: []Class{Local, Remote, Virtual}, Layout: "folding", ProtocolBasePath: "/api/folding/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
	}, Names: foldingNames{}})
	return f
}

// TestR_ARCH_008_AnUnfoldableNameFailsClosedInRouting: a name the format's rule cannot fold is looked up
// nowhere as written, because the spelling would miss the claim on its folded form: it reaches no
// remote member, is admitted by no virtual repository and is refused at a remote repository.
func TestR_ARCH_008_AnUnfoldableNameFailsClosedInRouting(t *testing.T) {
	f := foldingFixture(t)
	f.repo("team", Local, "folding")
	remote := f.repo("remote", Remote, "folding")
	v := f.virtual("all", "folding", []string{"team", "remote"}, nil)
	f.claim("team", "acme.*", ScopeGlobal)

	t.Run("R-REPO-023 a name that folds is claimed whatever its spelling", func(t *testing.T) {
		r := f.route(v, "ACME.Widgets")
		if !slices.Equal(memberKeys(r), []string{"team"}) || r.Decision.Kind != DecidedByClaim {
			t.Fatalf("route = %+v", r)
		}
	})
	t.Run("R-ARCH-008 a name the rule rejects is not admitted by the virtual repository and reaches no member", func(t *testing.T) {
		r := f.route(v, "ACME.Widgets 1")
		if r.Admitted || slices.Contains(memberKeys(r), "remote") || r.Decision.Kind != "" {
			t.Fatalf("route = %+v", r)
		}
		if r.Dropped["remote"] != DropNotAdmitted {
			t.Fatalf("dropped = %v", r.Dropped)
		}
	})
	t.Run("R-ARCH-008 a remote repository refuses an unfoldable name with the claim switch on and off", func(t *testing.T) {
		idx := buildClaimIndex(f.rows, slog.New(slog.DiscardHandler))
		if !remoteRefused(f.cat, idx, remote, "ACME.Widgets/x", nil) {
			t.Fatal("remoteRefused let an unfoldable name through")
		}
		for _, on := range []bool{true, false} {
			rt := &Routing{cat: f.cat, refusal: func() bool { return on }}
			if got, err := rt.RemoteRefused(t.Context(), remote, "ACME.Widgets/x", nil); err != nil || !got {
				t.Errorf("RemoteRefused(switch %v) = %v, %v", on, got, err)
			}
		}
	})
	t.Run("R-REPO-048 an unfoldable name is covered by no claim, so the shadowing check asks", func(t *testing.T) {
		idx := buildClaimIndex(f.rows, slog.New(slog.DiscardHandler))
		if coveredByOwn(f.cat, idx, f.repos["team"], "ACME.Widgets 1") {
			t.Fatal("covered")
		}
		if !coveredByOwn(f.cat, idx, f.repos["team"], "ACME.Widgets") {
			t.Fatal("a name that folds is covered by the claim on its folded form")
		}
	})
}
