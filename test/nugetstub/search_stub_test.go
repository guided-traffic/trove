package nugetstub

import (
	"slices"
	"testing"
)

// TestR_NUGET_016_TheStubSearchCarriesDownloadCountsOwnersAndVerified: the search resource of the
// test upstream answers with what a forwarded search must keep (R-NUGET-016): a download count per
// version, a total over every version of the package (not the sum of the versions listed), the
// owners and the verified flag; every request counts as a search.
func TestR_NUGET_016_TheStubSearchCarriesDownloadCountsOwnersAndVerified(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetListed(UtilID, "2.0.0", false); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Data []struct {
			ID             string   `json:"id"`
			Owners         []string `json:"owners"`
			Verified       bool     `json:"verified"`
			TotalDownloads int64    `json:"totalDownloads"`
			Versions       []struct {
				Version   string `json:"version"`
				Downloads int64  `json:"downloads"`
			} `json:"versions"`
		} `json:"data"`
	}
	f.getJSON(f.resource("SearchQueryService")+"?q="+exactIDField+"contoso.util", &res)
	if len(res.Data) != 1 {
		t.Fatalf("results %+v", res.Data)
	}
	d := res.Data[0]
	if !slices.Equal(d.Owners, []string{SearchOwner}) || !d.Verified {
		t.Errorf("owners %v, verified %v", d.Owners, d.Verified)
	}
	// 2.0.0 is unlisted: the result lists 2.1.0 alone, with the count of the second version, and
	// the total covers both.
	if len(d.Versions) != 1 || d.Versions[0].Version != "2.1.0" || d.Versions[0].Downloads != VersionDownloads(1) {
		t.Errorf("versions %+v", d.Versions)
	}
	if want := VersionDownloads(0) + VersionDownloads(1); d.TotalDownloads != want {
		t.Errorf("totalDownloads %d, want %d", d.TotalDownloads, want)
	}
	if n := f.counts().Searches(); n != 1 {
		t.Errorf("searches %d, want 1", n)
	}
}

// TestR_NUGET_016_TheStubSearchCarriesTheDeprecationAndAdvisoriesOfTheVersionItNames: a search
// result carries the deprecation and the vulnerabilities of the version it names, in nuget.org's
// JSON-LD form with the upstream's @id and @type, one advisory a file:// address, so a forwarded
// search must clean them (Q-NUGET-85).
func TestR_NUGET_016_TheStubSearchCarriesTheDeprecationAndAdvisoriesOfTheVersionItNames(t *testing.T) {
	f := newFixture(t, Config{})
	var res struct {
		Data []struct {
			ID          string `json:"id"`
			Deprecation struct {
				ID        string   `json:"@id"`
				Reasons   []string `json:"reasons"`
				Alternate struct {
					ID      string `json:"@id"`
					Package string `json:"id"`
				} `json:"alternatePackage"`
			} `json:"deprecation"`
			Vulnerabilities []struct {
				ID          string `json:"@id"`
				AdvisoryURL string `json:"advisoryUrl"`
			} `json:"vulnerabilities"`
		} `json:"data"`
	}
	f.getJSON(f.resource("SearchQueryService")+"?q="+exactIDField+"fabrikam.legacy", &res)
	if len(res.Data) != 1 || res.Data[0].ID != LegacyID {
		t.Fatalf("results %+v", res.Data)
	}
	d := res.Data[0]
	if d.Deprecation.ID == "" || d.Deprecation.Alternate.ID == "" || d.Deprecation.Alternate.Package != NewtonsoftID || !slices.Equal(d.Deprecation.Reasons, []string{"Legacy"}) {
		t.Errorf("deprecation %+v", d.Deprecation)
	}
	if len(d.Vulnerabilities) != 2 || d.Vulnerabilities[0].ID == "" || d.Vulnerabilities[0].AdvisoryURL != Advisory || d.Vulnerabilities[1].AdvisoryURL != LocalAdvisory {
		t.Errorf("vulnerabilities %+v", d.Vulnerabilities)
	}
	// A result whose version carries neither has neither member.
	var plain struct {
		Data []map[string]any `json:"data"`
	}
	f.getJSON(f.resource("SearchQueryService")+"?q="+exactIDField+"newtonsoft.json", &plain)
	if len(plain.Data) != 1 || plain.Data[0]["deprecation"] != nil || plain.Data[0]["vulnerabilities"] != nil {
		t.Errorf("Newtonsoft.Json 13.0.3: %+v", plain.Data)
	}
}
