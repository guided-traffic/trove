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
