package authz

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
)

// The tests of the per-request contract (Decider, Q-AUTHZ-159) and of the
// endpoint the subject carries (Q-EP-20 (b)).

var _ Decider = (*Engine)(nil)

// swappedIndex installs a second index in e, in which alice lost her grant, and
// returns the index a request that began before the swap still holds.
func swappedIndex(t *testing.T) (e *Engine, before *Index) {
	t.Helper()
	granted := newFixture().repo("npm-internal", "local", "npm", "")
	granted.policy(policyDef{name: "p", selectors: []string{"npm-internal"}, entries: map[string][]string{idAlice: {"read"}}})
	e = newTestEngine(t, granted, nil)
	before, err := e.Begin(t.Context(), aliceSub, "npm-internal")
	if err != nil {
		t.Fatal(err)
	}
	revoked := newFixture().repo("npm-internal", "local", "npm", "")
	revoked.s.Label.AuthzEpoch = 2
	e.o.Source.(*fakeSource).set(revoked.s)
	if err := e.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.Epoch() != 2 {
		t.Fatalf("the swap did not install epoch 2: %d", e.Epoch())
	}
	return e, before
}

func TestR_AUTHZ_015_OneIndexPerRequest(t *testing.T) {
	t.Run("a swap of the installed index does not change the decisions of a request that began before it", func(t *testing.T) {
		e, idx := swappedIndex(t)
		for i := range 3 {
			if d := e.DecideOn(t.Context(), idx, aliceSub, read("npm-internal", "x", "")); d.Outcome != Allow || d.Epoch != 1 {
				t.Fatalf("decision %d on the begun index: %+v", i, d)
			}
		}
		if got, err := e.ReadableOn(t.Context(), idx, aliceSub, []string{"npm-internal"}); err != nil || !slices.Equal(got, []string{"npm-internal"}) {
			t.Fatalf("ReadableOn = %v, %v", got, err)
		}
		if got, err := e.PathsAllowedOn(t.Context(), idx, aliceSub, read("npm-internal", "", ""), Read, []string{"a"}, nil); err != nil || !slices.Equal(got, []bool{true}) {
			t.Fatalf("PathsAllowedOn = %v, %v", got, err)
		}
		if got, err := e.CheckOn(idx, aliceSub, []CheckItem{{Repository: "npm-internal", Action: Read}}); err != nil || !slices.Equal(got, []bool{true}) {
			t.Fatalf("CheckOn = %v, %v", got, err)
		}
		// Decide twice would not: each call takes the installed index.
		if d := e.Decide(t.Context(), aliceSub, read("npm-internal", "x", "")); d.Outcome == Allow || d.Epoch != 2 {
			t.Fatalf("Decide takes the installed index: %+v", d)
		}
	})
	t.Run("Know keeps the index of a key it holds and advances to the installed index for one it does not", func(t *testing.T) {
		e, idx := swappedIndex(t)
		same, err := e.Know(t.Context(), idx, aliceSub, "npm-internal")
		if err != nil || same != idx {
			t.Fatalf("Know of a held key: %p, %v; want the begun index %p", same, err, idx)
		}
		next, err := e.Know(t.Context(), idx, aliceSub, "learned-later")
		if err != nil || next == idx || next.Epoch() != 2 {
			t.Fatalf("Know of an unknown key: epoch %d, same %v, %v", next.Epoch(), next == idx, err)
		}
	})
	t.Run("every …On form answers like its convenience form on the same index", func(t *testing.T) {
		f := exposureFixture()
		e := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling() })
		keys := []string{"files-virtual", "generic-auth", "generic-local", "generic-other", "npm-customers"}
		ctx := t.Context()
		for _, sub := range []Subject{onEP(adminSub, "customers"), onEP(botSub, "customers"), botSub, anonSub, onEP(carolSub, "default")} {
			idx, err := e.Begin(ctx, sub, keys...)
			if err != nil {
				t.Fatal(err)
			}
			c := read("generic-local", "", "internal/x.bin")
			if a, b := e.Decide(ctx, sub, c), e.DecideOn(ctx, idx, sub, c); a.Outcome != b.Outcome || a.Reason != b.Reason {
				t.Errorf("%s: Decide %+v, DecideOn %+v", sub.Name, a, b)
			}
			ra, ea := e.Readable(ctx, sub, keys)
			rb, eb := e.ReadableOn(ctx, idx, sub, keys)
			if !slices.Equal(ra, rb) || (ea == nil) != (eb == nil) {
				t.Errorf("%s: Readable %v %v, ReadableOn %v %v", sub.Name, ra, ea, rb, eb)
			}
			xa, _ := e.Exposed(ctx, sub, keys)
			if xb := e.ExposedOn(idx, sub, keys); !slices.Equal(xa, xb) {
				t.Errorf("%s: Exposed %v, ExposedOn %v", sub.Name, xa, xb)
			}
			pa, _ := e.PathsAllowed(ctx, sub, c, Read, []string{"releases/a", "internal/x"}, nil)
			pb, _ := e.PathsAllowedOn(ctx, idx, sub, c, Read, []string{"releases/a", "internal/x"}, nil)
			if !slices.Equal(pa, pb) {
				t.Errorf("%s: PathsAllowed %v, PathsAllowedOn %v", sub.Name, pa, pb)
			}
			if (e.Require(ctx, sub, "policies:read") == nil) != (e.RequireOn(ctx, idx, sub, "policies:read") == nil) {
				t.Errorf("%s: Require and RequireOn differ", sub.Name)
			}
			ha, _ := e.Holds(ctx, sub, "policies:read")
			if hb := e.HoldsOn(idx, sub, "policies:read"); ha != hb {
				t.Errorf("%s: Holds %v, HoldsOn %v", sub.Name, ha, hb)
			}
			items := []CheckItem{{Repository: "generic-local", Path: "releases/a", Action: Read}, {Repository: "generic-local", Path: "internal/x", Action: Read}}
			ca, epoch, ea := e.Check(ctx, sub, items)
			cb, eb := e.CheckOn(idx, sub, items)
			if !slices.Equal(ca, cb) || (ea == nil) != (eb == nil) || (ea == nil && epoch != idx.Epoch()) {
				t.Errorf("%s: Check %v %d %v, CheckOn %v %v, index epoch %d", sub.Name, ca, epoch, ea, cb, eb, idx.Epoch())
			}
		}
	})
}

// TestR_EP_013_ADecisionOnADetachedContextKeepsTheCeiling: the ceiling travels
// with the subject, so work that runs on a context the request does not own, a
// single-flight leader on behalf of its followers or a fan-out over the members
// of a virtual repository, decides under the endpoint of the subject it was
// given (Q-EP-20 (b)).
func TestR_EP_013_ADecisionOnADetachedContextKeepsTheCeiling(t *testing.T) {
	e := newTestEngine(t, exposureFixture(), func(o *Options) { o.Ceiling = exposureCeiling() })
	hidden := read("generic-local", "", "internal/x.bin")
	customers, partners := onEP(botSub, "customers"), onEP(botSub, "partners")

	t.Run("on context.Background and on a context stripped of its values", func(t *testing.T) {
		type key struct{}
		for name, ctx := range map[string]context.Context{
			"background":       context.Background(),
			"values stripped":  context.WithoutCancel(context.WithValue(t.Context(), key{}, "request")),
			"cancellation cut": context.WithoutCancel(t.Context()),
		} {
			if d := e.Decide(ctx, customers, hidden); d.Outcome != DenyHidden || d.Reason != ReasonExposure {
				t.Errorf("%s: Decide %+v", name, d)
			}
			idx, err := e.Begin(ctx, customers)
			if err != nil {
				t.Fatal(err)
			}
			if d := e.DecideOn(ctx, idx, customers, hidden); d.Reason != ReasonExposure {
				t.Errorf("%s: DecideOn %+v", name, d)
			}
			if d := e.DecideOn(ctx, idx, partners, hidden); d.Outcome != Allow {
				t.Errorf("%s: an endpoint in mode all admits it: %+v", name, d)
			}
		}
	})
	t.Run("a collapsed flight: the leader decides for every follower under the follower's subject", func(t *testing.T) {
		// One goroutine stands for the single-flight leader on a detached context
		// and decides for followers that arrived on different endpoints.
		subs := []Subject{customers, partners, customers, onEP(botSub, "default"), onEP(botSub, "nowhere")}
		want := []Outcome{DenyHidden, Allow, DenyHidden, Allow, DenyHidden}
		got := make([]Outcome, len(subs))
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			idx, err := e.Begin(ctx, botSub)
			if err != nil {
				t.Error(err)
				return
			}
			for i, s := range subs {
				got[i] = e.DecideOn(ctx, idx, s, hidden).Outcome
			}
		}()
		wg.Wait()
		if !slices.Equal(got, want) {
			t.Fatalf("outcomes %v, want %v", got, want)
		}
	})
	t.Run("a fan-out over the members decides every probe under the ceiling", func(t *testing.T) {
		members := []string{"generic-local", "generic-other", "files-virtual"}
		out := make([]Outcome, len(members))
		idx, err := e.Begin(context.Background(), customers, members...)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i, m := range members {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i] = e.DecideOn(context.Background(), idx, customers, read(m, "", "internal/x.bin")).Outcome
			}()
		}
		wg.Wait()
		for i, o := range out {
			if o != DenyHidden {
				t.Errorf("%s: outcome %d, want the ceiling's hidden 404", members[i], o)
			}
		}
	})
	t.Run("the listing and the check forms keep the ceiling too", func(t *testing.T) {
		keys := []string{"files-virtual", "generic-local", "generic-other", "npm-customers"}
		ctx := context.Background()
		idx, err := e.Begin(ctx, onEP(adminSub, "customers"))
		if err != nil {
			t.Fatal(err)
		}
		if got := e.ExposedOn(idx, onEP(adminSub, "customers"), keys); !slices.Equal(got, []string{"files-virtual", "generic-local", "npm-customers"}) {
			t.Errorf("ExposedOn = %v", got)
		}
		if got, _ := e.ReadableOn(ctx, idx, onEP(adminSub, "customers"), keys); slices.Contains(got, "generic-other") {
			t.Errorf("ReadableOn lists a repository the endpoint does not expose: %v", got)
		}
		if got, _ := e.PathsAllowedOn(ctx, idx, onEP(botSub, "customers"), read("generic-local", "", ""), Read, []string{"releases/a", "internal/x"}, nil); !slices.Equal(got, []bool{true, false}) {
			t.Errorf("PathsAllowedOn = %v", got)
		}
		if got, _ := e.CheckOn(idx, onEP(botSub, "customers"), []CheckItem{{Repository: "generic-local", Path: "internal/x", Action: Read}}); !slices.Equal(got, []bool{false}) {
			t.Errorf("CheckOn = %v", got)
		}
	})
	t.Run("a subject without an endpoint, a job or a command, has no ceiling", func(t *testing.T) {
		if d := e.Decide(context.Background(), botSub, hidden); d.Outcome != Allow {
			t.Fatalf("%+v", d)
		}
	})
}

// TestR_EP_013_TheSubjectCarriesTheEndpointIntoTheAudit: the endpoint column of
// an authorization event is the subject's, with no request context to read it
// from.
func TestR_EP_013_TheSubjectCarriesTheEndpointIntoTheAudit(t *testing.T) {
	rec := &recorder{}
	e := newTestEngine(t, exposureFixture(), func(o *Options) { o.Ceiling = exposureCeiling(); o.Recorder = rec })
	e.Decide(context.Background(), onEP(botSub, "customers"), read("generic-local", "", "internal/x.bin"))
	evs := rec.list()
	if len(evs) != 1 || evs[0].Endpoint != "customers" || evs[0].Type != EventDenied {
		t.Fatalf("events = %+v", evs)
	}
	var detail map[string]any
	if err := json.Unmarshal(evs[0].Details, &detail); err != nil || detail["endpoint"] != "customers" {
		t.Fatalf("details = %v, %v", detail, err)
	}
	if ev := e.decisionEvent(EventWriteAllowed, onEP(aliceSub, "default"), write("generic-local", "", "x", Deploy), Decision{Outcome: Allow}); ev.Endpoint != "default" {
		t.Fatalf("the allowed write is recorded on endpoint %q", ev.Endpoint)
	}
	if ev := e.decisionEvent(EventWriteAllowed, aliceSub, write("generic-local", "", "x", Deploy), Decision{Outcome: Allow}); ev.Endpoint != "" {
		t.Fatalf("a job's event names endpoint %q", ev.Endpoint)
	}
}

// TestR_EP_016_ExposureRuleAuditNamesTheEndpointOfTheActor: the audit recorder
// does not fill the endpoint column from the request (audit.Source has no
// endpoint), so the exposure service passes the endpoint of the acting subject,
// which edge.SubjectFrom took from the listener. Before Q-EP-20 (b) the column
// came from the request context.
func TestR_EP_016_ExposureRuleAuditNamesTheEndpointOfTheActor(t *testing.T) {
	rule := ExposureRule{ID: "r1", Endpoint: "customers", ExposureRuleInput: ExposureRuleInput{Name: "releases", Repository: "generic-local"}}
	for _, ep := range []string{"default", "ops", ""} {
		ev := exposureEvent(EventExposureCreated, onEP(adminSub, ep), rule, nil, stateOfRule(rule.ExposureRuleInput), nil, 1, "")
		if ev.Endpoint != ep {
			t.Errorf("actor on %q: the event names endpoint %q", ep, ev.Endpoint)
		}
		if ev.Subject.Name != "customers/releases" {
			t.Errorf("subject = %+v: the rule's own endpoint stays in the subject", ev.Subject)
		}
	}
}
