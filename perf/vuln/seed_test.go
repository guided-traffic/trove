//go:build integration

package vulnperf

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/npm"
	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/test/osvstub"
)

// TestR_VULN_028_TheSeededInstallation checks the setup of the run: 100,000 stored versions of the stated shape, named like
// the records of the dumps as the formats store a name, so that one version in ten has a record to match: 9,000 npm and
// 1,000 NuGet findings, 10,000 in all; and dumps of the stated sizes, 231,000 npm and 7,400 NuGet filler records (R-VULN-028).
func TestR_VULN_028_TheSeededInstallation(t *testing.T) {
	in := newInstallation(t)
	n := func(q string, args ...any) int64 { return in.st.Number(q, args...) }
	if got := n(`SELECT count(*) FROM package_versions`); got != 100000 {
		t.Errorf("stored versions = %d, want 100000", got)
	}
	if got := n(`SELECT count(*) FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id WHERE r.format = 'npm'`); got != 90000 {
		t.Errorf("npm versions = %d, want 90000", got)
	}
	if got := n(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.format = 'npm'`); got != 9000 {
		t.Errorf("npm packages = %d, want 9000", got)
	}
	for _, repo := range []string{repoNPMLocal, repoNPMRemote} {
		if got := n(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id WHERE r.key = $1`, repo); got != 4500 {
			t.Errorf("%s has %d packages, want 4500", repo, got)
		}
	}

	// The dumps: the filler of every ecosystem besides the fixture, NuGet's lowered to its size.
	fixture := osvstub.New(osvstub.Config{})
	for eco, filler := range map[string]int{"npm": npmFiller, "NuGet": nugetFiller} {
		if got, want := len(in.stub.IDs(eco)), len(fixture.IDs(eco))+filler; got != want {
			t.Errorf("the %s dump holds %d records, want %d (%d filler records and the fixture)", eco, got, want, filler)
		}
	}

	// Every seeded name is the name a filler record's affected package gets from its format's adapter, so version 1.0.0 of
	// every package is affected by one record: the evaluation finds one version per package.
	type adapter interface{ AdvisoryName(string) (string, bool) }
	for _, tt := range []struct {
		eco string
		ad  adapter
		ids []string
	}{
		{"npm", &npm.Format{}, []string{"GHSA-fill-0000-0000", "GHSA-fill-4500-0000", "GHSA-fill-8999-0000"}},
		{"NuGet", &nuget.Format{}, []string{"GHSA-fill-0000-0000", "GHSA-fill-0500-0000", "GHSA-fill-0999-0000"}},
	} {
		for _, id := range tt.ids {
			raw, ok := in.stub.Record(tt.eco, id)
			if !ok {
				t.Fatalf("the %s dump has no record %s", tt.eco, id)
			}
			var rec struct {
				Affected []struct {
					Package struct {
						Name string `json:"name"`
					} `json:"package"`
				} `json:"affected"`
			}
			if err := json.Unmarshal(raw, &rec); err != nil || len(rec.Affected) != 1 {
				t.Fatalf("record %s: %v", id, err)
			}
			name, ok := tt.ad.AdvisoryName(rec.Affected[0].Package.Name)
			if !ok {
				t.Fatalf("the %s adapter refuses the name %q", tt.eco, rec.Affected[0].Package.Name)
			}
			if got := n(`SELECT count(*) FROM packages WHERE name = $1`, name); got != 1 {
				t.Errorf("%s record %s names %q, which %d seeded packages carry, want 1", tt.eco, id, name, got)
			}
		}
	}
	// A NuGet package is stored as a push stores it: the lower-cased ID as its name, the declared ID as its display name.
	if got := n(`SELECT count(*) FROM packages p JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = lower(p.display_name) AND p.name <> p.display_name`, repoNuGet); got != nugetPackages {
		t.Errorf("%d NuGet packages carry the lower-cased ID as their name, want %d", got, nugetPackages)
	}
	if got := n(`SELECT count(*) FROM packages WHERE name = 'trove.test.filler0999' AND display_name = 'Trove.Test.Filler0999'`); got != 1 {
		t.Errorf("the last NuGet package is missing")
	}
}
