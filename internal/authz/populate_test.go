package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
)

// populateFixture is the remote side of the team example: npmjs fills on read,
// npm-frozen is a frozen mirror that fills on deploy, npm-internal is local, and
// npm-all is the virtual repository over all three. The developers read
// everything, the CI service account may also deploy into the frozen mirror, and
// anonymous reads npmjs.
func populateFixture() *fixture {
	f := newFixture().
		repo("npmjs", "remote", "npm", `{"remote":{"cacheFillAction":"read"}}`).
		repo("npm-frozen", "remote", "npm", `{"remote":{"cacheFillAction":"deploy"}}`).
		repo("npm-odd", "remote", "npm", `{"remote":{"cacheFillAction":"sometimes"}}`).
		repo("npm-internal", "local", "npm", "").
		repo("npm-all", "virtual", "npm", `{"virtual":{"members":["npm-internal","npmjs","npm-frozen"],"deployTarget":"npm-internal"}}`)
	f.policy(policyDef{name: "readers", selectors: []string{"npmjs", "npm-frozen", "npm-odd", "npm-internal", "npm-all"}, entries: map[string][]string{idDevs: {"read"}, idBot: {"read"}}})
	f.policy(policyDef{name: "release", selectors: []string{"npm-frozen"}, entries: map[string][]string{idBot: {"deploy"}}})
	f.policy(policyDef{name: "anonymous-mirror", selectors: []string{"npmjs"}, entries: map[string][]string{AnonymousID: {"read"}}})
	f.bind(RoleAdmin, idOps)
	return f
}

// The ways a request can reach the populate check.
func session(s Subject) Subject { s.SessionID, s.AuthMethod = "session-1", "session"; return s }
func bearer(s Subject) Subject  { s.TokenID, s.AuthMethod = "token-1", "bearer"; return s }
func onFillingEP(s Subject, ep string, fills bool) Subject {
	s.Endpoint, s.CacheFill = ep, fills
	return s
}

func TestR_AUTHZ_008_PopulateCheck(t *testing.T) {
	anonymousOn := func(o *Options) { o.AnonymousAccess = func(context.Context) (bool, error) { return true, nil } }
	e := newTestEngine(t, populateFixture(), anonymousOn)
	idx, err := e.Begin(t.Context(), bobSub)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		sub     Subject
		repo    string
		want    Outcome
		reason  string
		missing Action
	}{
		{"a token holder who may read fills a remote that fills on read", bearer(bobSub), "npmjs", Allow, ReasonAllowGrant, ""},
		{"the same holder with the session cookie does not populate", session(bobSub), "npmjs", DenyVisible, ReasonPopulate, ""},
		{"an administrator with the session cookie does not populate either", session(adminSub), "npmjs", DenyVisible, ReasonPopulate, ""},
		{"an administrator with a token populates through content:bypass", bearer(adminSub), "npm-frozen", Allow, ReasonAllowRole, ""},
		{"read alone does not fill a frozen mirror", bearer(bobSub), "npm-frozen", DenyVisible, ReasonPopulate, Deploy},
		{"deploy fills a frozen mirror", bearer(botSub), "npm-frozen", Allow, ReasonAllowGrant, ""},
		{"deploy with the session cookie does not", session(botSub), "npm-frozen", DenyVisible, ReasonPopulate, ""},
		{"R-EP-019 an endpoint with cache_fill false refuses every subject", onFillingEP(bearer(bobSub), "customers", false), "npmjs", DenyVisible, ReasonPopulate, ""},
		{"R-EP-019 an endpoint with cache_fill true leaves it to the grants", onFillingEP(bearer(bobSub), "partners", true), "npmjs", Allow, ReasonAllowGrant, ""},
		{"a caller who may not read gets the read's refusal, 404", bearer(carolSub), "npmjs", DenyHidden, ReasonNoGrant, Read},
		{"a local repository has no upstream to fill from", bearer(bobSub), "npm-internal", DenyVisible, ReasonPopulate, ""},
		{"a cacheFillAction that names no action fills for nobody", bearer(adminSub), "npm-odd", DenyVisible, ReasonPopulate, ""},
		{"anonymous may populate a remote that fills on read once anonymous access is on", anonSub, "npmjs", Allow, ReasonAllowGrant, ""},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-008 "+tc.name, func(t *testing.T) {
			d := e.MayPopulateOn(t.Context(), idx, tc.sub, read(tc.repo, "left-pad", "left-pad/-/left-pad-1.3.0.tgz"))
			if d.Outcome != tc.want || d.Reason != tc.reason {
				t.Fatalf("decision %+v, want outcome %d reason %q", d, tc.want, tc.reason)
			}
			if tc.missing != "" && d.Missing != tc.missing {
				t.Fatalf("missing %q, want %q", d.Missing, tc.missing)
			}
		})
	}
	t.Run("R-AUTHZ-016 a refused populate is 403 populate_not_permitted", func(t *testing.T) {
		d := e.MayPopulateOn(t.Context(), idx, session(bobSub), read("npmjs", "left-pad", ""))
		if ee, ok := errs.As(d.Err()); !ok || ee.Status() != 403 || ee.Code != errs.CodePopulateNotPermitted {
			t.Fatalf("err = %v", d.Err())
		}
	})
	t.Run("R-AUTHZ-008 anonymous does not populate while anonymous access is off", func(t *testing.T) {
		off := newTestEngine(t, populateFixture(), nil)
		if d := off.MayPopulateOn(t.Context(), idx, anonSub, read("npmjs", "left-pad", "")); d.Outcome != DenyUnauthenticated {
			t.Fatalf("decision %+v", d)
		}
	})
	t.Run("R-AUTHZ-029 an allowed populate names the grants that allowed it", func(t *testing.T) {
		d := e.MayPopulateOn(t.Context(), idx, bearer(botSub), read("npm-frozen", "left-pad", ""))
		if len(d.Sources) != 2 || d.Sources[0] != "readers" || d.Sources[1] != "release" {
			t.Fatalf("sources %v", d.Sources)
		}
	})
}

// TestR_AUTHZ_029_PopulateRefusalsAreAudited: the question form records nothing;
// the request form records a refused populate like a refused write, also with
// authz.audit.reads none, and leaves a refused read to the read rules.
func TestR_AUTHZ_029_PopulateRefusalsAreAudited(t *testing.T) {
	rec := &recorder{}
	e := newTestEngine(t, populateFixture(), func(o *Options) {
		o.Recorder = rec
		o.Config.Audit.Reads = config.AuthzReadsNone
	})
	idx, err := e.Begin(t.Context(), bobSub)
	if err != nil {
		t.Fatal(err)
	}
	c := read("npmjs", "left-pad", "")
	if d := e.MayPopulateOn(t.Context(), idx, session(bobSub), c); d.Outcome == Allow || len(rec.list()) != 0 {
		t.Fatalf("the question recorded %d events (decision %+v)", len(rec.list()), d)
	}
	if d := e.PopulateOn(t.Context(), idx, session(bobSub), c); d.Reason != ReasonPopulate {
		t.Fatalf("decision %+v", d)
	}
	evs := rec.list()
	if len(evs) != 1 || evs[0].Type != EventDenied || evs[0].Reason != ReasonPopulate {
		t.Fatalf("events %+v", evs)
	}
	if d := e.PopulateOn(t.Context(), idx, bearer(carolSub), c); d.Outcome != DenyHidden || len(rec.list()) != 1 {
		t.Fatalf("a refused read is a read: %+v, %d events", d, len(rec.list()))
	}
}

// addressedCeiling hides every read of what the client addressed as npm-all on the
// endpoint customers: exposure is decided on the addressed repository, never on
// the member (Q-EP-11).
type addressedCeiling struct{}

func (addressedCeiling) Admits(_ *Index, endpoint string, c Coordinate, a Action) CeilingVerdict {
	if endpoint != "customers" {
		return CeilingAdmits
	}
	if a != Read {
		return CeilingReadOnly
	}
	if firstNonEmpty(c.Addressed, c.Repository) == "npm-all" {
		return CeilingHidesRead
	}
	return CeilingAdmits
}

// TestR_AUTHZ_015_MemberUsable is the MemberUsable table of R-AUTHZ-015: read on
// the member for the coordinate decides whether the resolver consults it, and the
// populate check of R-AUTHZ-008 whether it may also fill a remote member's cache.
func TestR_AUTHZ_015_MemberUsable(t *testing.T) {
	e := newTestEngine(t, populateFixture(), func(o *Options) { o.Ceiling = addressedCeiling{} })
	idx, err := e.Begin(t.Context(), bobSub)
	if err != nil {
		t.Fatal(err)
	}
	through := read("npm-all", "left-pad", "")
	cases := []struct {
		name       string
		sub        Subject
		member     string
		read, fill bool
	}{
		{"a local member the subject reads is consulted, nothing to fill", bearer(bobSub), "npm-internal", true, false},
		{"a member the subject cannot read is skipped", bearer(carolSub), "npm-internal", false, false},
		{"a remote member that fills on read is consulted and filled", bearer(bobSub), "npmjs", true, true},
		{"the session cookie consults the remote member's cache only", session(bobSub), "npmjs", true, false},
		{"a frozen mirror is consulted and filled only by deploy", bearer(bobSub), "npm-frozen", true, false},
		{"deploy fills the frozen mirror through the virtual repository", bearer(botSub), "npm-frozen", true, true},
		{"R-EP-019 an endpoint without cache fill consults the cache only", onFillingEP(bearer(bobSub), "partners", false), "npmjs", true, false},
		{"R-EP-019 an endpoint with cache fill fills", onFillingEP(bearer(bobSub), "partners", true), "npmjs", true, true},
		{"R-EP-013 the exposure of the addressed repository hides every member", onFillingEP(bearer(bobSub), "customers", true), "npmjs", false, false},
		{"a member the index does not hold is skipped", bearer(adminSub), "ghost", false, false},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-015 R-AUTHZ-009 "+tc.name, func(t *testing.T) {
			r, f := e.MemberUsableOn(t.Context(), idx, tc.sub, tc.member, through)
			if r != tc.read || f != tc.fill {
				t.Fatalf("read %v fill %v, want %v %v", r, f, tc.read, tc.fill)
			}
		})
	}
	t.Run("R-AUTHZ-015 the member is decided for the request's coordinate", func(t *testing.T) {
		f := populateFixture()
		f.policy(policyDef{name: "web-only", selectors: []string{"npm-internal"}, pkgInc: []string{"@web/*"}, entries: map[string][]string{idCarol: {"read"}}})
		e := newTestEngine(t, f, nil)
		idx, err := e.Begin(t.Context(), carolSub)
		if err != nil {
			t.Fatal(err)
		}
		if r, _ := e.MemberUsableOn(t.Context(), idx, bearer(carolSub), "npm-internal", read("npm-all", "@web/button", "")); !r {
			t.Fatal("a name inside the grant's patterns is not consulted")
		}
		if r, _ := e.MemberUsableOn(t.Context(), idx, bearer(carolSub), "npm-internal", read("npm-all", "left-pad", "")); r {
			t.Fatal("a name outside the grant's patterns is consulted")
		}
	})
}
