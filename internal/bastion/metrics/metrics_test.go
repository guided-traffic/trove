package metrics

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// specNames reads the metric names out of the table of R-BAS-040 in the specification.
func specNames(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/spec/bastion.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	from := strings.Index(text, "### R-BAS-040 ·")
	to := strings.Index(text, "### R-BAS-041 ·")
	if from < 0 || to < from {
		t.Fatal("the specification has no R-BAS-040 section")
	}
	table := text[from:to]
	if end := strings.Index(table, "- The cache hit rate"); end > 0 {
		table = table[:end]
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile("`(trove_bastion_[a-z_]+)`").FindAllStringSubmatch(table, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestR_BAS_040_EveryMetricOfTheTableIsDefinedOnceAndNoOtherIs: the names of this package, the series of a
// registry and the table of the specification are the same set.
func TestR_BAS_040_EveryMetricOfTheTableIsDefinedOnceAndNoOtherIs(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(reg); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var gathered []string
	for _, f := range families {
		gathered = append(gathered, f.GetName())
	}
	sort.Strings(gathered)
	names := Names()
	sort.Strings(names)
	want := specNames(t)
	if !slices.Equal(names, want) {
		t.Errorf("Names() differs from the table of R-BAS-040:\n got  %v\n want %v", names, want)
	}
	// A gauge without a series (a vector never touched) is not gathered; the seeded ones and the plain
	// collectors are, so every name of the table must be there.
	for _, n := range want {
		if !slices.Contains(gathered, n) {
			t.Errorf("%s is in the table and not scrapeable from the start", n)
		}
	}
}

// TestR_BAS_040_RegisteringTwiceFails: a second registration on one registerer is an error, not a panic.
func TestR_BAS_040_RegisteringTwiceFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := New(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := New(reg); err == nil {
		t.Fatal("a second registration succeeded")
	}
}
