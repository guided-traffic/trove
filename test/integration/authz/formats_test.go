//go:build integration

package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

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
		KnownFormat: func(name string) bool { _, ok := formats.Lookup(name); return ok },
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
