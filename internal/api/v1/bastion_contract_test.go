package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
)

// The contract tests of the Bastion operations (R-BAS-004, R-BAS-008, R-BAS-009): the document, the
// declaration form of the interface routes and the stubs that answer until their work packages fill them (the registration operations are built).

// TestR_BAS_008_TheInterfaceOperationsAreTaggedAndVersioned: exactly the four interface routes carry the
// permission form {bastion: true} and x-trove-bastion-interface equal to the interface version of this build.
func TestR_BAS_008_TheInterfaceOperationsAreTaggedAndVersioned(t *testing.T) {
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GET " + iface.PathHandshake:  "bastionHandshake",
		"POST " + iface.PathCheck:     "bastionCheck",
		"GET " + iface.PathFeed:       "bastionFeed",
		"POST " + iface.PathDownloads: "bastionDownloads",
	}
	got := map[string]string{}
	for pattern, op := range doc.Operations() {
		if op.BastionInterface == 0 {
			if op.Requirement.Bastion {
				t.Errorf("%s declares {bastion: true} without an interface version", pattern)
			}
			continue
		}
		got[pattern] = op.ID
		if op.BastionInterface != iface.InterfaceVersion || !op.Requirement.Bastion {
			t.Errorf("%s: interface %d, requirement %+v", pattern, op.BastionInterface, op.Requirement)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("interface operations = %v, want %v", got, want)
	}
	for pattern, id := range want {
		if got[pattern] != id {
			t.Errorf("%s = %q, want %q", pattern, got[pattern], id)
		}
	}
}

// TestR_BAS_009_ADocumentedInterfaceVersionOtherThanTheCodesStopsTheLoad: a document written for another
// version of the interface is refused at load, so a version raised in one place only is found at start.
func TestR_BAS_009_ADocumentedInterfaceVersionOtherThanTheCodesStopsTheLoad(t *testing.T) {
	raw := string(EmbeddedYAML())
	if _, err := LoadDocument([]byte(strings.Replace(raw, "x-trove-bastion-interface: 1", "x-trove-bastion-interface: 2", 1))); err == nil ||
		!strings.Contains(err.Error(), "x-trove-bastion-interface") {
		t.Fatalf("a document for interface version 2 loaded: %v", err)
	}
	if _, err := LoadDocument([]byte(strings.Replace(raw, "      x-trove-bastion-interface: 1\n      x-trove-permission:\n        bastion: true\n", "      x-trove-permission:\n        bastion: true\n", 1))); err == nil {
		t.Fatal("a {bastion: true} operation without a version loaded")
	}
}

// TestR_BAS_005_AnInterfaceRouteAdmitsOnTheRecognitionAlone: without the recognition stage's proof the route
// answers 401 bastion_token_invalid; with it the request reaches the handler, whatever the Access says.
func TestR_BAS_005_AnInterfaceRouteAdmitsOnTheRecognitionAlone(t *testing.T) {
	s := newSetup(t, func(o *Options) { o.Access = bootstrapLike{} }, nil)
	w := s.get(iface.PathHandshake)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), errs.CodeBastionTokenInvalid) {
		t.Fatalf("without recognition: %d %s", w.Code, w.Body.String())
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, iface.PathHandshake, nil)
	r = r.WithContext(bastionmain.WithRecognition(r.Context(), bastionmain.Recognition{Bastion: "partners", Route: true, Interface: iface.InterfaceVersion}))
	s.handler.ServeHTTP(rec, r)
	// The handshake has no services wired in this setup: the handler is reached and answers like every
	// operation of the API whose service is missing.
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), errs.CodeUnavailable) {
		t.Fatalf("with recognition and no service: %d %s", rec.Code, rec.Body.String())
	}
}

// TestR_BAS_004_TheRegistrationOperationsAnswer503WithoutTheirService: the registration operations are built
// (work package 1); on a replica without the registration service, as in the tests of this package, they answer
// 503 unavailable to a caller that may call them, not 501.
func TestR_BAS_004_TheRegistrationOperationsAnswer503WithoutTheirService(t *testing.T) {
	s := newSetup(t, nil, authed)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/bastions"},
		{http.MethodGet, "/api/v1/bastions/partners"},
		{http.MethodDelete, "/api/v1/bastions/partners"},
		{http.MethodDelete, "/api/v1/bastions/partners/tokens/0192f0d1-1c2a-7c3e-8a51-6f7d2b9c4e41"},
	} {
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), errs.CodeUnavailable) ||
			rec.Header().Get("Content-Type") != errs.ProblemContentType {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}
