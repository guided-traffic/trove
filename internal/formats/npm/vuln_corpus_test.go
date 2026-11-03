package npm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
	"github.com/guided-traffic/trove/internal/vuln/match"
)

const (
	corpusRecords  = "../../../test/osvstub/testdata/real"
	corpusExpected = "../../vuln/match/testdata/osv-scanner-expected.json"
)

type corpusRecord struct {
	ID        string `json:"id"`
	Withdrawn string `json:"withdrawn"`
	Affected  []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string              `json:"type"`
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
		Versions []string `json:"versions"`
	} `json:"affected"`
}

// entries decodes the affected entries of a frozen record for one name of the adapter's ecosystem as the import of R-VULN-006
// does: names and versions normalized by the adapter, an entry of one range [introduced 0] and no versions[] is an every-version
// entry.
func (r corpusRecord) entries(a match.Adapter, name string) []advisory.Affected {
	var out []advisory.Affected
	for _, x := range r.Affected {
		if x.Package.Ecosystem != a.Ecosystem() {
			continue
		}
		if n, ok := a.AdvisoryName(x.Package.Name); !ok || n != name {
			continue
		}
		e := advisory.Affected{Name: name}
		for _, v := range x.Versions {
			if n, ok := a.AdvisoryVersion(v); ok {
				e.Versions = append(e.Versions, n)
			}
		}
		for _, rg := range x.Ranges {
			if rg.Type != "SEMVER" && rg.Type != "ECOSYSTEM" {
				continue
			}
			ar := advisory.Range{Type: advisory.RangeType(rg.Type)}
			for _, ev := range rg.Events {
				for k, v := range ev {
					ar.Events = append(ar.Events, advisory.Event{Kind: advisory.EventKind(k), Version: v})
				}
			}
			e.Ranges = append(e.Ranges, ar)
		}
		if len(e.Versions) == 0 && len(e.Ranges) == 1 && len(e.Ranges[0].Events) == 1 && e.Ranges[0].Events[0] == (advisory.Event{Kind: advisory.EventIntroduced, Version: "0"}) {
			e.EveryVersion = true
		}
		out = append(out, e)
	}
	return out
}

// corpusAgainstOSVScanner checks the matcher with the format's adapter against the frozen real records: for every package of the
// ecosystem and every candidate version the results are those osv-scanner's offline mode gave for the same records (`make
// osv-expected` freezes them). A withdrawn record affects nothing, as osv-scanner reports none of it.
func corpusAgainstOSVScanner(t *testing.T, a match.Adapter) {
	t.Helper()
	var exp struct {
		Ecosystems map[string]map[string]struct {
			Candidates []string            `json:"candidates"`
			Affected   map[string][]string `json:"affected"`
		} `json:"ecosystems"`
	}
	b, err := os.ReadFile(corpusExpected)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	var records []corpusRecord
	files, err := filepath.Glob(filepath.Join(corpusRecords, a.Ecosystem(), "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no frozen records of %s: %v", a.Ecosystem(), err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var r corpusRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	pkgs := exp.Ecosystems[a.Ecosystem()]
	for name, p := range pkgs { // the frozen names are as osv-scanner reports them; the matcher takes the adapter's form
		if n, ok := a.AdvisoryName(name); ok && n != name {
			pkgs[n] = p
			delete(pkgs, name)
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("the frozen results hold no package of %s", a.Ecosystem())
	}
	checked := 0
	for name, p := range pkgs {
		for _, r := range records {
			entries := r.entries(a, name)
			if len(entries) == 0 {
				continue
			}
			for _, v := range p.Candidates {
				nv, ok := a.AdvisoryVersion(v)
				if !ok {
					continue
				}
				want := slices.Contains(p.Affected[r.ID], v)
				got := false
				if r.Withdrawn == "" {
					for _, e := range entries {
						got = got || match.Affects(a, e, nv).Affected
					}
				}
				if got != want {
					t.Errorf("%s %s@%s: affected = %v, osv-scanner says %v", r.ID, name, v, got, want)
				}
				checked++
			}
		}
	}
	if checked < 100 {
		t.Errorf("only %d package versions checked", checked)
	}
}

// R-VULN-009: the real-record corpus, 13 frozen npm records, gives for every version of the affected packages the result of
// osv-scanner's offline mode.
func TestR_VULN_009_NPMCorpusAgainstOSVScanner(t *testing.T) { corpusAgainstOSVScanner(t, New()) }
