package dispatch

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestR_REPO_001_TwoProjectsWithTheSameRepositoryKeyAreSeparate(t *testing.T) {
	r := newRig(t)
	ops := qname.Repo{Project: "ops", Key: "local"}
	r.repos.byKey[ops] = repository.Repository{ID: "20", Name: ops, Class: repository.Local, Format: "probe"}
	for path, want := range map[string]string{
		"/api/probe/it/local/left-pad":  "probe.doc.get it/local left-pad",
		"/api/probe/ops/local/left-pad": "probe.doc.get ops/local left-pad",
	} {
		r.reached = nil
		if rec := r.do("GET", path); rec.Code != 204 || strings.Join(r.reached, ",") != want {
			t.Errorf("%s: %d, reached %v, want %s", path, rec.Code, r.reached, want)
		}
	}
	asked := map[qname.Repo]bool{}
	for _, c := range r.decider.asked {
		asked[c.Repository] = true
	}
	if !asked[ops] || !asked[qr("local")] {
		t.Errorf("the decision was asked for %v", r.decider.asked)
	}
	// The same key in a project that does not hold it is a missing repository.
	if rec := r.do("GET", "/api/probe/web/local/left-pad"); rec.Code != 404 {
		t.Errorf("a key of another project: %d", rec.Code)
	}
}

func TestR_REPO_001_OnlyAFourSegmentAddressNamesARepository(t *testing.T) {
	for _, path := range []string{
		"/api/probe/it", "/api/probe/it/", "/api/probe/local",
		"/api/probe/i%74/local/x", "/api/probe/it/loc%61l/x", "/api/probe/it%2Flocal/x", "/api/probe/it/local%7Ecache/x",
		"/api/probe/It/local/x", "/api/probe/it/%7Ecache/x", "/api/probe/it%7Ecache/local/x",
	} {
		r := newRig(t)
		rec := r.do("GET", path)
		if rec.Code != 404 || len(r.decider.asked) != 0 || len(r.repos.lookup) != 0 || len(r.reached) != 0 {
			t.Errorf("%s: %d asked %v looked up %v reached %v", path, rec.Code, r.decider.asked, r.repos.lookup, r.reached)
		}
	}
}

// A project the caller cannot see answers as a missing repository: the decision runs on the
// qualified name before any lookup, so no answer tells a hidden repository from an absent one
// (R-AUTHZ-016).
func TestR_AUTHZ_016_AProjectTheCallerCannotSeeAnswersAsAMissingRepository(t *testing.T) {
	r := newRig(t)
	secret := qname.Repo{Project: "secret", Key: "local"}
	r.repos.byKey[secret] = repository.Repository{ID: "30", Name: secret, Class: repository.Local, Format: "probe"}
	r.decider.by = func(n qname.Repo) authz.Decision {
		if n.Project == "it" && n.Key != "ghost" {
			return authz.Decision{Outcome: authz.Allow}
		}
		return authz.Decision{Outcome: authz.DenyHidden}
	}
	hidden := r.do("GET", "/api/probe/secret/local/left-pad")
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"hidden":          hidden,
		"missing project": r.do("GET", "/api/probe/nowhere/ghost/left-pad"),
		"missing key":     r.do("GET", "/api/probe/it/ghost/left-pad"),
	} {
		if rec.Code != 404 || rec.Header().Get("X-Probe-Error") != "not_found" ||
			rec.Header().Get("X-Probe-Detail") != hidden.Header().Get("X-Probe-Detail") {
			t.Errorf("%s: %d %v", name, rec.Code, rec.Header())
		}
	}
	if len(r.repos.lookup) != 0 || len(r.reached) != 0 {
		t.Errorf("a refused decision looked up %v and reached %v", r.repos.lookup, r.reached)
	}
}
