//go:build integration

package vulnfindings

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// histogramTotals returns the _sum and _count of the series of a histogram whose line holds selector; scraped before and after
// one call, the differences are that call's own observation.
func histogramTotals(text, name, selector string) (sum float64, count int64, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, selector) {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[strings.LastIndex(line, " ")+1:]), 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, name+"_sum{"):
			sum = v
		case strings.HasPrefix(line, name+"_count{"):
			count, ok = int64(v), true
		}
	}
	return sum, count, ok
}

// TestR_VULN_028_TheEvaluationOfAPublishIsMeasured: the evaluation a publish adds (V-4 of R-VULN-028, p99 5 ms) against
// advisory data at the size of the npm archive (231,000 records, one affected entry each, so that the name lookup reads an index
// of the real depth). 300 versions of names the data does not hold and of names it holds at another version are published to a
// repository that claims a name at its first publish, so that the name is internal and an advisory that names it is recorded
// as a claimed-name match (R-VULN-010), as in the performance run; the evaluation of each is read from the change of the duration histogram's sum
// between two scrapes, as the performance run does (Q-VULN-76). The median and the p99 are logged and bounded with the
// performance gate (TROVE_PERF_GATE) only: the shared stack's load is not the reference environment's. The evaluation reads in
// one round trip and writes the claimed-name match of an internal name in a second (Q-VULN-82), and a round trip is the cost a
// loaded host multiplies; before the pipeline it took eight.
func TestR_VULN_028_TheEvaluationOfAPublishIsMeasured(t *testing.T) {
	e := newEnv(t)
	e.setComplete("npm", true)
	e.st.Exec(`INSERT INTO vuln_advisory (ecosystem, id, canonical_id, modified, kind, aliases, summary, severity, severity_source, record_sha256)
		SELECT 'npm', 'GHSA-fill-' || g, 'GHSA-fill-' || g, now(), 'vulnerability', '{}', 'a test advisory', 'high', 'cvss', '\x00'
		  FROM generate_series(0, 230999) g`)
	e.st.Exec(`INSERT INTO vuln_affected (ecosystem, advisory_id, ord, name, ranges, versions, every_version, fixed)
		SELECT 'npm', 'GHSA-fill-' || g, 0, 'trove-test-filler-' || lpad(g::text, 4, '0'), '[]'::jsonb, '{1.0.0}', false, '{}'
		  FROM generate_series(0, 230999) g`)
	e.st.Exec(`ANALYZE vuln_advisory`)
	e.st.Exec(`ANALYZE vuln_affected`)
	const metric, series = "trove_vuln_evaluation_duration_seconds", `trigger="publish"`
	e.publish("npm-local", "trove-test-filler-warm", "1.0.0")
	sum, count, _ := histogramTotals(e.rep.Metrics(t), metric, series)
	var samples []time.Duration
	for i := range 300 {
		name := fmt.Sprintf("trove-test-filler-%04d", 100000+i)
		if i%10 == 0 {
			name = fmt.Sprintf("trove-test-filler-%04d", i)
		}
		e.publish("npm-local", name, fmt.Sprintf("1.%d.0", 20+i))
		nextSum, nextCount, ok := histogramTotals(e.rep.Metrics(t), metric, series)
		if !ok || nextCount == count {
			t.Fatalf("publish %d was not evaluated", i)
		}
		samples = append(samples, time.Duration((nextSum-sum)*float64(time.Second)))
		sum, count = nextSum, nextCount
	}
	slices.Sort(samples)
	p := func(q float64) time.Duration { return samples[min(int(q*float64(len(samples))), len(samples)-1)] }
	t.Logf("evaluation of a publish: median %s, p90 %s, p99 %s, max %s", p(0.5), p(0.9), p(0.99), p(1))
	if os.Getenv("TROVE_PERF_GATE") != "" && p(0.99) > 5*time.Millisecond {
		t.Errorf("the p99 of the evaluation of a publish is %s, the bound is 5ms", p(0.99))
	}
}
