//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

func allowGlobal(context.Context) error { return nil }

func refuseGlobal(context.Context) error {
	return errs.E(errs.Forbidden, errs.CodeForbidden, "a global claim needs repositories:write-config")
}

func (r *replica) autoClaim(repoKey, name string) (repository.AutoClaimResult, error) {
	r.t.Helper()
	ctx := context.Background()
	repo, err := r.reader.ByKey(ctx, repoKey)
	if err != nil {
		return repository.AutoClaimResult{}, err
	}
	var res repository.AutoClaimResult
	err = r.db.InTx(ctx, func(tx meta.Tx) error {
		var err error
		res, err = r.routing.AutoClaim(ctx, tx, repo, name, caller)
		return err
	})
	return res, err
}

func claimPatterns(set repository.ClaimSet) []string {
	var out []string
	for _, c := range set.Claims {
		out = append(out, c.Source+":"+c.Scope+":"+c.Pattern)
	}
	return out
}

func claimsVersion(e *env, key string) int64 {
	return e.scalar(`SELECT claims_version FROM repositories WHERE key = $1`, key)
}

func TestClaims(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	a := e.newReplica("claims-a", replicaOpts{poll: poll, catalog: pkgCatalog()})
	b := e.newReplica("claims-b", replicaOpts{poll: poll, catalog: pkgCatalog()})
	ctx := context.Background()
	for _, k := range []string{"team-a", "team-b", "sandbox"} {
		a.mustCreate(k, "local", "pkgfmt", "")
	}
	a.mustCreate("manual", "local", "pkgfmt", `"local":{"autoClaim":false}`)
	a.mustCreate("remote", "remote", "pkgfmt", "")
	a.mustCreate("generic-local", "local", "nopkg", "")
	a.mustCreate("all", "virtual", "pkgfmt", `"virtual":{"members":["team-a","team-b","remote"]}`)

	put := func(rep *replica, key string, pre repository.Precondition, in repository.ClaimInput, guard repository.GlobalGuard) (repository.ClaimSet, error) {
		return rep.svc.PutClaims(ctx, caller, key, pre, in, guard)
	}
	claim := func(p, scope string) repository.ClaimEntry { return repository.ClaimEntry{Pattern: p, Scope: scope} }

	t.Run("R-REPO-023 a new repository has no claims and claims_version 1", func(t *testing.T) {
		set, err := a.svc.Claims(ctx, "team-a")
		if err != nil || len(set.Claims) != 0 || set.Version != 1 {
			t.Fatalf("claims = %+v, %v", set, err)
		}
		_, err = a.svc.Claims(ctx, "ghost")
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})

	t.Run("R-REPO-023 a members claim needs no global permission and moves claims_version", func(t *testing.T) {
		set, err := put(a, "team-a", repository.MatchVersion(1), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "")}}, refuseGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if set.Version != 2 || !slices.Equal(claimPatterns(set), []string{"manual:members:@acme/*"}) {
			t.Fatalf("claims = %+v", set)
		}
		if claimsVersion(e, "team-a") != 2 || e.scalar(`SELECT config_version FROM repositories WHERE key = 'team-a'`) != 1 {
			t.Fatal("claims_version moved the wrong revision")
		}
		ev := a.rec.last()
		var d struct {
			Source        string `json:"source"`
			Added         []struct{ Pattern, Scope string }
			Removed       []string `json:"removed"`
			ClaimsVersion int64    `json:"claimsVersion"`
		}
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Type != repository.EventClaimsChanged || d.Source != "manual" || len(d.Added) != 1 || d.Added[0].Pattern != "@acme/*" || d.ClaimsVersion != 2 {
			t.Fatalf("event = %s %s", ev.Type, ev.Details)
		}
	})

	t.Run("R-REPO-023 a stale If-Match answers 412 with the current tag", func(t *testing.T) {
		_, err := put(a, "team-a", repository.MatchVersion(1), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "")}}, allowGlobal)
		wantCode(t, err, 412, errs.CodePreconditionFailed)
		if cur, ok := errs.CurrentRevision(err); !ok || cur != 2 {
			t.Fatalf("current revision = %d, %v", cur, ok)
		}
	})

	t.Run("R-REPO-023 a global claim needs repositories:write-config; the refusal changes nothing", func(t *testing.T) {
		_, err := put(a, "team-a", repository.MatchVersion(2), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "global")}}, refuseGlobal)
		wantCode(t, err, 403, errs.CodeForbidden)
		if claimsVersion(e, "team-a") != 2 {
			t.Fatal("a refused write moved claims_version")
		}
		set, err := put(a, "team-a", repository.MatchVersion(2), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "global")}}, allowGlobal)
		if err != nil || set.Version != 3 || !slices.Equal(claimPatterns(set), []string{"manual:global:@acme/*"}) {
			t.Fatalf("claims = %+v, %v", set, err)
		}
	})

	t.Run("R-REPO-023 keeping, narrowing or dropping a global claim also needs the permission", func(t *testing.T) {
		keep := repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "global"), claim("left-*", "members")}}
		_, err := put(a, "team-a", repository.MatchVersion(3), keep, refuseGlobal)
		wantCode(t, err, 403, errs.CodeForbidden)
		narrow := repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "members")}}
		_, err = put(a, "team-a", repository.MatchVersion(3), narrow, refuseGlobal)
		wantCode(t, err, 403, errs.CodeForbidden)
		_, err = put(a, "team-a", repository.MatchVersion(3), repository.ClaimInput{}, refuseGlobal)
		wantCode(t, err, 403, errs.CodeForbidden)
		if claimsVersion(e, "team-a") != 3 {
			t.Fatal("a refused write moved claims_version")
		}
	})

	t.Run("R-REPO-023 If-Match: * is the deliberate blind write", func(t *testing.T) {
		set, err := put(a, "team-a", repository.MatchAny(), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "global"), claim("left-*", "")}}, allowGlobal)
		if err != nil || set.Version != 4 || len(set.Claims) != 2 {
			t.Fatalf("claims = %+v, %v", set, err)
		}
	})

	t.Run("R-REPO-023 a write that changes nothing keeps the tag and records nothing", func(t *testing.T) {
		n := len(a.rec.types())
		set, err := put(a, "team-a", repository.MatchVersion(4), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/*", "global"), claim("left-*", "members")}}, allowGlobal)
		if err != nil || set.Version != 4 || len(a.rec.types()) != n {
			t.Fatalf("claims = %+v, %v, events %d -> %d", set, err, n, len(a.rec.types()))
		}
	})

	t.Run("R-REPO-023 R-NPM-033 a pattern outside the two forms is invalid_pattern; so is a duplicate scope value", func(t *testing.T) {
		in := repository.ClaimInput{Claims: []repository.ClaimEntry{claim("a*b", ""), claim("@acme/*x", ""), claim("ok", "everywhere"), claim("dup", ""), claim("dup", "")}}
		_, err := put(a, "team-b", repository.MatchVersion(1), in, allowGlobal)
		wantCode(t, err, 422, errs.CodeValidation)
		f := fieldPointers(t, err)
		want := map[string]string{
			"/claims/0/pattern": "invalid_pattern", "/claims/1/pattern": "invalid_pattern",
			"/claims/2/scope": "invalid_value", "/claims/4/pattern": "invalid_value",
		}
		for p, c := range want {
			if f[p] != c {
				t.Errorf("%s = %q, want %q (%v)", p, f[p], c, f)
			}
		}
	})

	t.Run("R-REPO-023 a repository that cannot hold claims answers not_applicable", func(t *testing.T) {
		for _, key := range []string{"remote", "all", "generic-local"} {
			_, err := put(a, key, repository.MatchAny(), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("x", "")}}, allowGlobal)
			wantCode(t, err, 422, errs.CodeValidation)
			if f := fieldPointers(t, err); f["/claims"] != errs.CodeNotApplicable {
				t.Errorf("%s: fields = %v", key, f)
			}
		}
	})

	t.Run("R-REPO-023 more than 1000 claims are refused", func(t *testing.T) {
		in := repository.ClaimInput{}
		for i := range repository.MaxClaims + 1 {
			in.Claims = append(in.Claims, claim("name-"+strconv.Itoa(i), ""))
		}
		_, err := put(a, "team-b", repository.MatchAny(), in, allowGlobal)
		if f := fieldPointers(t, err); f["/claims"] != "out_of_range" {
			t.Fatalf("fields = %v", f)
		}
	})

	t.Run("R-REPO-023 claim_overlaps marks a claim that overlaps another repository's claim in effect with it", func(t *testing.T) {
		// team-a holds the global @acme/*; team-b shares the virtual repository with team-a.
		set, err := put(a, "team-b", repository.MatchVersion(1), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("@acme/billing-*", ""), claim("unrelated", "")}}, allowGlobal)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.Warnings) != 1 || set.Warnings[0].Code != "claim_overlaps" || set.Warnings[0].Pointer != "/claims/0" {
			t.Fatalf("warnings = %+v", set.Warnings)
		}
		// A members claim of a repository that no virtual repository shares is no overlap.
		set, err = put(a, "sandbox", repository.MatchVersion(1), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("left-pad", "")}}, allowGlobal)
		if err != nil || len(set.Warnings) != 0 {
			t.Fatalf("claims = %+v, %v", set, err)
		}
	})

	t.Run("R-REPO-023 a claim change is in force on the other replica within 2 seconds, without Valkey", func(t *testing.T) {
		start := time.Now()
		// Prime B's index, then change through A.
		if _, err := b.routing.ClaimsFor(ctx, "pkgfmt", "@fresh/x"); err != nil {
			t.Fatal(err)
		}
		if _, err := put(a, "sandbox", repository.MatchAny(), repository.ClaimInput{Claims: []repository.ClaimEntry{claim("left-pad", ""), claim("@fresh/*", "")}}, allowGlobal); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to see the claim", 2*time.Second, func() bool {
			m, err := b.routing.ClaimsFor(ctx, "pkgfmt", "@fresh/x")
			return err == nil && len(m) == 1 && m[0].Repository == "sandbox"
		})
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %s", d)
		}
	})

	t.Run("R-REPO-023 B routes a claimed name through the claim", func(t *testing.T) {
		waitFor(t, "B to know the virtual repository", 3*poll, func() bool {
			r, err := b.reader.ByKey(ctx, "all")
			return err == nil && len(r.Members()) == 3
		})
		v, _ := b.reader.ByKey(ctx, "all")
		route, err := b.routing.Route(ctx, v, repository.RouteRequest{Name: "@acme/util"})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(route.Members); !slices.Equal(got, []string{"team-a"}) || !slices.Equal(route.Decision.ClaimedBy, []string{"team-a"}) {
			t.Fatalf("route = %+v", route)
		}
		route, _ = b.routing.Route(ctx, v, repository.RouteRequest{Name: "@acme/billing-core"})
		if got := keysOf(route.Members); !slices.Equal(got, []string{"team-b"}) {
			t.Fatalf("most specific claim: route = %+v", route)
		}
		route, _ = b.routing.Route(ctx, v, repository.RouteRequest{Name: "lodash"})
		if got := keysOf(route.Members); !slices.Equal(got, []string{"team-a", "team-b", "remote"}) {
			t.Fatalf("unclaimed: route = %+v", route)
		}
	})

	t.Run("R-REPO-023 remote refusal uses the global claims", func(t *testing.T) {
		rem, _ := b.reader.ByKey(ctx, "remote")
		refused, err := b.routing.RemoteRefused(ctx, rem, "@acme/new-thing", nil)
		if err != nil || !refused {
			t.Fatalf("refused = %v, %v", refused, err)
		}
		refused, _ = b.routing.RemoteRefused(ctx, rem, "@fresh/x", nil) // a members claim
		if refused {
			t.Fatal("a members claim refused at the remote")
		}
	})
}

func TestAutoClaim(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	a := e.newReplica("auto-a", replicaOpts{poll: poll, catalog: pkgCatalog()})
	b := e.newReplica("auto-b", replicaOpts{poll: poll, catalog: pkgCatalog()})
	ctx := context.Background()
	for _, k := range []string{"team-a", "team-b", "sandbox"} {
		a.mustCreate(k, "local", "pkgfmt", "")
	}
	a.mustCreate("manual", "local", "pkgfmt", `"local":{"autoClaim":false}`)
	a.mustCreate("remote", "remote", "pkgfmt", "")
	a.mustCreate("all", "virtual", "pkgfmt", `"virtual":{"members":["team-a","team-b","remote"]}`)
	if _, err := a.svc.PutClaims(ctx, caller, "team-a", repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@acme/*"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.PutClaims(ctx, caller, "sandbox", repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@org/*", Scope: "global"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}

	t.Run("R-REPO-023 the first publish of a name creates an exact claim of scope members", func(t *testing.T) {
		before := claimsVersion(e, "team-b")
		res, err := a.autoClaim("team-b", "left-pad")
		if err != nil || !res.Created || res.Pattern != "left-pad" {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if claimsVersion(e, "team-b") != before+1 {
			t.Fatal("claims_version did not move")
		}
		set, _ := a.svc.Claims(ctx, "team-b")
		if !slices.Equal(claimPatterns(set), []string{"auto:members:left-pad"}) {
			t.Fatalf("claims = %v", claimPatterns(set))
		}
		ev := a.rec.last()
		if ev.Type != repository.EventClaimsChanged || ev.Actor.ID != caller.Actor.ID {
			t.Fatalf("event = %s %s", ev.Type, ev.Details)
		}
		var d struct{ Source string }
		_ = json.Unmarshal(ev.Details, &d)
		if d.Source != "auto" {
			t.Fatalf("event details = %s", ev.Details)
		}
	})

	t.Run("R-REPO-023 R-REPO-048 a later version of a claimed name writes nothing", func(t *testing.T) {
		before, n := claimsVersion(e, "team-b"), len(a.rec.types())
		res, err := a.autoClaim("team-b", "left-pad")
		if err != nil || res.Created || res.Reason != repository.AutoClaimCovered {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if claimsVersion(e, "team-b") != before || len(a.rec.types()) != n {
			t.Fatal("a covered name wrote a version or an event")
		}
	})

	t.Run("R-REPO-023 a prefix claim of the repository covers the name: no exact claim", func(t *testing.T) {
		res, err := a.autoClaim("team-a", "@acme/util")
		if err != nil || res.Created || res.Reason != repository.AutoClaimCovered {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})

	t.Run("R-REPO-023 local.autoClaim: false creates no claim", func(t *testing.T) {
		res, err := a.autoClaim("manual", "something")
		if err != nil || res.Created || res.Reason != repository.AutoClaimDisabled {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims c JOIN repositories r ON r.id = c.repo_id WHERE r.key = 'manual'`); n != 0 {
			t.Fatal("a claim exists")
		}
	})

	t.Run("R-REPO-023 an auto claim does not take a name from a claim in effect in a shared virtual repository", func(t *testing.T) {
		// team-a claims @acme/*, team-b and team-a are members of "all".
		before := claimsVersion(e, "team-b")
		res, err := a.autoClaim("team-b", "@acme/util")
		if err != nil || res.Created || res.Reason != repository.AutoClaimSkipped {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if claimsVersion(e, "team-b") != before {
			t.Fatal("a skipped claim moved claims_version")
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims c JOIN repositories r ON r.id = c.repo_id WHERE r.key = 'team-b' AND c.pattern = '@acme/util'`); n != 0 {
			t.Fatal("the claim was created")
		}
	})

	t.Run("R-REPO-023 a global claim of another repository is in effect in every virtual repository", func(t *testing.T) {
		res, err := a.autoClaim("team-b", "@org/tool")
		if err != nil || res.Created || res.Reason != repository.AutoClaimSkipped {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})

	t.Run("R-REPO-023 a repository in no virtual repository is never skipped: its claim is exact and members-scoped", func(t *testing.T) {
		a.mustCreate("lonely", "local", "pkgfmt", "")
		res, err := a.autoClaim("lonely", "@org/tool")
		if err != nil || !res.Created {
			t.Fatalf("result = %+v, %v", res, err)
		}
		a.mustCreate("sandbox2", "local", "pkgfmt", "")
		if res, err := a.autoClaim("sandbox2", "react"); err != nil || !res.Created {
			t.Fatalf("result = %+v, %v", res, err)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims WHERE pattern = 'react' AND scope = 'members' AND source = 'auto'`); n != 1 {
			t.Fatal("the claim of react is not an auto claim of scope members")
		}
	})

	t.Run("R-REPO-023 after an auto claim, a PUT with the tag read before answers 412 with the new tag", func(t *testing.T) {
		set, _ := a.svc.Claims(ctx, "team-b")
		if _, err := a.autoClaim("team-b", "right-pad"); err != nil {
			t.Fatal(err)
		}
		_, err := a.svc.PutClaims(ctx, caller, "team-b", repository.MatchVersion(set.Version), repository.ClaimInput{}, allowGlobal)
		wantCode(t, err, 412, errs.CodePreconditionFailed)
		if cur, _ := errs.CurrentRevision(err); cur != set.Version+1 {
			t.Fatalf("current = %d, want %d", cur, set.Version+1)
		}
	})

	t.Run("R-REPO-023 a PUT replaces the manual claims and keeps the automatic ones unless removeAuto names them", func(t *testing.T) {
		set, _ := a.svc.Claims(ctx, "team-b")
		got, err := a.svc.PutClaims(ctx, caller, "team-b", repository.MatchVersion(set.Version),
			repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@team-b/*"}}}, allowGlobal)
		if err != nil || !slices.Equal(claimPatterns(got), []string{"manual:members:@team-b/*", "auto:members:left-pad", "auto:members:right-pad"}) {
			t.Fatalf("claims = %v, %v", claimPatterns(got), err)
		}
		got, err = a.svc.PutClaims(ctx, caller, "team-b", repository.MatchVersion(got.Version),
			repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@team-b/*"}}, RemoveAuto: []string{"right-pad"}}, allowGlobal)
		if err != nil || !slices.Equal(claimPatterns(got), []string{"manual:members:@team-b/*", "auto:members:left-pad"}) {
			t.Fatalf("claims = %v, %v", claimPatterns(got), err)
		}
		_, err = a.svc.PutClaims(ctx, caller, "team-b", repository.MatchVersion(got.Version),
			repository.ClaimInput{RemoveAuto: []string{"@team-b/*"}}, allowGlobal)
		wantCode(t, err, 422, errs.CodeValidation) // a manual claim is no automatic claim
		// Listing an automatic claim's pattern as manual turns it into a manual claim (Q-REPO-106).
		got, err = a.svc.PutClaims(ctx, caller, "team-b", repository.MatchVersion(got.Version),
			repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@team-b/*"}, {Pattern: "left-pad"}}}, allowGlobal)
		if err != nil || !slices.Equal(claimPatterns(got), []string{"manual:members:@team-b/*", "manual:members:left-pad"}) {
			t.Fatalf("claims = %v, %v", claimPatterns(got), err)
		}
	})

	t.Run("R-REPO-023 two replicas publishing the same new name create one claim and one revision", func(t *testing.T) {
		before := claimsVersion(e, "sandbox")
		var wg sync.WaitGroup
		results := make([]repository.AutoClaimResult, 2)
		errsOut := make([]error, 2)
		for i, rep := range []*replica{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				waitFor(t, "the replica to know the repository", 3*time.Second, func() bool {
					_, err := rep.reader.ByKey(ctx, "sandbox")
					return err == nil
				})
				results[i], errsOut[i] = rep.autoClaim("sandbox", "contested")
			}()
		}
		wg.Wait()
		for _, err := range errsOut {
			if err != nil {
				t.Fatal(err)
			}
		}
		created := 0
		for _, r := range results {
			if r.Created {
				created++
			}
		}
		if created != 1 || claimsVersion(e, "sandbox") != before+1 {
			t.Fatalf("results %+v, claims_version %d -> %d", results, before, claimsVersion(e, "sandbox"))
		}
	})

	t.Run("R-REPO-023 an auto claim is in force on the other replica within 2 seconds", func(t *testing.T) {
		if _, err := a.autoClaim("team-a", "fast-name"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to see the auto claim", 2*time.Second, func() bool {
			m, err := b.routing.ClaimsFor(ctx, "pkgfmt", "fast-name")
			return err == nil && len(m) == 1 && m[0].Source == "auto" && m[0].Repository == "team-a"
		})
	})

	t.Run("R-REPO-023 an auto claim beyond the limit is not created and the publish goes on", func(t *testing.T) {
		a.mustCreate("crowded", "local", "pkgfmt", "")
		e.exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope)
		        SELECT r.id, 'n' || i, 'auto', 'members' FROM repositories r, generate_series(1, $1::int) AS i WHERE r.key = 'crowded'`, repository.MaxClaims)
		res, err := a.autoClaim("crowded", "one-more")
		if err != nil || res.Created || res.Reason != repository.AutoClaimLimit {
			t.Fatalf("result = %+v, %v", res, err)
		}
	})

	t.Run("R-REPO-023 R-REPO-048 the claims of the repository, the exposure set and the covered check", func(t *testing.T) {
		waitFor(t, "B to know the repositories", 3*poll, func() bool {
			r, err := b.reader.ByKey(ctx, "all")
			return err == nil && len(r.Members()) == 3
		})
		teamA, _ := b.reader.ByKey(ctx, "team-a")
		lonely, _ := b.reader.ByKey(ctx, "lonely")
		waitFor(t, "B to see the claims", 3*poll, func() bool {
			c, err := b.routing.CoveredByOwnClaim(ctx, teamA, "@acme/util")
			return err == nil && c
		})
		if c, _ := b.routing.CoveredByOwnClaim(ctx, teamA, "brand-new"); c {
			t.Fatal("covered a name no claim covers")
		}
		set, err := b.routing.ExposureSet(ctx, teamA, "brand-new")
		if err != nil || len(set) != 1 || set[0].Virtual.Key != "all" || set[0].Remote.Key != "remote" {
			t.Fatalf("exposure set = %+v, %v", set, err)
		}
		if set, _ := b.routing.ExposureSet(ctx, lonely, "brand-new"); len(set) != 0 {
			t.Fatalf("a repository in no virtual repository is exposed: %+v", set)
		}
		// A claim in effect in V covers the name: no remote member is consulted.
		if set, _ := b.routing.ExposureSet(ctx, teamA, "@org/tool"); len(set) != 0 {
			t.Fatalf("exposure set under a global claim = %+v", set)
		}
		// A pin rule that names both the local and the remote repository keeps the pair.
		if _, err := a.svc.Patch(ctx, caller, "all", repository.MatchAny(),
			mergePatch(`{"virtual":{"pinning":{"rules":[{"packages":["@both/*"],"members":["team-a","remote"]},{"packages":["@remote-only/*"],"members":["remote"]}]}}}`)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "B to see the pin rules", 3*poll, func() bool {
			set, err := b.routing.ExposureSet(ctx, teamA, "@remote-only/x")
			return err == nil && len(set) == 0
		})
		if set, err := b.routing.ExposureSet(ctx, teamA, "@both/x"); err != nil || len(set) != 1 || set[0].Remote.Key != "remote" {
			t.Fatalf("a pin rule naming both repositories: exposure set = %+v, %v", set, err)
		}
	})
}

func TestClaimsGoWithTheRepository(t *testing.T) {
	e := newEnv(t)
	poll := 300 * time.Millisecond
	w := e.newReplica("gone-w", replicaOpts{role: "worker", poll: poll, catalog: pkgCatalog()})
	a := e.newReplica("gone-a", replicaOpts{poll: poll, catalog: pkgCatalog()})
	ctx := context.Background()
	a.mustCreate("owner", "local", "pkgfmt", "")
	a.mustCreate("remote", "remote", "pkgfmt", "")
	if _, err := a.svc.PutClaims(ctx, caller, "owner", repository.MatchAny(),
		repository.ClaimInput{Claims: []repository.ClaimEntry{{Pattern: "@gone/*", Scope: "global"}}}, allowGlobal); err != nil {
		t.Fatal(err)
	}
	rem, _ := a.reader.ByKey(ctx, "remote")
	waitFor(t, "the claim to be in force on A", 3*poll, func() bool {
		ok, err := a.routing.RemoteRefused(ctx, rem, "@gone/x", nil)
		return err == nil && ok
	})
	res, err := a.svc.Delete(ctx, caller, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	if j := e.waitJob(w.queue, res.JobID); j.State != "succeeded" {
		t.Fatalf("job = %s", j.State)
	}
	t.Run("R-REPO-013 R-REPO-023 the deletion job drops the claims and every replica stops refusing the names", func(t *testing.T) {
		if n := e.scalar(`SELECT count(*) FROM namespace_claims`); n != 0 {
			t.Fatalf("%d claims remain", n)
		}
		waitFor(t, "the refusal to end", 2*time.Second, func() bool {
			ok, err := a.routing.RemoteRefused(ctx, rem, "@gone/x", nil)
			return err == nil && !ok
		})
	})
}
