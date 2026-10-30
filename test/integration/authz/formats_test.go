//go:build integration

package authz

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// lowerNames is the name rule of a format whose package names compare
// case-insensitively, and exactNames of one whose names compare as written.
type (
	lowerNames struct{}
	exactNames struct{}
)

func (lowerNames) Normalize(name string) (string, bool) { return strings.ToLower(name), name != "" }
func (exactNames) Normalize(name string) (string, bool) { return name, name != "" }

// integrationFormats is the catalog of these tests: the registered generic format
// and, for the package-name rules the grants of the suite exercise, stand-ins for
// a format with case-sensitive names (npm) and one with case-insensitive names
// (nuget). The decision point learns a format's name rule from the catalog only.
func integrationFormats() *catalog.Registry {
	r := catalog.NewRegistry()
	for _, e := range catalog.Default().List() {
		r.Register(e)
	}
	all := []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}
	r.Register(catalog.Entry{
		Facts: catalog.Facts{ID: "npm", Classes: all, Layout: "npm", PatternSubject: catalog.SubjectName, PackageNames: true},
		Names: exactNames{},
	})
	r.Register(catalog.Entry{
		Facts: catalog.Facts{ID: "nuget", Classes: all, Layout: "nuget", PatternSubject: catalog.SubjectName, PackageNames: true},
		Names: lowerNames{},
	})
	return r
}

// TestR_AUTHZ_004_FormatSelectorComesFromTheFormatRegistry: a policy that names a
// format the registry does not hold is refused with 422 unknown_format in
// errors[], whatever the format is called (no literal list of formats).
func TestR_AUTHZ_004_FormatSelectorComesFromTheFormatRegistry(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	admin := sub(e.user("sec-alice"))
	e.user("alice")
	e.repo("gen", "local", "generic", "")
	svc := authz.NewService(authz.ServiceOptions{
		DB: a.db, Changes: a.changes, Principals: sqlPrincipals{e}, Repositories: sqlRepositories{e}, Recorder: a.rec,
		Formats: catalog.Default(),
	})
	in := func(selector string) authz.PolicyInput {
		return authz.PolicyInput{
			Name: "p", Resources: authz.Resources{Repositories: []string{selector}},
			Entries: []authz.Entry{{Principal: "user:alice", Actions: []string{"read"}}},
		}
	}
	if _, _, err := svc.Create(t.Context(), admin, in("@any-local:generic")); err != nil {
		t.Fatalf("a registered format: %v", err)
	}
	_, _, err := svc.Create(t.Context(), admin, in("@any-local:npm"))
	ee, ok := errs.As(err)
	if !ok || ee.Status() != 422 || ee.Code != errs.CodeValidation || len(ee.Fields) != 1 || ee.Fields[0].Code != errs.CodeUnknownFormat {
		t.Fatalf("err = %v", err)
	}
}
