package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// noTarget is the resolution of a virtual repository that has no deployment target (R-REPO-021).
type noTarget struct{ proceedResolver }

func (noTarget) DeployTarget(context.Context, *formats.Request) (*repository.Repository, error) {
	return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget, "The virtual repository has no default deployment target, so it takes no deploys.")
}

func memberOf(key string) vmember {
	return vmember{pm: formats.PlannedMember{Member: formats.RepoRef{Name: qname.Repo{Project: "it", Key: key}}}}
}

func rqWith(rules bool) *formats.Request {
	rq := &formats.Request{}
	rq.Endpoint.Rules = rules
	return rq
}

// ok writes a 200 with a body, as a member that has the item does.
func ok(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("x"))
	return nil
}

func TestAWalkServesTheFirstMemberThatHasTheItemAndNamesIt(t *testing.T) {
	calls := 0
	steps := []step{
		{memberOf("first"), func(http.ResponseWriter) error { calls++; return errManifestUnknown() }},
		{memberOf("second"), func(w http.ResponseWriter) error { calls++; return ok(w) }},
		{memberOf("third"), func(w http.ResponseWriter) error { calls++; return ok(w) }},
	}
	rec := httptest.NewRecorder()
	if err := walk(context.Background(), rqWith(false), rec, steps, errManifestUnknown); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || rec.Header().Get("Trove-Resolved-From") != "second" {
		t.Fatalf("calls %d, resolved from %q", calls, rec.Header().Get("Trove-Resolved-From"))
	}
}

func TestAWalkNamesNoMemberOnAnEndpointWithRules(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := walk(context.Background(), rqWith(true), rec, []step{{memberOf("a"), ok}}, errManifestUnknown); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Trove-Resolved-From"); got != "" {
		t.Fatalf("Trove-Resolved-From = %q on an endpoint with exposure rules", got)
	}
}

func TestAMemberThatFailsDoesNotMakeTheItemUnknown(t *testing.T) {
	down := errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "docker-hub did not answer.")
	steps := []step{
		{memberOf("a"), func(http.ResponseWriter) error { return errManifestUnknown() }},
		{memberOf("hub"), func(http.ResponseWriter) error { return down }},
		{memberOf("c"), func(http.ResponseWriter) error { return errManifestUnknown() }},
	}
	err := walk(context.Background(), rqWith(false), httptest.NewRecorder(), steps, errManifestUnknown)
	if errs.KindOf(err) != errs.UpstreamFailed {
		t.Fatalf("err = %v, want the failure of the member that could not be asked", err)
	}
	// With every member answering "not found", the item is unknown.
	steps[1].run = func(http.ResponseWriter) error { return errManifestUnknown() }
	if err := walk(context.Background(), rqWith(false), httptest.NewRecorder(), steps, errManifestUnknown); errs.KindOf(err) != errs.NotFound {
		t.Fatalf("err = %v, want 404", err)
	}
	// A member the caller may not fill counts as a miss, not as a refusal of the request.
	steps[1].run = func(http.ResponseWriter) error {
		return errs.E(errs.Forbidden, errs.CodePopulateNotPermitted, "not permitted")
	}
	if err := walk(context.Background(), rqWith(false), httptest.NewRecorder(), steps, errManifestUnknown); errs.KindOf(err) != errs.NotFound {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestAFailureOnAnEndpointWithRulesNamesNoMember(t *testing.T) {
	rq := rqWith(true)
	rq.Repo = &repository.Repository{Name: qname.Repo{Project: "it", Key: "virtual"}}
	down := errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream of docker-hub did not answer.")
	err := walk(context.Background(), rq, httptest.NewRecorder(), []step{{memberOf("hub"), func(http.ResponseWriter) error { return down }}}, errManifestUnknown)
	if e, _ := errs.As(err); e == nil || e.Kind != errs.UpstreamFailed || e.Detail == down.Detail {
		t.Fatalf("err = %v", err)
	}
}

func TestAnAnswerThatHasStartedEndsTheWalk(t *testing.T) {
	later := false
	steps := []step{
		{memberOf("a"), func(w http.ResponseWriter) error {
			w.WriteHeader(http.StatusOK)
			return errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "cut")
		}},
		{memberOf("b"), func(w http.ResponseWriter) error { later = true; return ok(w) }},
	}
	if err := walk(context.Background(), rqWith(false), httptest.NewRecorder(), steps, errManifestUnknown); err == nil || later {
		t.Fatalf("err = %v, later member asked = %v", err, later)
	}
}

func TestATagsListUnionIsSortedWithoutRepeatsAndNamesTheMemberOfEachTag(t *testing.T) {
	a := formats.MemberDocument{Member: formats.RepoRef{Name: qname.Repo{Project: "it", Key: "hub"}}, Tier: formats.TierRemote, Position: 0, Doc: taglistDoc("app", "latest", "2", "10")}
	b := formats.MemberDocument{Member: formats.RepoRef{Name: qname.Repo{Project: "it", Key: "mine"}}, Tier: formats.TierLocal, Position: 1, Doc: taglistDoc("app", "latest", "1")}
	out, err := virtualAdapter{}.Merge(context.Background(), nil, formats.Path{Path: "app/_taglist"}, []formats.MemberDocument{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var got mergedTaglist
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1", "10", "2", "latest"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("tags = %v, want %v", got.Tags, want)
	}
	source := map[string]string{}
	for i, tag := range got.Tags {
		source[tag] = got.Members[got.From[i]]
	}
	if source["latest"] != "mine" || source["2"] != "hub" || source["1"] != "mine" {
		t.Fatalf("sources = %v: a local member's tag is not preferred over a remote member's that comes first", source)
	}
}

func TestOnlyTheTagsListIsAMergedDocument(t *testing.T) {
	va := virtualAdapter{}
	for _, p := range []string{"app/_manifests/" + digestZero, "app/_blobs/" + digestZero, "app/_tags/1", "team/app/_manifests/" + digestZero} {
		if va.Strategy(formats.Path{Path: p}) != formats.FirstMatch {
			t.Errorf("%s is merged", p)
		}
	}
	if va.Strategy(formats.Path{Path: "team/app/_taglist"}) != formats.Merge {
		t.Error("the tags list is not merged")
	}
}

func TestAWriteThroughAVirtualRepositoryWithoutADeployTargetAnswers405(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{Class: formats.Virtual, Resolver: noTarget{}})
	manifest := imageManifestBytes(sha([]byte("{}")), sha([]byte("layer")))
	for _, tc := range []struct{ method, path, query string }{
		{http.MethodPut, "app/manifests/1", ""},
		{http.MethodPost, "app/blobs/uploads/", ""},
		{http.MethodPost, "app/blobs/uploads/", "digest=" + sha([]byte("x"))},
		{http.MethodPatch, "app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", ""},
		{http.MethodPut, "app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", "digest=" + sha([]byte("x"))},
		{http.MethodGet, "app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", ""},
		{http.MethodDelete, "app/blobs/uploads/0e4b8d38-39a4-4d3e-9c1e-5a0d0f6c2a11", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp := fx.request(tc.method, tc.path, manifest, ctImage, func(r *http.Request) { r.URL.RawQuery = tc.query })
			fx.wantStatus(resp, http.StatusMethodNotAllowed, "UNSUPPORTED")
		})
	}
}

func TestADeleteThroughAVirtualRepositoryAnswers405(t *testing.T) {
	fx := newFixture(t, formattest.HostOptions{Class: formats.Virtual})
	for _, path := range []string{"app/manifests/1", "app/manifests/" + digestZero, "app/blobs/" + digestZero} {
		resp := fx.request(http.MethodDelete, path, nil, "")
		fx.wantStatus(resp, http.StatusMethodNotAllowed, "UNSUPPORTED")
		if resp.Header().Get("Allow") == "" {
			t.Errorf("DELETE %s: no Allow header", path)
		}
	}
}
