//go:build integration

package vulnperf

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/test/osvstub"
)

// TestR_VULN_028_TheSeededInstallation checks the setup of the run: 100,000 stored versions of the stated shape, named like
// the records of the dumps, so that one version in ten has a record to match; and dumps of the stated sizes, 231,000 npm and
// 7,400 NuGet filler records (R-VULN-028).
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
	// Every seeded name is the name of a filler record, so version 1.0.0 of every package is affected by one record.
	for _, id := range []string{"GHSA-fill-0000-0000", "GHSA-fill-4500-0000", "GHSA-fill-8999-0000"} {
		raw, ok := in.stub.Record("npm", id)
		if !ok {
			t.Fatalf("the test source has no record %s", id)
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
		if got := n(`SELECT count(*) FROM packages WHERE name = $1`, rec.Affected[0].Package.Name); got != 1 {
			t.Errorf("record %s names %q, which %d seeded packages carry, want 1", id, rec.Affected[0].Package.Name, got)
		}
	}
	if got := n(`SELECT count(*) FROM packages WHERE name = 'Trove.Test.Filler0999'`); got != 1 {
		t.Errorf("the last NuGet package is missing")
	}
}
