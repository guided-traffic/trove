//go:build integration

package bastioniface

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
)

type checkBody struct {
	Credential    *string  `json:"credential"`
	ClientAddress string   `json:"clientAddress"`
	Repositories  []string `json:"repositories"`
}

func (s *site) check(bst string, cred *string, addr string, repos ...string) (iface.CheckAnswer, reply) {
	s.t.Helper()
	rp := s.iface(bst, http.MethodPost, iface.PathCheck, checkBody{Credential: cred, ClientAddress: addr, Repositories: repos})
	var ans iface.CheckAnswer
	if rp.Status == http.StatusOK {
		rp.decode(s.t, &ans)
	}
	return ans, rp
}

func str(s string) *string { return &s }

// checkFixture: the service accounts $FULL (acme-full) and $PART (acme-part) and a registration.
type checkFixture struct {
	*site
	e                *env
	bst              string
	full, part       string // the tokens
	fullSA, partSA   string // the principal IDs
	fullRec, partRec token.Record
}

func newCheckFixture(t *testing.T, o siteOpts) checkFixture {
	e := newEnv(t)
	s := e.newSite(o)
	f := checkFixture{site: s, e: e, bst: s.registered("partners", e.registration("partners"))}
	full, part := s.serviceAccount("acme-full"), s.serviceAccount("acme-part")
	exp := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	fc := s.sat(full, func(r *token.CreateRequest) { r.TTL.ExpiresAt = &exp })
	pc := s.sat(part)
	f.full, f.part, f.fullSA, f.partSA, f.fullRec, f.partRec = fc.Secret, pc.Secret, full.ID, part.ID, fc.Record, pc.Record
	s.shapes.set(full.ID, "it/npm-all", bastionmain.Shape{Read: iface.ReadFull})
	s.shapes.set(part.ID, "it/npm-all", bastionmain.Shape{Read: iface.ReadPartial})
	s.shapes.set(part.ID, "it/npm-partner", bastionmain.Shape{Read: iface.ReadNone, Status: http.StatusForbidden})
	return f
}

// TestR_BAS_011_ACheckAnswersTheSubjectAndTheShapes is the first acceptance criterion of R-BAS-011 with the real
// token service and decision point: $FULL on two repositories, $PART on one, a revoked token, no credential with
// anonymous access off.
func TestR_BAS_011_ACheckAnswersTheSubjectAndTheShapes(t *testing.T) {
	f := newCheckFixture(t, siteOpts{})
	ans, rp := f.check(f.bst, str(f.full), "203.0.113.7", "it/npm-all", "it/npm-partner")
	if rp.Status != http.StatusOK {
		t.Fatalf("%d %s", rp.Status, rp.Body)
	}
	if got := rp.Header.Get(iface.HeaderInterface); got != strconv.Itoa(iface.InterfaceVersion) {
		t.Errorf("%s = %q", iface.HeaderInterface, got)
	}
	sub := ans.Subject
	if sub.Kind != iface.SubjectToken || sub.TokenID != f.fullRec.ID || sub.PrincipalID != f.fullSA || sub.ExpiresAt == nil || !sub.ExpiresAt.Equal(*f.fullRec.ExpiresAt) {
		t.Errorf("subject = %+v, token %+v", sub, f.fullRec)
	}
	if ans.Repositories["it/npm-all"] != (iface.RepositoryRead{Read: iface.ReadFull}) ||
		ans.Repositories["it/npm-partner"] != (iface.RepositoryRead{Read: iface.ReadNone, Status: 404}) {
		t.Errorf("repositories = %+v", ans.Repositories)
	}

	ans, _ = f.check(f.bst, str(f.part), "203.0.113.7", "it/npm-all", "it/npm-partner", "it/npm-unknown")
	if ans.Repositories["it/npm-all"].Read != iface.ReadPartial || ans.Repositories["it/npm-partner"] != (iface.RepositoryRead{Read: iface.ReadNone, Status: 403}) ||
		ans.Repositories["it/npm-unknown"] != (iface.RepositoryRead{Read: iface.ReadNone, Status: 404}) {
		t.Errorf("$PART: %+v", ans.Repositories)
	}
	if ans.Subject.ExpiresAt == nil || !ans.Subject.ExpiresAt.Equal(*f.partRec.ExpiresAt) {
		t.Errorf("the expiry of $PART = %v, want %v", ans.Subject.ExpiresAt, f.partRec.ExpiresAt)
	}

	if _, err := f.tokens.Revoke(context.Background(), admin, f.fullRec.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the revocation reaches the check", 10*time.Second, func() bool {
		a, _ := f.check(f.bst, str(f.full), "203.0.113.7", "it/npm-all")
		return a.Subject.Kind == iface.SubjectInvalid
	})
	if a, _ := f.check(f.bst, str(f.full), "203.0.113.7", "it/npm-all"); len(a.Repositories) != 0 {
		t.Errorf("an invalid subject carries shapes: %+v", a.Repositories)
	}
	if a, _ := f.check(f.bst, nil, "203.0.113.7", "it/npm-all"); a.Subject.Kind != iface.SubjectAnonymousRefused {
		t.Errorf("no credential, anonymous off: %+v", a.Subject)
	}
	if a, _ := f.check(f.bst, str("trv_sat_notatokenatall"), "203.0.113.7", "it/npm-all"); a.Subject.Kind != iface.SubjectInvalid {
		t.Errorf("a malformed credential: %+v", a.Subject)
	}
}

func TestR_BAS_011_AnAnonymousCallerIsCheckedWhenAnonymousAccessIsOn(t *testing.T) {
	f := newCheckFixture(t, siteOpts{endpointAnonymous: true})
	on := true
	if _, err := f.setts.Update(context.Background(), admin, token.SettingsPatch{AnonymousAccess: &on, Expected: errs.AnyRevision}); err != nil {
		t.Fatal(err)
	}
	f.shapes.set("", "it/npm-public", bastionmain.Shape{Read: iface.ReadFull})
	waitFor(t, "the switch reaches the check", 10*time.Second, func() bool {
		a, _ := f.check(f.bst, nil, "203.0.113.7", "it/npm-public")
		return a.Subject.Kind == iface.SubjectAnonymous
	})
	a, _ := f.check(f.bst, nil, "203.0.113.7", "it/npm-public", "it/npm-all")
	if a.Repositories["it/npm-public"].Read != iface.ReadFull || a.Repositories["it/npm-all"] != (iface.RepositoryRead{Read: iface.ReadNone, Status: 404}) {
		t.Errorf("anonymous: %+v", a)
	}
}

// TestR_BAS_011_ACheckWritesTheAuditOfADirectRequestAndNoAuthzDenied: a rejected credential is token.rejected with
// the source address of the caller, not of the Bastion; the type of a Bastion token is refused at the default
// endpoint like on any other route; and no authz.denied is written, whatever the shapes say.
func TestR_BAS_011_ACheckWritesTheAuditOfADirectRequestAndNoAuthzDenied(t *testing.T) {
	f := newCheckFixture(t, siteOpts{})
	if _, err := f.tokens.Revoke(context.Background(), admin, f.partRec.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the revocation", 10*time.Second, func() bool {
		a, _ := f.check(f.bst, str(f.part), "203.0.113.9", "it/npm-all")
		return a.Subject.Kind == iface.SubjectInvalid
	})
	f.check(f.bst, str(f.full), "203.0.113.9", "it/npm-all", "it/npm-partner", "it/npm-secret")
	if a, _ := f.check(f.bst, str(f.bst), "203.0.113.11", "it/npm-all"); a.Subject.Kind != iface.SubjectInvalid {
		t.Errorf("a Bastion token as the caller's credential: %+v", a.Subject)
	}
	waitFor(t, "token.rejected with the caller's address", 10*time.Second, func() bool {
		return f.e.number("SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND host(source_ip) = '203.0.113.9' AND reason = 'revoked'") >= 1 &&
			f.e.number("SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND host(source_ip) = '203.0.113.11' AND reason = 'endpoint_not_accepted'") >= 1
	})
	if n := f.e.number("SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND host(source_ip) = '127.0.0.1'"); n != 0 {
		t.Errorf("%d token.rejected events name the Bastion's address", n)
	}
	time.Sleep(1500 * time.Millisecond) // the asynchronous writer flushes every second
	if n := f.e.number("SELECT count(*) FROM audit_event WHERE action = 'authz.denied'"); n != 0 || len(f.rec.named("authz.denied")) != 0 {
		t.Errorf("%d authz.denied events", n)
	}
}

// TestR_BAS_011_FourHundredFailedChecksFromOneAddressAreThrottled is the second acceptance criterion: from the
// 301st rejected credential within a minute the answer is throttled with retryAfterSeconds, a direct request from
// the same address is throttled too, and a second caller through the same Bastion is not slowed down.
func TestR_BAS_011_FourHundredFailedChecksFromOneAddressAreThrottled(t *testing.T) {
	// The test connects through loopback as the main installation's own ingress (server.trusted_proxies), so the
	// direct request below names its client address in X-Forwarded-For.
	f := newCheckFixture(t, siteOpts{trusted: []string{"127.0.0.1/32", "::1/128"}})
	const addr = "203.0.113.50"
	var firstThrottled int
	for i := 1; i <= 400; i++ {
		raw, err := token.Generate(token.TypePAT)
		if err != nil {
			t.Fatal(err)
		}
		a, rp := f.check(f.bst, str(raw), addr, "it/npm-all")
		if rp.Status != http.StatusOK {
			t.Fatalf("check %d: %d %s", i, rp.Status, rp.Body)
		}
		switch a.Subject.Kind {
		case iface.SubjectInvalid:
			if firstThrottled != 0 {
				t.Fatalf("check %d is invalid after the throttle began at %d", i, firstThrottled)
			}
		case iface.SubjectThrottled:
			if firstThrottled == 0 {
				firstThrottled = i
				if a.Subject.RetryAfterSeconds == nil || *a.Subject.RetryAfterSeconds != 60 {
					t.Errorf("retryAfterSeconds = %v", a.Subject.RetryAfterSeconds)
				}
			}
		default:
			t.Fatalf("check %d: %+v", i, a.Subject)
		}
	}
	if firstThrottled != 301 {
		t.Errorf("the first throttled check is number %d, want 301", firstThrottled)
	}

	// A direct request from that address is throttled too: the bucket is the caller's address.
	raw, _ := token.Generate(token.TypePAT)
	rp := f.do(call{method: http.MethodGet, path: "/api/npm/it/npm-all/lodash", header: http.Header{"Authorization": {"Bearer " + raw}, "X-Forwarded-For": {addr}}})
	if rp.Status != http.StatusTooManyRequests || rp.code() != errs.CodeRateLimited {
		t.Errorf("a direct request from the throttled address: %d %s", rp.Status, rp.Body)
	}
	// A credential that is known is exempt from the throttle while it is cached or marked; a second caller
	// through the same Bastion is not slowed down at all.
	if a, _ := f.check(f.bst, str(f.full), "203.0.113.51", "it/npm-all"); a.Subject.Kind != iface.SubjectToken {
		t.Errorf("a second caller: %+v", a.Subject)
	}
	raw2, _ := token.Generate(token.TypePAT)
	if a, _ := f.check(f.bst, str(raw2), "203.0.113.51", "it/npm-all"); a.Subject.Kind != iface.SubjectInvalid {
		t.Errorf("a failed credential of a second caller: %+v", a.Subject)
	}
}

// TestR_BAS_011_ACheckIsValidatedAndNeedsTheBastionToken: a body above the bounds is 422, a caller's token is no
// Bastion token on an interface route.
func TestR_BAS_011_ACheckIsValidatedAndNeedsTheBastionToken(t *testing.T) {
	f := newCheckFixture(t, siteOpts{})
	if _, rp := f.check(f.bst, nil, "203.0.113.7", "a1", "a2", "a3", "a4", "a5"); rp.Status != http.StatusUnprocessableEntity {
		t.Errorf("five repositories with max_repositories 4: %d %s", rp.Status, rp.Body)
	}
	// A name without a project is no repository name (R-BAS-011, Q-REPO-212): it is refused as malformed, never
	// answered as a missing repository of the default project.
	if _, rp := f.check(f.bst, nil, "203.0.113.7", "npm-all"); rp.Status != http.StatusBadRequest || rp.code() != errs.CodeInvalid {
		t.Errorf("a name without a project: %d %s", rp.Status, rp.Body)
	}
	if _, rp := f.check(f.bst, nil, "nonsense", "a1"); rp.Status != http.StatusUnprocessableEntity {
		t.Errorf("a client address that is none: %d", rp.Status)
	}
	rp := f.iface(f.full, http.MethodPost, iface.PathCheck, checkBody{ClientAddress: "203.0.113.7", Repositories: []string{"it/npm-all"}})
	if rp.Status != http.StatusUnauthorized || rp.code() != errs.CodeBastionTokenInvalid && rp.code() != errs.CodeInvalidToken {
		t.Errorf("a service account token on the check: %d %s", rp.Status, rp.Body)
	}
	if rp := f.iface(f.bst, http.MethodPost, iface.PathCheck, checkBody{ClientAddress: "203.0.113.7", Repositories: []string{"it/npm-all"}}, iface.HeaderInterface, "2"); rp.Status != http.StatusBadRequest || rp.code() != errs.CodeBastionInterfaceMismatch {
		t.Errorf("another interface version: %d %s", rp.Status, rp.Body)
	}
}
