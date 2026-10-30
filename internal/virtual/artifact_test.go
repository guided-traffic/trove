package virtual_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

const tgz = "left-pad/-/1.0.0.tgz"

func TestR_REPO_019_ArtifactTiers(t *testing.T) {
	t.Run("R-REPO-019 local members form the first tier, whatever the order of the member list", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{remote, local} })
		vt.Put(w.arts.LocalItems, "npm-local", tgz, "local-bytes")
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "remote-bytes")
		rec, err := w.artifact(alice, "left-pad", "1.0.0")
		if err != nil || rec.Body.String() != "local-bytes" {
			t.Fatalf("body %q err %v", rec.Body.String(), err)
		}
		if got := rec.Header().Get(virtual.ResolvedFromHeader); got != "npm-local" {
			t.Fatalf("Trove-Resolved-From = %q", got)
		}
		if slices.Contains(w.arts.Calls(), "fetch:npm-remote") || slices.Contains(w.arts.Calls(), "cached:npm-remote") {
			t.Fatalf("a lower tier was asked: %v", w.arts.Calls())
		}
		if n := counter(t, w.reg, "trove_virtual_resolutions_total", map[string]string{"repo": "npm-all", "source": "local"}); n != 1 {
			t.Fatalf("trove_virtual_resolutions_total{local} = %v", n)
		}
	})

	t.Run("R-REPO-019 the cache of a remote member is the second tier and is served without its upstream", func(t *testing.T) {
		w := newWorld(t)
		vt.Put(w.arts.CachedItems, "npm-remote", tgz, "cached-bytes")
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "upstream-bytes")
		rec, err := w.artifact(alice, "left-pad", "1.0.0")
		if err != nil || rec.Body.String() != "cached-bytes" || rec.Header().Get(virtual.ResolvedFromHeader) != "npm-remote" {
			t.Fatalf("body %q err %v headers %v", rec.Body.String(), err, rec.Header())
		}
		if slices.Contains(w.arts.Calls(), "fetch:npm-remote") {
			t.Fatalf("the upstream was asked: %v", w.arts.Calls())
		}
	})

	t.Run("R-REPO-019 the upstream of a remote member is the third tier", func(t *testing.T) {
		w := newWorld(t)
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "upstream-bytes")
		rec, err := w.artifact(alice, "left-pad", "1.0.0")
		if err != nil || rec.Body.String() != "upstream-bytes" || rec.Header().Get(virtual.ResolvedFromHeader) != "npm-remote" {
			t.Fatalf("body %q err %v", rec.Body.String(), err)
		}
		if n := counter(t, w.reg, "trove_virtual_resolutions_total", map[string]string{"source": "upstream"}); n != 1 {
			t.Fatalf("source upstream = %v", n)
		}
	})

	t.Run("R-REPO-019 no Trove-Resolved-From on an endpoint with exposure rules", func(t *testing.T) {
		w := newWorld(t)
		vt.Put(w.arts.LocalItems, "npm-local", tgz, "x")
		vt.Put(w.arts.UpstreamItems, "npm-remote", "right-pad/-/1.0.0.tgz", "y")
		c, p := w.plan(alice, artifactOf("left-pad", "1.0.0"))
		c.Rules = true
		rec := httptest.NewRecorder()
		if err := w.r.Artifact(context.Background(), c, p, w.arts, rec); err != nil {
			t.Fatal(err)
		}
		c, p = w.plan(alice, artifactOf("right-pad", "1.0.0"))
		c.Rules = true
		rec2 := httptest.NewRecorder()
		if err := w.r.Artifact(context.Background(), c, p, w.arts, rec2); err != nil {
			t.Fatal(err)
		}
		if rec.Header().Get(virtual.ResolvedFromHeader) != "" || rec2.Header().Get(virtual.ResolvedFromHeader) != "" {
			t.Fatalf("member named: %v %v", rec.Header(), rec2.Header())
		}
	})
}

func TestR_REPO_019_NotFoundAndFailure(t *testing.T) {
	t.Run("R-REPO-019 every consulted member said not found: 404", func(t *testing.T) {
		w := newWorld(t)
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, code := kind(err); k != errs.NotFound || code != errs.CodeNotFound {
			t.Fatalf("err = %v", err)
		}
		if n := counter(t, w.reg, "trove_virtual_resolutions_total", map[string]string{"source": "none"}); n != 1 {
			t.Fatalf("source none = %v", n)
		}
	})

	t.Run("R-REPO-019 the upstream is down and nothing is cached: 502, not 404", func(t *testing.T) {
		w := newWorld(t)
		w.arts.FetchErr["npm-remote"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, code := kind(err); k != errs.UpstreamFailed || code != errs.CodeUpstreamFailed {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-019 a timeout answers 504 and an unavailable member 503: the failure of the first member that failed", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{local, remote, remote2} })
		w.arts.FetchErr["npm-remote"] = errs.E(errs.UpstreamTimeout, errs.CodeUpstreamTimeout, "slow")
		w.arts.FetchErr["npm-remote2"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.UpstreamTimeout {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-019 a later member still serves after an earlier one failed", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{local, remote, remote2} })
		w.arts.FetchErr["npm-remote"] = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "down")
		vt.Put(w.arts.UpstreamItems, "npm-remote2", tgz, "second")
		rec, err := w.artifact(alice, "left-pad", "1.0.0")
		if err != nil || rec.Body.String() != "second" || rec.Header().Get(virtual.ResolvedFromHeader) != "npm-remote2" {
			t.Fatalf("body %q err %v headers %v", rec.Body.String(), err, rec.Header())
		}
	})

	t.Run("R-REPO-019 a failure after the first byte ends the request and names no other member", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{local, remote, remote2} })
		w.arts.FailAfterBytes["npm-remote"] = errors.New("connection reset")
		vt.Put(w.arts.UpstreamItems, "npm-remote2", tgz, "second")
		rec, err := w.artifact(alice, "left-pad", "1.0.0")
		if err == nil || rec.Body.String() != "par" {
			t.Fatalf("body %q err %v", rec.Body.String(), err)
		}
		if slices.Contains(w.arts.Calls(), "fetch:npm-remote2") {
			t.Fatalf("a second member continued the response: %v", w.arts.Calls())
		}
	})

	t.Run("R-REPO-019 a refusal to populate counts as a miss", func(t *testing.T) {
		w := newWorld(t)
		w.arts.FetchErr["npm-remote"] = errs.E(errs.Forbidden, errs.CodePopulateNotPermitted, "no")
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestR_REPO_019_StepsOneToFive(t *testing.T) {
	t.Run("R-REPO-019 step 1 the patterns of the virtual repository refuse: 404 and no member is asked", func(t *testing.T) {
		w := newWorld(t)
		w.router.NotAdmitted = true
		vt.Put(w.arts.LocalItems, "npm-local", tgz, "x")
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.NotFound || len(w.arts.Calls()) != 0 {
			t.Fatalf("err = %v calls %v", err, w.arts.Calls())
		}
		if _, err := w.metadata(alice, "left-pad"); err == nil {
			t.Fatal("metadata answered")
		}
	})

	t.Run("R-REPO-019 step 1 a disabled virtual repository answers 404", func(t *testing.T) {
		w := newWorld(t)
		w.v.Disabled = true
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-REPO-019 step 3 a claim that leaves no member answers 404 without asking a remote member", func(t *testing.T) {
		w := newWorld(t)
		w.router.Dropped = map[string]string{"npm-local": repository.DropClaimedElsewhere, "npm-remote": repository.DropClaimedElsewhere}
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "x")
		_, err := w.artifact(alice, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.NotFound || len(w.arts.Calls()) != 0 || w.remote.Count("") != 0 {
			t.Fatalf("err = %v arts %v remote %v", err, w.arts.Calls(), w.remote.Calls())
		}
	})

	t.Run("R-REPO-019 step 4 a member the caller cannot read is skipped silently and counted", func(t *testing.T) {
		w := newWorld(t)
		vt.Put(w.arts.LocalItems, "npm-local", tgz, "internal")
		// Bob reads npm-all and npm-local only: the remote member is unreadable for him.
		vt.Put(w.arts.UpstreamItems, "npm-remote", "only-upstream/-/1.0.0.tgz", "public")
		c, p := w.plan(bob, artifactOf("only-upstream", "1.0.0"))
		if p.Unreadable != 1 || len(p.Members) != 1 || p.Members[0].Repo.Key != "npm-local" {
			t.Fatalf("plan: unreadable %d members %v", p.Unreadable, p.Members)
		}
		rec := httptest.NewRecorder()
		err := w.r.Artifact(context.Background(), c, p, w.arts, rec)
		if k, _ := kind(err); k != errs.NotFound {
			t.Fatalf("err = %v, want 404 and not 403", err)
		}
		if slices.Contains(w.arts.Calls(), "cached:npm-remote") || slices.Contains(w.arts.Calls(), "fetch:npm-remote") {
			t.Fatalf("the unreadable member was consulted: %v", w.arts.Calls())
		}
	})

	t.Run("R-REPO-019 step 5 the order of the plan is the order of the route", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.members = []repository.Repository{remote, local} })
		_, p := w.plan(alice, artifactOf("left-pad", "1.0.0"))
		if len(p.Members) != 2 || p.Members[0].Repo.Key != "npm-remote" || p.Members[0].Position != 0 || p.Members[1].Position != 1 {
			t.Fatalf("members %+v", p.Members)
		}
	})
}

func TestR_AUTHZ_009_PerMemberDecisions(t *testing.T) {
	t.Run("R-AUTHZ-009 rule 3 a remote member the caller may not fill is a miss for a package that is not cached", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fill = "deploy" })
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "x")
		_, err := w.artifact(alice, "left-pad", "1.0.0") // read only: may not fill a frozen mirror
		if k, _ := kind(err); k != errs.NotFound {
			t.Fatalf("err = %v", err)
		}
		if slices.Contains(w.arts.Calls(), "fetch:npm-remote") {
			t.Fatalf("a caller without the cache-fill right reached the upstream: %v", w.arts.Calls())
		}
		_, p := w.plan(alice, artifactOf("left-pad", "1.0.0"))
		if p.Members[1].MayFill {
			t.Fatal("MayFill for a caller with read only")
		}
	})

	t.Run("R-AUTHZ-009 rule 3 the cache-fill right is checked on the remote member with its cacheFillAction", func(t *testing.T) {
		w := newWorld(t, func(o *worldOpts) { o.fill = "deploy" })
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "fetched")
		rec, err := w.artifact(carol, "left-pad", "1.0.0") // carol holds deploy on the mirror
		if err != nil || rec.Body.String() != "fetched" {
			t.Fatalf("body %q err %v", rec.Body.String(), err)
		}
	})

	t.Run("R-AUTHZ-009 rule 3 an endpoint that does not fill caches makes every member a miss", func(t *testing.T) {
		w := newWorld(t)
		sub := alice
		sub.Endpoint, sub.CacheFill = "customers", false
		vt.Put(w.arts.UpstreamItems, "npm-remote", tgz, "x")
		_, err := w.artifact(sub, "left-pad", "1.0.0")
		if k, _ := kind(err); k != errs.NotFound || slices.Contains(w.arts.Calls(), "fetch:npm-remote") {
			t.Fatalf("err = %v calls %v", err, w.arts.Calls())
		}
	})

	t.Run("R-AUTHZ-009 rule 5 a write is decided on the deploy target and the deploy target is the one of the repository", func(t *testing.T) {
		w := newWorld(t)
		tgt, err := w.r.DeployTarget(w.v)
		if err != nil || tgt.Key != "npm-local" {
			t.Fatalf("target %v err %v", tgt.Key, err)
		}
		// Alice may deploy into npm-local and read npm-all: the write through npm-all is allowed;
		// a grant of deploy on npm-all itself would not matter (checked in the decision point).
		e := vt.NewDecider(t, specs("read"), append(grants(), vt.Grant{Repos: []string{"npm-local"}, Principal: vt.Alice, Actions: []string{"deploy"}},
			vt.Grant{Repos: []string{"npm-all"}, Principal: vt.Bob, Actions: []string{"deploy"}}))
		write := authz.Coordinate{Repository: "npm-all", Class: repository.Virtual, Format: vt.Format, Kind: "write", Package: "p", Actions: []authz.Action{authz.Deploy}}
		aIdx, _ := e.Begin(context.Background(), alice, "npm-all")
		if d := e.DecideOn(context.Background(), aIdx, alice, write); d.Outcome != authz.Allow {
			t.Fatalf("alice: %+v", d)
		}
		bIdx, _ := e.Begin(context.Background(), bob, "npm-all")
		if d := e.DecideOn(context.Background(), bIdx, bob, write); d.Outcome == authz.Allow {
			t.Fatalf("bob holds deploy on the virtual repository only, which has no effect: %+v", d)
		}
	})

	t.Run("R-AUTHZ-009 rule 5 a virtual repository without a deploy target is read-only", func(t *testing.T) {
		w := newWorld(t)
		v := vt.VirtualRepo(5, "npm-all", 7200, nil, local, remote)
		_, err := w.r.DeployTarget(v)
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.MethodNotAllowed || e.Code != errs.CodeNoDeployTarget || !slices.Equal(e.Allow, []string{"GET", "HEAD"}) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-AUTHZ-009 rule 5 a disabled deploy target answers 404", func(t *testing.T) {
		w := newWorld(t)
		off := local
		off.Disabled = true
		v := vt.VirtualRepo(5, "npm-all", 7200, &off, local, remote)
		if _, err := w.r.DeployTarget(v); err == nil {
			t.Fatal("a disabled target took a deploy")
		}
	})

	t.Run("R-AUTHZ-009 rule 6 an anonymous caller needs read on every member, else 401", func(t *testing.T) {
		everything := []string{"npm-all", "npm-local", "npm-remote"}
		anon := func(repos ...string) *authz.Engine {
			return vt.NewDecider(t, specs("read"), []vt.Grant{{Repos: repos, Principal: authz.AnonymousID, Actions: []string{"read"}}})
		}
		read := authz.Coordinate{Repository: "npm-all", Class: repository.Virtual, Format: vt.Format, Kind: "metadata", Package: "p", Actions: []authz.Action{authz.Read}}
		for name, tc := range map[string]struct {
			repos []string
			want  authz.Outcome
		}{
			"all members readable":      {everything, authz.Allow},
			"one member not readable":   {[]string{"npm-all", "npm-local"}, authz.DenyUnauthenticated},
			"no gate on the repository": {[]string{"npm-local", "npm-remote"}, authz.DenyUnauthenticated},
		} {
			e := anon(tc.repos...)
			idx, err := e.Begin(context.Background(), anonymousSubject(), "npm-all")
			if err != nil {
				t.Fatal(err)
			}
			if d := e.DecideOn(context.Background(), idx, anonymousSubject(), read); d.Outcome != tc.want {
				t.Errorf("%s: outcome %d reason %q, want %d", name, d.Outcome, d.Reason, tc.want)
			}
		}
	})

	t.Run("R-AUTHZ-009 rule 7 a virtual repository is listed only for a subject that holds read on it", func(t *testing.T) {
		w := newWorld(t)
		e := vt.NewDecider(t, specs("read"), []vt.Grant{
			{Repos: []string{"npm-all", "npm-local"}, Principal: vt.Alice, Actions: []string{"read"}},
			{Repos: []string{"npm-local", "npm-remote"}, Principal: vt.Bob, Actions: []string{"read"}},
		})
		keys := []string{"npm-all", "npm-local", "npm-remote"}
		for sub, want := range map[string][]string{vt.Alice: {"npm-all", "npm-local"}, vt.Bob: {"npm-local", "npm-remote"}} {
			s := subjectFor(sub)
			idx, _ := e.Begin(context.Background(), s, keys...)
			got, err := e.ReadableOn(context.Background(), idx, s, keys)
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("%s lists %v (%v), want %v", sub, got, err, want)
			}
		}
		_ = w
	})

	t.Run("R-AUTHZ-009 rule 8 content that exists only in an unreadable member answers exactly like a missing package", func(t *testing.T) {
		w := newWorld(t)
		// npm-remote holds the package; bob cannot read npm-remote.
		vt.Put(w.arts.CachedItems, "npm-remote", tgz, "secret")
		w.remote.SetDoc("npm-remote", "left-pad", []string{"1.0.0"}, future(w, 1e9), false)
		_, hiddenErr := w.artifact(bob, "left-pad", "1.0.0")
		_, missingErr := w.artifact(bob, "no-such", "1.0.0")
		he, _ := errs.As(hiddenErr)
		me, _ := errs.As(missingErr)
		if he == nil || me == nil || he.Kind != me.Kind || he.Code != me.Code || he.Detail != me.Detail {
			t.Fatalf("hidden %v, missing %v", hiddenErr, missingErr)
		}
		_, hiddenMeta := w.metadata(bob, "left-pad")
		_, missingMeta := w.metadata(bob, "no-such")
		he, _ = errs.As(hiddenMeta)
		me, _ = errs.As(missingMeta)
		if he == nil || me == nil || he.Kind != errs.NotFound || he.Kind != me.Kind || he.Code != me.Code || he.Detail != me.Detail {
			t.Fatalf("metadata: hidden %v, missing %v", hiddenMeta, missingMeta)
		}
	})
}

func TestDeployTargetOfTheResolver(t *testing.T) {
	w := newWorld(t)
	tgt, err := w.r.DeployTarget(w.v)
	if err != nil || tgt.Class != repository.Local {
		t.Fatalf("target %+v err %v", tgt, err)
	}
	_ = http.StatusOK
}

func TestNewRefusesAnEngineWithoutItsParts(t *testing.T) {
	for name, o := range map[string]virtual.Options{
		"no router":  {Decider: vt.NewDecider(t, specs("read"), nil), Locals: vt.NewRows()},
		"no decider": {Router: &vt.Router{}, Locals: vt.NewRows()},
		"no locals":  {Router: &vt.Router{}, Decider: vt.NewDecider(t, specs("read"), nil)},
	} {
		if _, err := virtual.New(o); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}
