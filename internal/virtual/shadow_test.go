package virtual_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
)

var ciBot, lead = subjectFor("user-ci-bot"), subjectFor("user-lead")

// shadowWorld: npm-local is in npm-all (with npm-remote) and in npm-ops (with npm-remote2, which
// ci-bot cannot read). ci-bot holds deploy but not manage on npm-local, lead holds both.
func shadowWorld(t *testing.T) *world {
	t.Helper()
	repos := specs("read")
	repos = append(repos, vt.RepoSpec{Key: "npm-ops", Class: "virtual", Members: []string{"npm-local", "npm-remote2"}})
	gs := []vt.Grant{
		{Repos: []string{"npm-all", "npm-local", "npm-remote"}, Principal: "user-ci-bot", Actions: []string{"read"}},
		{Repos: []string{"npm-local"}, Principal: "user-ci-bot", Actions: []string{"deploy"}},
		{Repos: []string{"npm-all", "npm-ops", "npm-local", "npm-remote", "npm-remote2"}, Principal: "user-lead", Actions: []string{"read"}},
		{Repos: []string{"npm-local"}, Principal: "user-lead", Actions: []string{"deploy", "manage"}},
	}
	w := newWorld(t, func(o *worldOpts) { o.grants, o.repos = gs, repos })
	w.router.Exposure = map[string][]repository.Exposure{
		"npm-local": {{Virtual: w.v, Remote: remote}},
	}
	return w
}

func (w *world) shadow(sub authz.Subject, name string) (virtual.ShadowOutcome, error) {
	w.t.Helper()
	idx, err := w.authz.Begin(w.t.Context(), sub, "npm-local", "npm-all")
	if err != nil {
		w.t.Fatal(err)
	}
	return w.r.ShadowCheck(w.t.Context(), virtual.Caller{Subject: sub, Index: idx}, local, name, name)
}

func shadowCount(t *testing.T, w *world, outcome string) float64 {
	return counter(t, w.reg, "trove_shadow_checks_total", map[string]string{"repo": "npm-local", "outcome": outcome})
}

func TestR_REPO_048_Outcomes(t *testing.T) {
	t.Run("R-REPO-048 an empty exposure set proceeds without any upstream request", func(t *testing.T) {
		w := shadowWorld(t)
		w.router.Exposure = nil
		out, err := w.shadow(ciBot, "react")
		if err != nil || !out.Proceed || out.Outcome != virtual.OutcomeNoExposure || len(w.remote.Calls()) != 0 || shadowCount(t, w, "no-exposure") != 1 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 the check does not run when the local repository already holds a version", func(t *testing.T) {
		w := shadowWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		out, err := w.shadow(ciBot, "lodash")
		if err != nil || !out.Proceed || out.Outcome != "" || len(w.remote.Calls()) != 0 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 a package row without a live version does not count as held", func(t *testing.T) {
		w := shadowWorld(t)
		w.rows.Publish(idLocal, "lodash", "1.0.0")
		w.rows.Unpublish(idLocal, "lodash", "1.0.0")
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(ciBot, "lodash")
		if err != nil || out.Proceed {
			t.Fatalf("out %+v err %v", out, err)
		}
	})

	t.Run("R-REPO-048 a manual claim of the local repository that covers the name satisfies the check", func(t *testing.T) {
		w := shadowWorld(t)
		w.router.Covered = map[string]bool{"npm-local/left-pad": true}
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(ciBot, "left-pad")
		if err != nil || !out.Proceed || len(w.remote.Calls()) != 0 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 a remote member serves the name: a deploy holder is refused with 403 and a manage holder proceeds", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(ciBot, "lodash")
		if err != nil || out.Proceed || out.Code != errs.CodeShadowsRemotePackage || out.Outcome != virtual.OutcomeRefused {
			t.Fatalf("ci-bot: out %+v err %v", out, err)
		}
		refusal, _ := errs.As(out.Err("lodash"))
		if refusal == nil || refusal.Kind != errs.Forbidden || refusal.Code != errs.CodeShadowsRemotePackage {
			t.Fatalf("refusal = %v", out.Err("lodash"))
		}
		if !slices.Equal(out.Virtuals, []string{"npm-all"}) {
			t.Fatalf("virtual repositories named: %v", out.Virtuals)
		}
		if refusal.Detail == "" || containsAny(refusal.Detail, "npm-remote") {
			t.Fatalf("detail names a remote member: %q", refusal.Detail)
		}

		out, err = w.shadow(lead, "lodash")
		if err != nil || !out.Proceed || out.Outcome != virtual.OutcomeShadowingApproved || out.Undetermined || out.Err("lodash") != nil {
			t.Fatalf("lead: out %+v err %v", out, err)
		}
		if len(out.Shadowed) != 1 || out.Shadowed[0].Virtual.Key != "npm-all" || out.Shadowed[0].Remote.Key != "npm-remote" {
			t.Fatalf("the audit event lacks the pairs: %+v", out.Shadowed)
		}
	})

	t.Run("R-REPO-048 a stored document that is not gone serves the name without a probe", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.SetState("npm-remote", virtual.EntryState{Stored: true})
		out, err := w.shadow(ciBot, "lodash")
		if err != nil || out.Proceed || w.remote.Count("probe:") != 0 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 a stored document marked gone does not serve the name, and nothing is probed", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.SetState("npm-remote", virtual.EntryState{Stored: true, GoneAt: time.Now()})
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(ciBot, "lodash")
		if err != nil || !out.Proceed || out.Outcome != virtual.OutcomeClear || w.remote.Count("probe:") != 0 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 no remote member serves the name: the publish proceeds after one probe", func(t *testing.T) {
		w := shadowWorld(t)
		out, err := w.shadow(ciBot, "acme-brand-new")
		if err != nil || !out.Proceed || out.Outcome != virtual.OutcomeClear || w.remote.Count("probe:") != 1 || shadowCount(t, w, "clear") != 1 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
	})

	t.Run("R-REPO-048 a negative entry is never read: it neither refuses nor admits", func(t *testing.T) {
		w := shadowWorld(t)
		// A negative entry for the path says "not found" ...
		w.remote.SetState("npm-remote", virtual.EntryState{Negative: true})
		// ... and the probe says the upstream serves it: refused, after one probe.
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(ciBot, "acme-brand-new-3")
		if err != nil || out.Proceed || out.Code != errs.CodeShadowsRemotePackage || w.remote.Count("probe:") != 1 {
			t.Fatalf("out %+v err %v calls %v", out, err, w.remote.Calls())
		}
		// And the reverse: a probe that finds nothing admits although the entry is there.
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.NotServed})
		if out, err = w.shadow(ciBot, "acme-brand-new-3"); err != nil || !out.Proceed {
			t.Fatalf("out %+v err %v", out, err)
		}
	})

	t.Run("R-REPO-048 a circuit that is open is undetermined: 503 with Retry-After for deploy, proceed for manage", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Undetermined, RetryAfter: 20 * time.Second})
		out, err := w.shadow(ciBot, "brand-new-2")
		if err != nil || out.Proceed || out.Code != errs.CodeShadowCheckUnavailable || out.RetryAfter != 20*time.Second || out.Outcome != virtual.OutcomeUnavailable {
			t.Fatalf("ci-bot: out %+v err %v", out, err)
		}
		e, _ := errs.As(out.Err("brand-new-2"))
		if e == nil || e.Kind != errs.Unavailable || e.RetryAfter != 20*time.Second {
			t.Fatalf("refusal = %v", out.Err("brand-new-2"))
		}
		out, err = w.shadow(lead, "brand-new-2")
		if err != nil || !out.Proceed || !out.Undetermined || out.Outcome != virtual.OutcomeUndeterminedAllowed {
			t.Fatalf("lead: out %+v err %v", out, err)
		}
	})

	t.Run("R-REPO-048 without a remaining circuit time Retry-After is 30 seconds", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Undetermined})
		out, _ := w.shadow(ciBot, "x")
		if out.RetryAfter != 30*time.Second {
			t.Fatalf("RetryAfter = %v", out.RetryAfter)
		}
	})

	t.Run("R-REPO-048 a name that one member serves and another cannot answer for is refused, not unavailable", func(t *testing.T) {
		w := shadowWorld(t)
		w.router.Exposure["npm-local"] = []repository.Exposure{{Virtual: w.v, Remote: remote}, {Virtual: w.v, Remote: remote2}}
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		w.remote.SetProbe("npm-remote2", virtual.ProbeResult{Outcome: virtual.Undetermined})
		out, _ := w.shadow(ciBot, "x")
		if out.Code != errs.CodeShadowsRemotePackage {
			t.Fatalf("out %+v", out)
		}
	})

	t.Run("R-REPO-048 a probe that fails in a way nobody classified is undetermined", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.ProbeErr["npm-remote"] = errors.New("boom")
		out, err := w.shadow(ciBot, "x")
		if err != nil || out.Proceed || out.Code != errs.CodeShadowCheckUnavailable {
			t.Fatalf("out %+v err %v", out, err)
		}
	})

	t.Run("R-REPO-048 a replica without the remote engine fails closed", func(t *testing.T) {
		w := shadowWorld(t)
		r, err := virtual.New(virtual.Options{Router: w.router, Decider: w.authz, Locals: w.rows})
		if err != nil {
			t.Fatal(err)
		}
		idx, _ := w.authz.Begin(t.Context(), ciBot, "npm-local")
		out, err := r.ShadowCheck(t.Context(), virtual.Caller{Subject: ciBot, Index: idx}, local, "x", "x")
		if err != nil || out.Proceed || out.Code != errs.CodeShadowCheckUnavailable {
			t.Fatalf("out %+v err %v", out, err)
		}
	})

	t.Run("R-REPO-048 a repository that is not local is not checked", func(t *testing.T) {
		w := shadowWorld(t)
		idx, _ := w.authz.Begin(t.Context(), ciBot, "npm-all")
		out, err := w.r.ShadowCheck(t.Context(), virtual.Caller{Subject: ciBot, Index: idx}, remote, "x", "x")
		if err != nil || !out.Proceed {
			t.Fatalf("out %+v err %v", out, err)
		}
	})
}

func TestR_REPO_048_OneProbePerPublish(t *testing.T) {
	t.Run("R-REPO-048 a remote member in two virtual repositories is probed once", func(t *testing.T) {
		w := shadowWorld(t)
		ops := vt.VirtualRepo(6, "npm-ops", 7200, nil, local, remote)
		w.router.Exposure["npm-local"] = []repository.Exposure{{Virtual: w.v, Remote: remote}, {Virtual: ops, Remote: remote}}
		w.remote.SetProbe("npm-remote", virtual.ProbeResult{Outcome: virtual.Served})
		out, err := w.shadow(lead, "lodash")
		if err != nil || w.remote.Count("probe:") != 1 || w.remote.Count("state:") != 1 {
			t.Fatalf("calls %v err %v", w.remote.Calls(), err)
		}
		// Both pairs reach the audit event, the error names the virtual repositories the caller reads.
		if len(out.Shadowed) != 2 {
			t.Fatalf("pairs = %+v", out.Shadowed)
		}
		refused, _ := w.shadow(ciBot, "lodash")
		if !slices.Equal(refused.Virtuals, []string{"npm-all"}) {
			t.Fatalf("ci-bot is told about %v and may only read npm-all", refused.Virtuals)
		}
	})

	t.Run("R-REPO-048 the probes of one publish run concurrently", func(t *testing.T) {
		w := shadowWorld(t)
		w.router.Exposure["npm-local"] = []repository.Exposure{{Virtual: w.v, Remote: remote}, {Virtual: w.v, Remote: remote2}}
		w.remote.ProbeDelay = 100 * time.Millisecond
		if _, err := w.shadow(lead, "x"); err != nil {
			t.Fatal(err)
		}
		if w.remote.MaxInflight != 2 {
			t.Fatalf("at most %d probes at once", w.remote.MaxInflight)
		}
	})

	t.Run("R-REPO-048 a probe is made for the name's package document and on behalf of the publisher", func(t *testing.T) {
		w := shadowWorld(t)
		var got virtual.RemoteCall
		w.remote.OnProbe = func(rc virtual.RemoteCall) { got = rc }
		idx, _ := w.authz.Begin(t.Context(), ciBot, "npm-local")
		_, err := w.r.ShadowCheck(t.Context(), virtual.Caller{Subject: ciBot, Index: idx}, local, "@acme/util", "@acme/util")
		if err != nil || got.Path != "@acme/util" || got.Package != "@acme/util" || got.MayFill || got.Subject.ID != ciBot.ID {
			t.Fatalf("probe call %+v err %v", got, err)
		}
	})

	t.Run("R-REPO-048 a failure to read the stored state is an error, not a verdict", func(t *testing.T) {
		w := shadowWorld(t)
		w.remote.StateErr["npm-remote"] = errors.New("database down")
		if out, err := w.shadow(lead, "x"); err == nil || out.Proceed {
			t.Fatalf("out %+v err %v", out, err)
		}
	})
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}
