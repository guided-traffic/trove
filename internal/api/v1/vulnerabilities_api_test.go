package v1

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/vuln/report"
	"github.com/guided-traffic/trove/internal/vuln/store"
)

// TestR_VULN_020_DetailsAreSanitized: the advisory's Markdown is untrusted; the HTML member is the README sanitizer's output.
func TestR_VULN_020_DetailsAreSanitized(t *testing.T) {
	rd, err := readme.New(readme.Options{MaxSourceBytes: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{o: Options{Packages: PackageServices{Readme: rd}}}
	got := s.detailsHTML("# Title\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1)) <img src=x onerror=alert(1)> **bold**")
	for _, bad := range []string{"<script", "javascript:", "onerror", "alert(1)>"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived in %s", bad, got)
		}
	}
	if !strings.Contains(got, "<strong>bold</strong>") || !strings.Contains(got, "Title") {
		t.Errorf("the Markdown was not rendered: %s", got)
	}
	// Without a renderer the text is escaped, never passed through.
	if got := (&Server{}).detailsHTML("<script>x</script>"); strings.Contains(got, "<script") {
		t.Errorf("unrendered: %s", got)
	}
}

// TestR_VULN_020_ExportRowProtectsFormulas: a cell that starts like a formula gets the apostrophe.
func TestR_VULN_020_ExportRowProtectsFormulas(t *testing.T) {
	f := report.Finding{Row: store.Row{ID: "i", Package: `=HYPERLINK("http://x")`, Summary: "+1", Version: "-1", Repository: "@r", DetectedAt: time.Unix(0, 0), CVSSScore: -1}}
	row := exportRow(f)
	if len(row) != len(exportHeader()) {
		t.Fatalf("%d cells for %d columns", len(row), len(exportHeader()))
	}
	for _, i := range []int{9, 11, 12, 13} {
		if !strings.HasPrefix(row[i], "'") {
			t.Errorf("%s = %q", exportHeader()[i], row[i])
		}
	}
}

func TestR_VULN_020_Patch(t *testing.T) {
	req, err := findingPatch([]byte(`{"status":"dismissed","dismissalReason":"mitigated","comment":null}`))
	if err != nil || req.Status != "dismissed" || !req.HasReason || !req.HasComment || req.Comment != "" {
		t.Fatalf("%+v %v", req, err)
	}
	for _, bad := range []string{`{"status":null}`, `{"status":1}`, `{"id":"x"}`, `[]`} {
		if _, err := findingPatch([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestR_VULN_020_FilterParsing(t *testing.T) {
	spec := findingListSpec()
	for _, bad := range []string{"severity=urgent", "status=x", "kind=y", "blocked=maybe", "blockReason=z", "detectedFrom=now", "unknown=1"} {
		r := httptest.NewRequest("GET", "/x?"+bad, nil)
		lq, err := ParseList(r, spec, apiCfg())
		if err == nil {
			_, err = findingFilter(lq, &report.Snapshot{})
		}
		if err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
