//go:build integration

package nuget

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestR_NUGET_034_VersionIncludesAndExcludesCompareTheCanonicalForm: a version name in a rule's
// include or exclude list is compared as written with the form the format asks about, the
// normalized lower-case URL form of R-NUGET-003 (Q-NUGET-62). In that form an exclude hides a
// version; the range of the same rule is compared after normalization, so "2.0" there means 2.0.0
// or higher.
func TestR_NUGET_034_VersionIncludesAndExcludesCompareTheCanonicalForm(t *testing.T) {
	x := newExposureEnv(t, 1)
	r := x.reps[0]
	put := func(rule string) {
		t.Helper()
		g := x.api("GET", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", nil)
		a := x.api("PUT", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", []byte(rule), "If-Match", g.Header.Get("ETag"))
		if a.Status != http.StatusOK {
			t.Fatalf("update the rule: %d %s", a.Status, a.Body)
		}
		x.epoch = a.Header.Get("Trove-Authz-Epoch")
	}
	versions := func() []string {
		t.Helper()
		return listed(t, x.onCustomers(r, "GET", "v3/flatcontainer/acme.sdk/index.json", nil))
	}

	put(strings.NewReplacer(`"exclude":[],"range":"2.0"`, `"exclude":["2.0.0","2.1.0-beta.1"],"range":"1.0"`, `"prereleases":false`, `"prereleases":true`).Replace(sdkRule))
	if got := versions(); !slices.Equal(got, []string{"1.9.0", "2.1.0"}) {
		t.Fatalf("excludes in the canonical form: %v", got)
	}
	put(strings.NewReplacer(`"include":[],"exclude":[],"range":"2.0"`, `"include":["2.0.0","2.1.0"],"exclude":[],"range":"1.0"`).Replace(sdkRule))
	if got := versions(); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
		t.Fatalf("includes in the canonical form: %v", got)
	}
	// A range written in any spelling the NuGet client reads, with pre-releases switched on:
	// 2.1.0-beta.1 is below 2.1.0 and above 2.0.0, so "[2.0.0.0, 2.1.0)" holds it.
	put(strings.NewReplacer(`"range":"2.0"`, `"range":"[2.0.0.0, 2.1.0)"`, `"prereleases":false`, `"prereleases":true`).Replace(sdkRule))
	if got := versions(); !slices.Equal(got, []string{"2.0.0", "2.1.0-beta.1"}) {
		t.Fatalf("range in another spelling: %v", got)
	}
	// The rule is refused when its range is one the NuGet client's parser refuses.
	g := x.api("GET", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", nil)
	bad := strings.Replace(sdkRule, `"range":"2.0"`, `"range":"1.*"`, 1)
	if a := x.api("PUT", "/api/v1/endpoints/customers/exposure-rules/acme-sdk", []byte(bad), "If-Match", g.Header.Get("ETag")); a.Status != http.StatusUnprocessableEntity && a.Status != http.StatusBadRequest {
		t.Fatalf("a floating range: %d %s", a.Status, a.Body)
	}
}
