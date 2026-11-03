//go:build integration

package bastioniface

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_BAS_008_TheRecognizeStageIsInTheChainOfTheComposedReplica: a replica composed by app.New (the real chain
// with every stage, the format routes and the document validator) answers an invalid Bastion token with the
// problem details of the management API, on an interface route and on a package route, counts it, and leaves a
// request with a token of another type alone. Until the registrations of work package 1 are wired, every Bastion
// token is invalid, which is what this test can prove of the wiring.
func TestR_BAS_008_TheRecognizeStageIsInTheChainOfTheComposedReplica(t *testing.T) {
	s := testenv.NewStack(t, testenv.StackOptions{Valkey: true, Name: "bif"})
	r := s.Start(t, testenv.ReplicaOptions{Name: "bif-web-1", Role: "web"})
	bst, err := token.Generate(token.TypeBST)
	if err != nil {
		t.Fatal(err)
	}
	pat, err := token.Generate(token.TypePAT)
	if err != nil {
		t.Fatal(err)
	}
	v := strconv.Itoa(iface.InterfaceVersion)

	a := s.Do(r, http.MethodGet, iface.PathHandshake, bst, nil, iface.HeaderInterface, v)
	if a.Status != http.StatusUnauthorized || a.Code(t) != errs.CodeBastionTokenInvalid || a.Header.Get("Content-Type") != errs.ProblemContentType {
		t.Errorf("the handshake with a Bastion token no registration owns: %d %s", a.Status, a.Body)
	}

	a = s.Do(r, http.MethodGet, "/api/npm/npm-all/lodash", "", nil, iface.HeaderAuthorization, "Bearer "+bst, iface.HeaderInterface, v)
	if a.Status != http.StatusUnauthorized || a.Code(t) != errs.CodeBastionTokenInvalid || a.Header.Get("Content-Type") != errs.ProblemContentType {
		t.Errorf("a forwarded request with such a token: %d %s %s", a.Status, a.Header.Get("Content-Type"), a.Body)
	}

	// A token of another type in Trove-Bastion-Authorization leaves the request ordinary: whatever the main
	// installation says to the request, it carries no mark of a Bastion.
	a = s.Do(r, http.MethodGet, "/api/npm/npm-all/lodash", "", nil, iface.HeaderAuthorization, "Bearer "+pat, iface.HeaderInterface, v)
	for k := range a.Header {
		if strings.HasPrefix(k, iface.HeaderPrefix) {
			t.Errorf("an ordinary request is answered with %s", k)
		}
	}
	if a.Code(t) == errs.CodeBastionTokenInvalid {
		t.Errorf("a token of another type was taken for a Bastion token: %d %s", a.Status, a.Body)
	}

	m := r.Metrics(t)
	for _, want := range []string{
		`trove_bastion_interface_requests_total{bastion="unknown",operation="handshake",outcome="invalid_token"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}
