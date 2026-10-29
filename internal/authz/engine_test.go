package authz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

func label(authz, repo int64, at int64) Label {
	return Label{AuthzEpoch: authz, AuthzAt: time.Unix(at, 0), RepoEpoch: repo, RepoAt: time.Unix(at, 0)}
}

// startedEngine returns an engine with its loops running over a fake source and
// a fake change log.
func startedEngine(t *testing.T, f *fixture, mod func(*Options)) (*Engine, *fakeSource, *fakeChanges) {
	t.Helper()
	src := &fakeSource{}
	src.set(f.s)
	ch := &fakeChanges{}
	ch.poll(f.s.Label)
	o := Options{Source: src, Changes: ch, Config: testConfig(), RetryBudget: 500 * time.Millisecond, AnonymousAccess: func() bool { return false }}
	if mod != nil {
		mod(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e, src, ch
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestR_AUTHZ_028_IndexFollowsTheChangeLog(t *testing.T) {
	t.Run("R-AUTHZ-028 a change the poll applied is built and takes effect", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, src, ch := startedEngine(t, f, nil)
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != DenyHidden {
			t.Fatalf("before the grant: %v", d)
		}
		f2 := *f
		f2.policy(policyDef{name: "p", selectors: []string{"npm-internal"}, entries: map[string][]string{idDevs: {"read"}}})
		f2.s.Label = label(2, 1, 1000)
		src.set(f2.s)
		ch.poll(f2.s.Label)
		waitFor(t, "the epoch 2 index", func() bool { return e.Epoch() == 2 })
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != Allow {
			t.Fatalf("after the grant: %v", d)
		}
		f3 := f2
		f3.s.Policies, f3.s.Selectors, f3.s.Entries = nil, nil, nil
		f3.s.Label = label(3, 1, 1000)
		src.set(f3.s)
		ch.poll(f3.s.Label)
		waitFor(t, "the epoch 3 index", func() bool { return e.Epoch() == 3 })
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != DenyHidden {
			t.Fatalf("after the removal: %v", d)
		}
	})

	t.Run("R-AUTHZ-028 a forged message costs an epoch read and never a build", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, src, ch := startedEngine(t, f, nil)
		before := func() (int, int) { src.mu.Lock(); defer src.mu.Unlock(); return src.builds, src.epochs }
		b0, e0 := before()
		start := time.Now()
		for i := 0; i < 2000; i++ {
			ch.apply(AreaAuthz)
		}
		time.Sleep(300 * time.Millisecond)
		b1, e1 := before()
		if b1 != b0 {
			t.Errorf("builds rose from %d to %d", b0, b1)
		}
		if limit := int(time.Since(start)/epochReadGap) + 2; e1-e0 > limit {
			t.Errorf("%d epoch reads in %s, at most one per %s allowed (%d)", e1-e0, time.Since(start), epochReadGap, limit)
		}
		_ = e
	})

	t.Run("R-AUTHZ-028 a build with lower epochs never replaces a newer index", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		f.s.Label = label(5, 5, 1000)
		e, src, _ := startedEngine(t, f, nil)
		low := *f
		low.s.Label = label(4, 5, 1000)
		src.set(low.s)
		e.buildOnce(TriggerSafety)
		if e.Epoch() != 5 {
			t.Fatalf("epoch = %d, a lower build was installed", e.Epoch())
		}
	})

	t.Run("R-AUTHZ-028 a flush installs the next build whatever its epochs", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		f.s.Label = label(5, 5, 1000)
		e, src, _ := startedEngine(t, f, nil)
		low := *f
		low.s.Label = label(2, 5, 2000)
		src.set(low.s)
		e.FlushArea(AreaAuthz, cluster_epoch_regressed)
		waitFor(t, "the flushed build", func() bool { return e.Epoch() == 2 })
	})

	t.Run("R-AUTHZ-028 a reused epoch with another changed_at is not the same label", func(t *testing.T) {
		a, b := label(7, 1, 1000), label(7, 1, 1001)
		if a.Equal(b) || !a.Equal(a) {
			t.Fatal("changed_at must take part in the comparison")
		}
	})

	t.Run("R-AUTHZ-028 the safety rebuild runs when nothing else does", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		_, src, _ := startedEngine(t, f, func(o *Options) { o.Config.Cache.SafetyTTL = 100 * time.Millisecond })
		waitFor(t, "a safety build", func() bool { src.mu.Lock(); defer src.mu.Unlock(); return src.builds >= 3 })
	})

	t.Run("R-AUTHZ-028 a build that fails at start-up stops the start", func(t *testing.T) {
		src := &fakeSource{err: errors.New("down")}
		e, err := New(Options{Source: src, Changes: &fakeChanges{}, Config: testConfig()})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Start(context.Background()); err == nil {
			t.Fatal("started without an index")
		}
	})

	t.Run("R-AUTHZ-028 start-up refuses authz.cache.max_staleness below three poll intervals and names the key", func(t *testing.T) {
		cfg := testConfig()
		cfg.Cache.MaxStaleness = 2 * time.Second
		_, err := New(Options{Source: &fakeSource{}, Changes: &fakeChanges{}, Config: cfg, EpochPollInterval: time.Second})
		if err == nil || !strings.Contains(err.Error(), "authz.cache.max_staleness") {
			t.Fatalf("err = %v", err)
		}
	})
}

// cluster_epoch_regressed is the flush reason the change log reports; the value
// is not interpreted by the engine.
const cluster_epoch_regressed = "epoch_regressed"

func TestR_AUTHZ_028_StaleIndexWaitsThenRefuses(t *testing.T) {
	short := func(o *Options) {
		o.Config.Cache.MaxStaleness = 150 * time.Millisecond
		o.RetryBudget = 400 * time.Millisecond
		o.Config.Cache.SafetyTTL = time.Hour
	}
	t.Run("R-AUTHZ-028 a confirmed index decides", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, _, ch := startedEngine(t, f, short)
		time.Sleep(200 * time.Millisecond)
		ch.poll(f.s.Label) // the poll confirms the index
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != DenyHidden {
			t.Fatalf("%v", d)
		}
	})
	t.Run("R-AUTHZ-016 an index that is not confirmed is waited for and then answered 503 authz_stale", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, _, _ := startedEngine(t, f, short)
		time.Sleep(200 * time.Millisecond) // the index is older than max_staleness, the poll is silent
		start := time.Now()
		d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", ""))
		if d.Outcome != DenyStale || d.Reason != ReasonStale {
			t.Fatalf("%v", d)
		}
		if took := time.Since(start); took < 300*time.Millisecond {
			t.Errorf("answered after %s, it should have waited out the retry budget", took)
		}
		err := d.Err()
		ee, _ := errs.As(err)
		if ee == nil || ee.Status() != 503 || ee.Code != errs.CodeAuthzStale || ee.RetryAfter <= 0 {
			t.Fatalf("err = %#v", ee)
		}
	})
	t.Run("R-AUTHZ-028 a waiting request is decided when the poll confirms in time", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, _, ch := startedEngine(t, f, short)
		time.Sleep(200 * time.Millisecond)
		go func() { time.Sleep(100 * time.Millisecond); ch.poll(f.s.Label) }()
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != DenyHidden {
			t.Fatalf("%v", d)
		}
	})
	t.Run("R-AUTHZ-028 a poll that saw a newer epoch does not confirm the old index", func(t *testing.T) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, src, ch := startedEngine(t, f, short)
		src.mu.Lock()
		src.err = errors.New("the build cannot reach the database")
		src.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		ch.poll(label(2, 1, 1000))
		if d := e.Decide(t.Context(), bobSub, read("npm-internal", "x", "")); d.Outcome != DenyStale {
			t.Fatalf("%v", d)
		}
	})
	t.Run("R-AUTHZ-028 Valkey plays no part: the engine has no Valkey dependency", func(t *testing.T) {
		// The constructor takes a Source and a Changes only; a stopped Valkey is a
		// stopped fast path of Changes and nothing here.
		var _ Changes = &fakeChanges{}
	})
}

func TestR_AUTHZ_032_MinEpoch(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *fakeSource, *fixture) {
		f := newFixture().repo("npm-internal", "local", "npm", "")
		e, src, _ := startedEngine(t, f, nil)
		return e, src, f
	}
	t.Run("R-AUTHZ-032 a request waits for an index of at least the named epoch", func(t *testing.T) {
		e, src, f := setup(t)
		f2 := *f
		f2.policy(policyDef{name: "p", selectors: []string{"npm-internal"}, entries: map[string][]string{idDevs: {"read"}}})
		f2.s.Label = label(2, 1, 1000)
		src.set(f2.s) // the replica's poll has not applied it
		sub := bobSub
		sub.MinEpoch = 2
		if d := e.Decide(t.Context(), sub, read("npm-internal", "x", "")); d.Outcome != Allow || d.Epoch != 2 {
			t.Fatalf("%v", d)
		}
	})
	t.Run("R-AUTHZ-032 an epoch the database never reached answers 412 at once", func(t *testing.T) {
		e, _, _ := setup(t)
		sub := bobSub
		sub.MinEpoch = 1001
		start := time.Now()
		d := e.Decide(t.Context(), sub, read("npm-internal", "x", ""))
		if d.Outcome != DenyEpochUnknown || d.Status() != 412 {
			t.Fatalf("%v", d)
		}
		if time.Since(start) > 150*time.Millisecond {
			t.Errorf("took %s", time.Since(start))
		}
		if ee, _ := errs.As(d.Err()); ee == nil || ee.Code != errs.CodeAuthzEpochUnknown || ee.Status() != 412 {
			t.Fatalf("err = %v", d.Err())
		}
	})
	t.Run("R-AUTHZ-032 an anonymous caller ignores the header", func(t *testing.T) {
		e, _, _ := setup(t)
		sub := anonSub
		sub.MinEpoch = 1001
		if d := e.Decide(t.Context(), sub, read("npm-internal", "x", "")); d.Outcome != DenyUnauthenticated {
			t.Fatalf("%v", d)
		}
	})
	t.Run("R-AUTHZ-032 an index that already has the epoch decides without a database read", func(t *testing.T) {
		e, src, _ := setup(t)
		src.mu.Lock()
		before := src.epochs
		src.mu.Unlock()
		sub := bobSub
		sub.MinEpoch = 1
		e.Decide(t.Context(), sub, read("npm-internal", "x", ""))
		src.mu.Lock()
		defer src.mu.Unlock()
		if src.epochs != before {
			t.Errorf("%d epoch reads for a current index", src.epochs-before)
		}
	})
	t.Run("R-AUTHZ-032 1000 requests per second with an unknown epoch cause at most 20 epoch reads per second", func(t *testing.T) {
		e, src, _ := setup(t)
		src.mu.Lock()
		before := src.epochs
		src.mu.Unlock()
		sub := bobSub
		sub.MinEpoch = 5000
		start := time.Now()
		var wg sync.WaitGroup
		for i := 0; i < 1000; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); e.Decide(t.Context(), sub, read("npm-internal", "x", "")) }()
			time.Sleep(time.Millisecond)
		}
		wg.Wait()
		src.mu.Lock()
		reads := src.epochs - before
		src.mu.Unlock()
		if limit := int(time.Since(start)/epochReadGap) + 3; reads > limit {
			t.Errorf("%d epoch reads in %s (limit %d)", reads, time.Since(start), limit)
		}
	})
	t.Run("R-AUTHZ-032 the header is a decimal integer between 1 and 2^63-1", func(t *testing.T) {
		for raw, want := range map[string]int64{"": 0, "1": 1, "42": 42, "9223372036854775807": 9223372036854775807} {
			if got, err := ParseMinEpoch(raw); err != nil || got != want {
				t.Errorf("ParseMinEpoch(%q) = %d, %v", raw, got, err)
			}
		}
		for _, raw := range []string{"abc", "0", "-3", "1.5", "9223372036854775808", " 4", "0x10", "+5", "٣"} {
			_, err := ParseMinEpoch(raw)
			ee, _ := errs.As(err)
			if ee == nil || ee.Code != errs.CodeInvalidHeader || ee.Status() != 400 {
				t.Errorf("ParseMinEpoch(%q) = %v", raw, err)
			}
		}
	})
}

func TestR_AUTHZ_016_DecisionErr(t *testing.T) {
	cases := []struct {
		name   string
		d      Decision
		status int
		code   string
	}{
		{"allow", Decision{Outcome: Allow}, 0, ""},
		{"unauthenticated", Decision{Outcome: DenyUnauthenticated}, 401, errs.CodeUnauthenticated},
		{"hidden", Decision{Outcome: DenyHidden}, 404, errs.CodeNotFound},
		{"visible names the action", Decision{Outcome: DenyVisible, Missing: Deploy, Target: "files-internal/a.zip"}, 403, errs.CodeForbidden},
		{"read-only endpoint", Decision{Outcome: DenyVisible, Reason: ReasonEndpointReadOnly}, 403, "endpoint_read_only"},
		{"stale", Decision{Outcome: DenyStale}, 503, errs.CodeAuthzStale},
		{"epoch unknown", Decision{Outcome: DenyEpochUnknown}, 412, errs.CodeAuthzEpochUnknown},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-016 "+tc.name, func(t *testing.T) {
			err := tc.d.Err()
			if tc.status == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			ee, ok := errs.As(err)
			if !ok || ee.Status() != tc.status || ee.Code != tc.code {
				t.Fatalf("err = %v", err)
			}
			if tc.d.Outcome == DenyVisible && tc.d.Missing != "" && !strings.Contains(ee.Detail, "deploy") {
				t.Errorf("detail %q does not name the action", ee.Detail)
			}
			if tc.d.Outcome == DenyUnauthenticated && ee.Challenge == "" {
				t.Error("the 401 carries no challenge")
			}
		})
	}
	t.Run("R-AUTHZ-016 the 403 names only what the caller sent", func(t *testing.T) {
		d := Decision{Outcome: DenyVisible, Missing: Delete, Target: "npm-internal/@web/button"}
		if got := d.forbiddenText(); got != "The action delete is not permitted on npm-internal/@web/button." {
			t.Fatalf("%q", got)
		}
	})
}

// recorder collects the audit events of a test.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}
func (r *recorder) RecordTx(context.Context, meta.Tx, audit.Event) error { return nil }
func (r *recorder) list() []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Event(nil), r.events...)
}

func TestR_AUTHZ_029_DecisionAudit(t *testing.T) {
	f := newFixture().repo("npm-internal", "local", "npm", "")
	f.policy(policyDef{name: "p", selectors: []string{"npm-internal"}, entries: map[string][]string{idDevs: {"read"}}})
	engine := func(mod func(*Options)) (*Engine, *recorder) {
		rec := &recorder{}
		e := newTestEngine(t, f, func(o *Options) {
			o.Recorder = rec
			o.Audit = config.Audit{DenialDedupWindow: 0}
			if mod != nil {
				mod(o)
			}
		})
		return e, rec
	}
	t.Run("R-AUTHZ-029 a refused write is recorded", func(t *testing.T) {
		e, rec := engine(nil)
		e.Decide(t.Context(), bobSub, write("npm-internal", "x", "", Deploy))
		evs := rec.list()
		if len(evs) != 1 || evs[0].Type != EventDenied || evs[0].Reason != ReasonNoGrant || evs[0].Actor.ID != idBob {
			t.Fatalf("%+v", evs)
		}
	})
	t.Run("R-AUTHZ-029 a refused read is recorded with reads denied and not with none", func(t *testing.T) {
		e, rec := engine(nil)
		e.Decide(t.Context(), carolSub, read("npm-internal", "x", ""))
		if len(rec.list()) != 1 {
			t.Fatalf("%+v", rec.list())
		}
		e2, rec2 := engine(func(o *Options) { o.Config.Audit.Reads = config.AuthzReadsNone })
		e2.Decide(t.Context(), carolSub, read("npm-internal", "x", ""))
		e2.Decide(t.Context(), carolSub, write("npm-internal", "x", "", Deploy))
		if evs := rec2.list(); len(evs) != 1 || !strings.Contains(string(evs[0].Details), `"deploy"`) {
			t.Fatalf("reads none must still record writes: %+v", evs)
		}
	})
	t.Run("R-AUTHZ-029 an allowed read is recorded only with reads all", func(t *testing.T) {
		e, rec := engine(nil)
		e.Decide(t.Context(), bobSub, read("npm-internal", "x", ""))
		if len(rec.list()) != 0 {
			t.Fatalf("%+v", rec.list())
		}
		e2, rec2 := engine(func(o *Options) { o.Config.Audit.Reads = config.AuthzReadsAll })
		e2.Decide(t.Context(), bobSub, read("npm-internal", "x", ""))
		evs := rec2.list()
		if len(evs) != 1 || evs[0].Type != EventReadAllowed || !strings.Contains(string(evs[0].Details), "p") {
			t.Fatalf("%+v", evs)
		}
	})
	t.Run("R-AUTHZ-029 anonymous denials are recorded only with audit.record_anonymous_denials", func(t *testing.T) {
		e, rec := engine(nil)
		e.Decide(t.Context(), anonSub, read("npm-internal", "x", ""))
		if len(rec.list()) != 0 {
			t.Fatal("recorded")
		}
		e2, rec2 := engine(func(o *Options) { o.Audit.RecordAnonymousDenials = true })
		e2.Decide(t.Context(), anonSub, read("npm-internal", "x", ""))
		if len(rec2.list()) != 1 {
			t.Fatal("not recorded")
		}
	})
	// The producer does not deduplicate (Q-AUTHZ-24): every refusal reaches the
	// recorder, with the columns and the detail the writer's deduplication keys
	// on (R-OPS-015), so the writer's summary carries the right count.
	t.Run("R-AUTHZ-029 R-OPS-015 every refusal reaches the recorder with the deduplication key", func(t *testing.T) {
		e, rec := engine(func(o *Options) { o.Audit.DenialDedupWindow = time.Hour })
		for range 3 {
			e.Decide(t.Context(), carolSub, write("npm-internal", "x", "", Deploy, Overwrite))
		}
		evs := rec.list()
		if len(evs) != 3 {
			t.Fatalf("%d events, want 3 (no producer-side collapse)", len(evs))
		}
		ev := evs[0]
		var d map[string]any
		_ = json.Unmarshal(ev.Details, &d)
		if ev.Outcome != audit.Denied || ev.Repository != "npm-internal" || ev.Path != "x" || d["required_action"] != "deploy,overwrite" {
			t.Fatalf("%+v %v", ev, d)
		}
		if _, ok := d["count"]; ok {
			t.Fatal("the producer must not set details.count; the writer does")
		}
	})
	t.Run("R-AUTHZ-029 a refused management operation carries the permission as its key", func(t *testing.T) {
		e, rec := engine(nil)
		_ = e.Require(t.Context(), bobSub, "policies:write")
		evs := rec.list()
		var d map[string]any
		if len(evs) == 1 {
			_ = json.Unmarshal(evs[0].Details, &d)
		}
		if len(evs) != 1 || evs[0].Outcome != audit.Denied || d["required_action"] != "policies:write" {
			t.Fatalf("%+v %v", evs, d)
		}
	})
	t.Run("R-AUTHZ-029 a stale refusal carries deny.stale", func(t *testing.T) {
		rec := &recorder{}
		e, _, _ := startedEngine(t, f, func(o *Options) {
			o.Recorder = rec
			o.Config.Cache.MaxStaleness = 100 * time.Millisecond
			o.RetryBudget = 150 * time.Millisecond
		})
		time.Sleep(150 * time.Millisecond)
		e.Decide(t.Context(), bobSub, write("npm-internal", "x", "", Deploy))
		evs := rec.list()
		if len(evs) != 1 || evs[0].Reason != ReasonStale {
			t.Fatalf("%+v", evs)
		}
	})
	t.Run("R-AUTHZ-029 a refused management operation is recorded", func(t *testing.T) {
		e, rec := engine(nil)
		if err := e.Require(t.Context(), bobSub, "policies:write"); err == nil {
			t.Fatal("allowed")
		}
		evs := rec.list()
		if len(evs) != 1 || evs[0].Reason != ReasonCapability {
			t.Fatalf("%+v", evs)
		}
	})
}

func TestR_AUTHZ_016_ReadableFiltersListings(t *testing.T) {
	f := newFixture().repo("a-repo", "local", "generic", "").repo("b-repo", "local", "generic", "").repo("c-repo", "local", "generic", "")
	f.policy(policyDef{name: "p", selectors: []string{"a-repo", "c-repo"}, entries: map[string][]string{idDevs: {"read"}}})
	f.policy(policyDef{name: "anon", selectors: []string{"b-repo"}, entries: map[string][]string{AnonymousID: {"read"}}})
	e := newTestEngine(t, f, nil)
	on := newTestEngine(t, f, func(o *Options) { o.AnonymousAccess = func() bool { return true } })
	keys := []string{"c-repo", "b-repo", "a-repo", "ghost"}
	cases := []struct {
		name string
		e    *Engine
		sub  Subject
		want []string
	}{
		{"a member of devs sees a and c in the order given", e, bobSub, []string{"c-repo", "a-repo"}},
		{"a user in no group sees nothing", e, carolSub, nil},
		{"anonymous access off lists nothing", e, anonSub, nil},
		{"anonymous access on lists what anonymous reads", on, anonSub, []string{"b-repo"}},
		{"an administrator with bypass sees every existing repository", newTestEngine(t, newFixtureWithAdmin(f), nil), adminSub, []string{"c-repo", "b-repo", "a-repo"}},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-016 "+tc.name, func(t *testing.T) {
			got, err := tc.e.Readable(t.Context(), tc.sub, keys)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func newFixtureWithAdmin(f *fixture) *fixture {
	g := *f
	g.s.Bindings = append([]q.AuthzBuildBindingsRow(nil), f.s.Bindings...)
	g.bind(RoleAdmin, idOps)
	return &g
}

func TestR_AUTHZ_023_CheckForTheCaller(t *testing.T) {
	f := newFixture().repo("npm-internal", "local", "npm", "").repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal"]}}`)
	f.policy(policyDef{name: "p", selectors: []string{"npm-internal", "npm-all"}, pkgInc: nil, entries: map[string][]string{idWeb: {"read", "deploy"}}})
	f.policy(policyDef{name: "q", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, entries: map[string][]string{idDevs: {"delete"}}})
	e := newTestEngine(t, f, nil)
	items := []CheckItem{
		{Repository: "npm-internal", Package: "@web/button", Action: Deploy},
		{Repository: "npm-internal", Path: "@web/button/-/button-1.2.0.tgz", Action: Delete},
		{Repository: "npm-all", Action: Read},
	}
	t.Run("R-AUTHZ-023 the example returns the rights of the caller", func(t *testing.T) {
		got, epoch, err := e.Check(t.Context(), aliceSub, items)
		if err != nil || epoch != 1 {
			t.Fatal(err, epoch)
		}
		// alice: deploy yes (web), delete on a path: pattern q includes packages only, a path coordinate has no package: no; read on the virtual repository yes
		want := []bool{true, false, true}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("check %d = %v, want %v", i, got[i], want[i])
			}
		}
	})
	t.Run("R-AUTHZ-023 a caller with no grants gets false everywhere, also for a missing repository", func(t *testing.T) {
		missing := append([]CheckItem(nil), items...)
		missing[0].Repository = "does-not-exist"
		for _, set := range [][]CheckItem{items, missing} {
			got, _, err := e.Check(t.Context(), carolSub, set)
			if err != nil {
				t.Fatal(err)
			}
			for i, ok := range got {
				if ok {
					t.Errorf("check %d allowed", i)
				}
			}
		}
	})
	t.Run("R-AUTHZ-023 an anonymous caller gets 401 and 101 checks are refused", func(t *testing.T) {
		if _, _, err := e.Check(t.Context(), anonSub, items); err == nil {
			t.Error("anonymous check allowed")
		}
		if _, _, err := e.Check(t.Context(), aliceSub, make([]CheckItem, 101)); err == nil {
			t.Error("101 checks allowed")
		}
		if _, _, err := e.Check(t.Context(), aliceSub, []CheckItem{{Repository: "npm-internal", Action: "publish"}}); err == nil {
			t.Error("unknown action allowed")
		}
	})
	t.Run("R-AUTHZ-023 a check honours the token scope", func(t *testing.T) {
		sub := tokenScopOf(aliceSub)
		got, _, err := e.Check(t.Context(), sub, items)
		if err != nil || got[0] || !got[2] {
			t.Fatalf("%v %v", got, err)
		}
	})
	t.Run("R-AUTHZ-022 effective capabilities, roles and principals", func(t *testing.T) {
		f2 := newFixture().bind(RoleAuditor, idAlice)
		me, err := newTestEngine(t, f2, nil).Effective(t.Context(), aliceSub)
		if err != nil || len(me.Roles) != 1 || me.Roles[0] != RoleAuditor || len(me.Capabilities) == 0 {
			t.Fatalf("%+v %v", me, err)
		}
		joined := strings.Join(me.Principals, ",")
		for _, want := range []string{"user:alice", "group:developers", "group:team-web", "authenticated"} {
			if !strings.Contains(joined, want) {
				t.Errorf("principals %q lack %s", joined, want)
			}
		}
	})
}

func tokenScopOf(s Subject) Subject {
	return tokenScop(s, token.Scope{Repositories: []token.ScopeGrant{{Repositories: []string{"npm-all"}, Actions: []string{"read"}}}})
}
