package npm

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
)

// bareView answers the shared half of an exposure view only.
type bareView struct{}

func (bareView) Package(string) bool         { return true }
func (bareView) Version(string, string) bool { return true }

// taggedView also judges dist-tags, as the decision point's view does.
type taggedView struct{ bareView }

func (taggedView) Tag(_, tag, _ string) bool { return tag == "next" }

// TestR_NPM_048_TheViewOfARequestAnswersForDistTagsOrHidesAll: dist-tags are npm's concept, so
// the shared view of the contract does not ask for them (Q-ARCH-257); npm reads them from the
// same value through its own interface, and a view that cannot answer exposes nothing.
func TestR_NPM_048_TheViewOfARequestAnswersForDistTagsOrHidesAll(t *testing.T) {
	rq := func(v formats.ExposureView, rules bool) *formats.Request {
		return &formats.Request{Endpoint: formats.EndpointFacts{Rules: rules, Exposure: v}}
	}
	if v := viewOf(rq(taggedView{}, true)); v == nil || !v.Package("a") || !v.Tag("a", "next", "1.0.0") || v.Tag("a", "beta", "1.0.0") {
		t.Fatalf("a view that judges tags is used as it is: %#v", v)
	}
	v := viewOf(rq(bareView{}, true))
	if v == nil || v.Package("a") || v.Version("a", "1.0.0") || v.Tag("a", "next", "1.0.0") {
		t.Fatalf("a view without Tag must expose nothing: %#v", v)
	}
	if viewOf(rq(nil, false)) != nil {
		t.Fatal("an endpoint without rules filters nothing")
	}
	if v := viewOf(rq(nil, true)); v == nil || v.Package("a") {
		t.Fatal("an endpoint with rules and no view exposes nothing")
	}
}
