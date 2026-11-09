//go:build integration

package deblocal

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// perfGate switches the p99 bound on, as for P-13 and P-14 (Q-ARCH-220): the per-change tier asserts that every upload is published and
// the p50, and the nightly performance run, which sets TROVE_PERF_GATE=1, also the p99 under the reference load.
func perfGate() bool { return os.Getenv("TROVE_PERF_GATE") == "1" }

func percentile(d []time.Duration, q float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(q*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}

// seed stores n package files of distinct packages in the suites as the upload leaves them (an item with its blob row, the package file
// and its coordinate), by SQL: a build reads rows and no package file (R-DEB-010), so this is the load a repository of that size puts on
// it, without n uploads.
func (e *env) seed(n int, suites ...string) {
	e.t.Helper()
	e.st.Exec(`INSERT INTO blobs (sha256, sha1, md5, sha512, size)
		SELECT sha256(('seed' || g)::bytea), decode(repeat('00', 20), 'hex'), decode(md5('seed' || g), 'hex'), decode(repeat('ab', 64), 'hex'), 1000
		FROM generate_series(1, $1::int) g ON CONFLICT DO NOTHING`, n)
	e.st.Exec(`INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
		SELECT r.id, 'pool/s/seed' || g || '/seed' || g || '_1.0-1_amd64.deb', 'pool/s/seed' || g, 'file', sha256(('seed' || g)::bytea), 1000
		FROM generate_series(1, $1::int) g, repositories r WHERE r.key = $2`, n, repo)
	e.st.Exec(`INSERT INTO deb_binaries (item_id, repo_id, package, version, architecture, source, control)
		SELECT i.id, i.repo_id, 'seed' || substr(i.path, 12, position('/' in substr(i.path, 12)) - 1) || '', '1.0-1', 'amd64', '',
		       'Package: ' || 'seed' || substr(i.path, 12, position('/' in substr(i.path, 12)) - 1) || E'\nVersion: 1.0-1\nArchitecture: amd64\nMaintainer: seed <seed@example.org>\nDescription: seeded\n'
		FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE 'pool/s/seed%'`, repo)
	for _, s := range suites {
		e.st.Exec(`INSERT INTO deb_suites (repo_id, suite, dirty_since) SELECT id, $2, now() FROM repositories WHERE key = $1 ON CONFLICT DO NOTHING`, repo, s)
	}
	e.st.Exec(`INSERT INTO deb_coordinates (item_id, repo_id, suite, component)
		SELECT b.item_id, b.repo_id, (ARRAY[` + quoted(suites) + `])[1 + (b.item_id % ` + strconv.Itoa(len(suites)) + `)::int], 'main'
		FROM deb_binaries b JOIN items i ON i.id = b.item_id WHERE i.path LIKE 'pool/s/seed%'`)
}

func quoted(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += "'" + s + "'"
	}
	return out
}

var metricLine = regexp.MustCompile(`(?m)^(trove_deb_index_[a-z_]+)\{([^}]*)\} ([0-9.e+-]+)$`)

// metric sums a series of the metrics of all replicas whose labels contain the given text.
func (e *env) metric(name, labels string) float64 {
	e.t.Helper()
	var sum float64
	for _, r := range e.rs {
		for _, m := range metricLine.FindAllStringSubmatch(r.Metrics(e.t), -1) {
			if m[1] == name && (labels == "" || strings.Contains(m[2], labels)) {
				v, _ := strconv.ParseFloat(m[3], 64)
				sum += v
			}
		}
	}
	return sum
}

// R-DEB-010: from the answer of an upload to the commit of a generation that lists it, p99 at most deb.index_delay + 5 s while Valkey is
// available and deb.index_delay + jobs.poll_interval + 5 s while it is not, for a local repository of 10,000 package files in three
// suites under ten uploads per second spread over three replicas. The per-change tier runs a smaller load and asserts that every upload
// is published and the p50; the nightly performance run (TROVE_PERF_GATE=1) runs the reference load and asserts the p99. The delay
// metric of R-DEB-033 is read afterwards.
func TestR_DEB_010_DelayBound(t *testing.T) {
	const indexDelay, poll = time.Second, 2 * time.Second
	for _, mode := range []struct {
		name   string
		valkey bool
		bound  time.Duration
	}{
		{"with Valkey", true, indexDelay + 5*time.Second},
		{"with Valkey stopped", false, indexDelay + poll + 5*time.Second},
	} {
		t.Run(mode.name, func(t *testing.T) {
			files, perSecond, duration := 150, 5, 4*time.Second
			if perfGate() {
				files, perSecond, duration = 10_000, 10, 90*time.Second
			}
			e := newEnvOpts(t, envOpts{valkey: mode.valkey, replicas: 3, env: []string{"TROVE_JOBS_POLL_INTERVAL=" + poll.String()}})
			suites := []string{"stable", "testing", "unstable"}
			e.seed(files, suites...)
			// The first build lists the whole repository; the load starts after it.
			warm := e.mustUpload(e.tokA, "warm_1.0-1_amd64.deb", build(t, "warm", "1.0-1", nil), 201, suites)
			for _, s := range suites {
				e.waitIndexOn(e.rs[0], s, seqOf(e, warm, s))
			}

			var mu sync.Mutex
			var delays []time.Duration
			var wg sync.WaitGroup
			total := int(duration.Seconds()) * perSecond
			tick := time.NewTicker(time.Second / time.Duration(perSecond))
			defer tick.Stop()
			for i := 0; i < total; i++ {
				<-tick.C
				wg.Add(1)
				go func() {
					defer wg.Done()
					name := fmt.Sprintf("load%04d", i)
					suite := suites[i%len(suites)]
					up := e.rs[i%3]
					a := e.st.Do(up, "PUT", uploadPath(name+"_1.0-1_amd64.deb", []string{suite}), e.tokA, build(t, name, "1.0-1", nil), "Content-Type", debMedia)
					answered := time.Now()
					if a.Status != 201 {
						t.Errorf("upload %s = %d %s", name, a.Status, a.Body)
						return
					}
					seq := seqOf(e, a.JSON(t), suite)
					// Another replica answers the wait: the publication is for all of them.
					e.waitIndexOn(e.rs[(i+1)%3], suite, seq)
					mu.Lock()
					delays = append(delays, time.Since(answered))
					mu.Unlock()
				}()
			}
			wg.Wait()
			if len(delays) != total {
				t.Fatalf("%d of %d uploads were published", len(delays), total)
			}
			p50, p99 := percentile(delays, 0.5), percentile(delays, 0.99)
			t.Logf("delay of %d uploads on %d files: p50 %s p99 %s max %s (bound %s)", len(delays), files, p50, p99, percentile(delays, 1), mode.bound)
			if p50 > mode.bound {
				t.Errorf("p50 %s is above the bound %s", p50, mode.bound)
			}
			switch {
			case p99 <= mode.bound:
			case !perfGate():
				t.Logf("p99 %s is above %s; enforced with TROVE_PERF_GATE=1 (Q-ARCH-220)", p99, mode.bound)
			default:
				t.Errorf("p99 %s is above the bound %s", p99, mode.bound)
			}

			if n := e.metric("trove_deb_index_build_seconds_count", ""); n > 0 {
				t.Logf("%v builds, mean %.2f s each, %v of them published; mean delay measured %.2f s", n, e.metric("trove_deb_index_build_seconds_sum", "")/n,
					e.metric("trove_deb_index_builds_total", `outcome="published"`), e.metric("trove_deb_index_delay_seconds_sum", "")/max(1, e.metric("trove_deb_index_delay_seconds_count", "")))
			}
			// R-DEB-033: the builds were counted and their delay measured.
			if n := e.metric("trove_deb_index_builds_total", `outcome="published"`); n < 1 {
				t.Errorf("trove_deb_index_builds_total{outcome=\"published\"} = %v", n)
			}
			if n := e.metric("trove_deb_index_delay_seconds_count", `class="local"`); n < 1 {
				t.Errorf("trove_deb_index_delay_seconds_count = %v", n)
			}
			if sum, n := e.metric("trove_deb_index_delay_seconds_sum", ""), e.metric("trove_deb_index_delay_seconds_count", ""); n > 0 && sum/n > mode.bound.Seconds() {
				t.Errorf("the mean delay %.1f s is above the bound", sum/n)
			}
		})
	}
}

// waitIndexOn is waitIndex through one replica.
func (e *env) waitIndexOn(r *testenv.Replica, suite string, seq int64) suiteState {
	e.t.Helper()
	path := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/index?waitFor=" + suite + ":" + strconv.FormatInt(seq, 10)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		a := e.st.Do(r, "GET", path, e.tokA, nil)
		if a.Status != 200 {
			e.t.Fatalf("index state = %d %s", a.Status, a.Body)
		}
		var body struct {
			Suites []suiteState `json:"suites"`
		}
		if err := json.Unmarshal(a.Body, &body); err != nil {
			e.t.Fatal(err)
		}
		for _, s := range body.Suites {
			if s.Distribution == suite && s.PublishedSeq >= seq {
				return s
			}
		}
	}
	e.t.Fatalf("suite %s did not publish change %d", suite, seq)
	return suiteState{}
}
