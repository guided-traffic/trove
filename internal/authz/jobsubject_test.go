package authz

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

func TestJobSubject(t *testing.T) {
	f := newFixture().repo("generic-local", "local", "generic", "")
	f.policy(policyDef{name: "devs-delete", selectors: []string{"generic-local"}, entries: map[string][]string{idDevs: {"read", "delete"}}})
	e := newTestEngine(t, f, nil)
	ctx := context.Background()
	asked := userSub(idAlice, "alice", idDevs)
	asked.Scope = &scope.Scope{Inherit: true}
	asked.TokenID, asked.AuthMethod, asked.Endpoint, asked.CacheFill, asked.MinEpoch = "tok-1", "bearer", "partners", true, 7
	j := JobSubjectOf(asked)
	devs := []principal.GroupRef{{ID: idDevs, Name: "developers"}}
	allowed := func(sub Subject) bool {
		t.Helper()
		got, err := e.PathsAllowed(ctx, sub, Coordinate{Repository: qr("generic-local")}, Delete, []string{"tree/a.bin"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got[0]
	}

	t.Run("R-STOR-018 the job carries who asked, the credential and the endpoint, and none of the grants", func(t *testing.T) {
		raw, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{idDevs, "groups", "scope", "inherit", "minEpoch"} {
			if strings.Contains(string(raw), leaked) {
				t.Errorf("the job subject carries %q: %s", leaked, raw)
			}
		}
		var back JobSubject
		if err := json.Unmarshal(raw, &back); err != nil || back != j {
			t.Fatalf("the job subject does not survive its encoding: %+v, %v", back, err)
		}
		if j.ID != idAlice || j.TokenID != "tok-1" || j.Endpoint != "partners" || !j.CacheFill {
			t.Fatalf("job subject = %+v", j)
		}
	})

	// R-AUTHZ-002: the groups are resolved when the step is evaluated.
	t.Run("R-STOR-018 a batch decides on the groups of the standing it read, not on those of the request", func(t *testing.T) {
		if !allowed(j.Subject(Standing{Groups: devs})) {
			t.Fatal("a member of the group is refused")
		}
		if allowed(j.Subject(Standing{})) {
			t.Fatal("a principal that left the group keeps its grant in the job")
		}
	})

	// R-AUTHZ-013: the token scope is a ceiling.
	t.Run("R-STOR-018 the scope of the standing is the ceiling of the batch", func(t *testing.T) {
		narrow := &scope.Scope{Repositories: []scope.Grant{{Repositories: []string{inst("generic-local")}, Actions: []string{"read"}}}}
		if allowed(j.Subject(Standing{Groups: devs, Scope: narrow})) {
			t.Fatal("a scope without delete lets the job delete")
		}
	})

	// R-EP-013: the exposure ceiling of the request's endpoint.
	t.Run("R-STOR-018 the subject of a batch keeps the endpoint and the credential of the request, and takes the kind and name of the standing", func(t *testing.T) {
		sub := j.Subject(Standing{Groups: devs})
		if sub.Endpoint != "partners" || !sub.CacheFill || sub.TokenID != "tok-1" || sub.AuthMethod != "bearer" || sub.MinEpoch != 0 || sub.Name != "alice" {
			t.Fatalf("subject = %+v", sub)
		}
		renamed := j.Subject(Standing{Kind: principal.KindUser, Name: "alice-renamed", Groups: devs})
		if renamed.Name != "alice-renamed" || renamed.Kind != principal.KindUser || renamed.ID != idAlice {
			t.Fatalf("the standing's name is not the batch's: %+v", renamed)
		}
	})

	// R-AUTHZ-002: a CI token's principal set.
	t.Run("R-STOR-018 a CI token's principal set is the mappings of the bundle the standing read", func(t *testing.T) {
		sub := JobSubject{Kind: principal.KindCI, ID: idMapAcm, TokenID: "cit-1"}.Subject(Standing{CI: &claims.Frozen{Mappings: []string{"ci:gitlab-internal/acme"}}})
		if len(sub.Mappings) != 1 || sub.Mappings[0] != "ci:gitlab-internal/acme" || sub.CI == nil {
			t.Fatalf("subject = %+v", sub)
		}
	})
}
