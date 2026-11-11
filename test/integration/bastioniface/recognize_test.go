//go:build integration

package bastioniface

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// echo is the answer of the stand-in package route: what the chain made of the request.
type echo struct {
	Client               string `json:"client"`
	BaseURL              string `json:"baseURL"`
	Principal            string `json:"principal"`
	Anonymous            bool   `json:"anonymous"`
	Authorization        string `json:"authorization"`
	BastionAuthorization string `json:"bastionAuthorization"`
}

func packageRequest(path string, h ...string) call {
	hdr := http.Header{}
	for i := 0; i+1 < len(h); i += 2 {
		hdr.Add(h[i], h[i+1])
	}
	return call{method: http.MethodGet, path: path, header: hdr}
}

// TestR_BAS_008_AForwardedRequestIsRecognizedOnlyByAValidBastionToken is the acceptance criterion of R-BAS-008
// through the real chain: a revoked Bastion token answers 401 bastion_token_invalid as problem+json on a package
// route, a valid token of another type leaves the request ordinary without a marking header, and a valid Bastion
// token takes the Bastion's credential out of the request while the caller's own stays.
func TestR_BAS_008_AForwardedRequestIsRecognizedOnlyByAValidBastionToken(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{endpointAnonymous: true})
	bst := s.registered("partners", e.registration("partners"))
	s.setPublic(true)
	sa := s.serviceAccount("acme-full")
	caller := s.sat(sa).Secret
	v := strconv.Itoa(iface.InterfaceVersion)

	revoked, err := token.Generate(token.TypeBST)
	if err != nil {
		t.Fatal(err)
	}
	rp := s.do(packageRequest("/api/npm/it/npm-all/lodash", "Authorization", "Bearer "+caller, iface.HeaderAuthorization, "Bearer "+revoked, iface.HeaderInterface, v))
	if rp.Status != http.StatusUnauthorized || rp.code() != errs.CodeBastionTokenInvalid || rp.Header.Get("Content-Type") != errs.ProblemContentType {
		t.Errorf("a revoked Bastion token: %d %s %s", rp.Status, rp.Header.Get("Content-Type"), rp.Body)
	}

	// $PAT in Trove-Bastion-Authorization: ordinary request, no marking header, the headers are ignored.
	rp = s.do(packageRequest("/api/npm/it/npm-all/lodash", "Authorization", "Bearer "+caller, iface.HeaderAuthorization, "Bearer "+caller,
		iface.HeaderInterface, v, iface.HeaderReplica, "pod", iface.HeaderProbe, "1"))
	var got echo
	rp.decode(t, &got)
	if rp.Status != http.StatusOK || got.Principal != "acme-full" {
		t.Fatalf("an ordinary request: %d %s", rp.Status, rp.Body)
	}
	for k := range rp.Header {
		if strings.HasPrefix(k, iface.HeaderPrefix) {
			t.Errorf("an ordinary answer carries %s", k)
		}
	}

	// A valid Bastion token: the caller's credential is untouched, the Bastion's is not passed on.
	rp = s.do(packageRequest("/api/npm/it/npm-all/lodash", "Authorization", "Bearer "+caller, iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v))
	rp.decode(t, &got)
	if rp.Status != http.StatusOK || got.Principal != "acme-full" || got.Authorization != "Bearer "+caller || got.BastionAuthorization != "" {
		t.Errorf("a recognized request: %d %+v", rp.Status, got)
	}
	if rp.Header.Get(iface.HeaderInterface) != v {
		t.Errorf("%s = %q", iface.HeaderInterface, rp.Header.Get(iface.HeaderInterface))
	}
	// No Trove-Bastion-* request header is ever echoed: the only one on the answer is the version.
	for k := range rp.Header {
		if strings.HasPrefix(k, iface.HeaderPrefix) && k != iface.HeaderInterface {
			t.Errorf("the answer echoes %s", k)
		}
	}

	// A caller without a credential through a Bastion is anonymous, as without one.
	rp = s.do(packageRequest("/api/npm/it/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v))
	rp.decode(t, &got)
	if !got.Anonymous {
		t.Errorf("a forwarded request without a credential: %+v", got)
	}
}

// TestR_BAS_009_AnotherInterfaceVersionAnswersAMismatchOnEveryRoute: 400 with the member interfaceVersion, as
// problem details also on a package route, and the answer names the main installation's version.
func TestR_BAS_009_AnotherInterfaceVersionAnswersAMismatchOnEveryRoute(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bst := s.registered("partners", e.registration("partners"))
	for name, c := range map[string]call{
		"package route, none":      packageRequest("/api/npm/it/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+bst),
		"package route, version 2": packageRequest("/api/npm/it/npm-all/lodash", iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, "2"),
		"handshake, version 0":     {method: http.MethodGet, path: iface.PathHandshake, header: http.Header{"Authorization": {"Bearer " + bst}, iface.HeaderInterface: {"0"}}},
	} {
		rp := s.do(c)
		var p map[string]any
		rp.decode(t, &p)
		if rp.Status != http.StatusBadRequest || p["code"] != errs.CodeBastionInterfaceMismatch || p[iface.ProblemMemberInterfaceVersion] != float64(iface.InterfaceVersion) ||
			rp.Header.Get("Content-Type") != errs.ProblemContentType || rp.Header.Get(iface.HeaderInterface) != "1" {
			t.Errorf("%s: %d %v %v", name, rp.Status, rp.Header, p)
		}
	}
}

// TestR_BAS_008_AnInterfaceRouteTakesAnyOtherCredentialAsNoBastion: a service account token on an interface route
// is no Bastion; a Bastion token anywhere but the interface and Trove-Bastion-Authorization is no credential at all
// (R-BAS-005).
func TestR_BAS_008_AnInterfaceRouteTakesAnyOtherCredentialAsNoBastion(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bst := s.registered("partners", e.registration("partners"))
	sa := s.serviceAccount("acme-full")
	caller := s.sat(sa).Secret
	v := strconv.Itoa(iface.InterfaceVersion)
	rp := s.do(call{method: http.MethodGet, path: iface.PathHandshake, header: http.Header{"Authorization": {"Bearer " + caller}, iface.HeaderInterface: {v}}})
	if rp.Status != http.StatusUnauthorized || rp.code() != errs.CodeBastionTokenInvalid {
		t.Errorf("a service account token on the handshake: %d %s", rp.Status, rp.Body)
	}
	// The Bastion token as the caller's credential of a package route is an unknown token like any other type
	// the endpoint does not accept (R-BAS-005): the request is the caller's, not the Bastion's.
	rp = s.do(packageRequest("/api/npm/it/npm-all/lodash", "Authorization", "Bearer "+bst))
	if rp.Status != http.StatusUnauthorized || rp.code() != errs.CodeInvalidToken {
		t.Errorf("a Bastion token as Authorization of a package route: %d %s", rp.Status, rp.Body)
	}
}

// TestR_BAS_008_InterfaceRequestsAreCounted: trove_bastion_interface_requests_total by registration, operation
// and outcome through the real chain.
func TestR_BAS_008_InterfaceRequestsAreCounted(t *testing.T) {
	e := newEnv(t)
	s := e.newSite(siteOpts{})
	bst := s.registered("partners", e.registration("partners"))
	s.iface(bst, http.MethodGet, iface.PathHandshake, nil)
	s.iface(bst, http.MethodGet, iface.PathHandshake, nil)
	s.iface(bst, http.MethodGet, iface.PathHandshake, nil, iface.HeaderInterface, "9")
	s.iface(bst, http.MethodGet, iface.PathFeed+"?after=garbage", nil)
	families, err := s.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "trove_bastion_interface_requests_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			key := ""
			for _, l := range m.GetLabel() {
				key += l.GetName() + "=" + l.GetValue() + ","
			}
			got[key] = m.GetCounter().GetValue()
		}
	}
	want := map[string]float64{
		"bastion=partners,operation=handshake,outcome=ok,":                 2,
		"bastion=partners,operation=handshake,outcome=interface_mismatch,": 1,
		"bastion=partners,operation=feed,outcome=refused,":                 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
}

// bastionForwarded are the forwarded headers of a Bastion for a caller at addr (R-BAS-017): one entry in each
// family and the Bastion's own scheme, host and port.
func bastionForwarded(addr string) []string {
	return []string{
		"X-Forwarded-For", addr, "Forwarded", "for=" + addr + ";proto=https;host=packages.example.com",
		"X-Forwarded-Proto", "https", "X-Forwarded-Host", "packages.example.com", "X-Forwarded-Port", "443",
	}
}

// TestR_BAS_017_TheRealClientAndTheBastionsHostReachTheMainInstallation: through the real chain and the real
// authentication stage, with either value of server.forwarded_header: the caller's address (not the Bastion's, and
// not what the caller put in its own X-Forwarded-For) is the one in the request and in the audit, documents are
// rendered on the Bastion's host, and a loop of failed credentials of one caller is throttled by that caller's
// address while a second caller through the same Bastion is not slowed down.
func TestR_BAS_017_TheRealClientAndTheBastionsHostReachTheMainInstallation(t *testing.T) {
	for _, family := range []httpserver.ForwardedHeader{httpserver.HeaderXForwardedFor, httpserver.HeaderForwarded} {
		t.Run(string(family), func(t *testing.T) {
			e := newEnv(t)
			s := e.newSite(siteOpts{forwarded: family, additional: []string{"https://packages.example.com"}, endpointAnonymous: true, failures: 20})
			bst := s.registered("partners", e.registration("partners"))
			s.setPublic(true)
			v := strconv.Itoa(iface.InterfaceVersion)

			rp := s.do(packageRequest("/api/npm/it/npm-all/lodash", append(bastionForwarded("203.0.113.7"), iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)...))
			var got echo
			rp.decode(t, &got)
			if rp.Status != http.StatusOK || got.Client != "203.0.113.7" || got.BaseURL != "https://packages.example.com" {
				t.Fatalf("%d %+v", rp.Status, got)
			}

			// The audit names the caller: a revoked token presented through the Bastion.
			sa := s.serviceAccount("acme-full")
			full := s.sat(sa)
			if _, err := s.tokens.Revoke(context.Background(), admin, full.Record.ID, "owner"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the revocation", 10*time.Second, func() bool {
				rp := s.do(packageRequest("/api/npm/it/npm-all/lodash", append(bastionForwarded("203.0.113.7"), "Authorization", "Bearer "+full.Secret, iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)...))
				return rp.Status == http.StatusUnauthorized
			})
			waitFor(t, "token.rejected with the caller's address", 10*time.Second, func() bool {
				return e.number("SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND host(source_ip) = '203.0.113.7' AND reason = 'revoked'") >= 1
			})
			if n := e.number("SELECT count(*) FROM audit_event WHERE action = 'token.rejected' AND host(source_ip) IN ('127.0.0.1', '192.0.2.1')"); n != 0 {
				t.Errorf("%d token.rejected events name the Bastion or a forged address", n)
			}

			// A caller's loop of failed credentials is throttled by its address, the second caller is not slowed.
			request := func(addr string) reply {
				raw, _ := token.Generate(token.TypePAT)
				return s.do(packageRequest("/api/npm/it/npm-all/lodash", append(bastionForwarded(addr), "Authorization", "Bearer "+raw, iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)...))
			}
			throttled := 0
			for range 40 {
				if rp := request("203.0.113.8"); rp.Status == http.StatusTooManyRequests {
					throttled++
				}
			}
			if throttled == 0 {
				t.Error("40 failed credentials of one caller were never throttled")
			}
			if rp := request("203.0.113.9"); rp.Status != http.StatusUnauthorized {
				t.Errorf("a second caller through the same Bastion: %d %s", rp.Status, rp.Body)
			}
		})
	}
}
