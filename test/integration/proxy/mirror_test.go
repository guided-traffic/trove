//go:build integration

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/repository"
)

// The remote engine's side of the mirror (R-REPO-054, R-REPO-055, R-REPO-057): the background requests of a pass against the
// engine with PostgreSQL, the object store and Valkey, and the counting upstream of internal/proxy/proxytest.

// bg builds the engine's request for a pass's artifact or document: a background request of the job, with the populate check of a
// mirror (it asks nobody).
func (r *replica) bg(rem proxy.Remote, path string, kind proxy.Kind, ceiling int, job string) proxy.Request {
	return r.request(rem, path, kind, func(rq *proxy.Request) {
		rq.Priority, rq.Ceiling, rq.JobID = proxy.PriorityBackground, ceiling, job
		rq.Caller = proxy.CallerFacts{Actor: audit.Actor{Kind: "system", Name: "mirror"}}
	})
}

// fill runs a pass's fill of an artifact and returns its outcome.
func (r *replica) fill(rem proxy.Remote, path string, ceiling int) (proxy.FillOutcome, error) {
	return r.engine.Fill(context.Background(), r.bg(rem, path, proxy.Artifact, ceiling, "job-1"))
}

// held tracks the requests the upstream holds open: a handler that waits until its request is cancelled or its delay is over.
type held struct{ cur, peak atomic.Int64 }

func (h *held) route(delay time.Duration) proxytest.Route {
	return proxytest.Route{Handler: func(w http.ResponseWriter, r *http.Request) {
		n := h.cur.Add(1)
		for {
			p := h.peak.Load()
			if n <= p || h.peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer h.cur.Add(-1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("tarball"))
	}}
}

func histogramSum(r *replica, name, repo string) float64 {
	mfs, err := r.reg.Gather()
	if err != nil {
		panic(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, []string{"repo", repo}) && m.GetHistogram() != nil {
				return m.GetHistogram().GetSampleSum()
			}
		}
	}
	return 0
}

func TestR_REPO_055_AClientFetchPreemptsABackgroundFetchOnOneReplica(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	h := &held{}
	up.Set("/pkg/bg/-/bg-1.tgz", h.route(3*time.Second))
	up.Set("/pkg/c1/-/c1-1.tgz", h.route(2*time.Second))
	up.Set("/pkg/c2/-/c2-1.tgz", proxytest.Route{Body: []byte("c2"), Type: "application/octet-stream"})
	// maxConcurrentFetches 2: the background share is B = 1.
	rem := e.remote("pre", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 2 })
	r := e.newReplica("a", replicaOpts{})
	ceiling := proxy.BackgroundShare(2, 4)

	t.Run("R-REPO-055 the second client fetch pre-empts the background fetch, waits less than 50 ms for its slot and nothing of the pre-empted fetch is stored", func(t *testing.T) {
		if ceiling != 1 {
			t.Fatalf("B = %d, want 1", ceiling)
		}
		done := make(chan error, 1)
		go func() { _, err := r.fill(rem, "pkg/bg/-/bg-1.tgz", ceiling); done <- err }()
		waitFor(t, "the background fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/bg/-/bg-1.tgz") == 1 })
		go r.artifact(rem, "pkg/c1/-/c1-1.tgz")
		waitFor(t, "the first client fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/c1/-/c1-1.tgz") == 1 })

		if s := r.artifact(rem, "pkg/c2/-/c2-1.tgz"); s.status != 200 || string(s.body) != "c2" {
			t.Fatalf("the second client fetch: %d %s", s.status, s.body)
		}
		if wait := histogramSum(r, "trove_remote_fetch_queue_wait_seconds", "pre"); wait > 0.05 {
			t.Fatalf("the client fetches waited %.3f s for their slots in all", wait)
		}
		select {
		case err := <-done:
			if !errors.Is(err, proxy.ErrPreempted) {
				t.Fatalf("the pre-empted fetch returned %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the background fetch was not pre-empted")
		}
		if n := e.number("SELECT count(*) FROM remote_cache_entries WHERE path = 'pkg/bg/-/bg-1.tgz'") +
			e.number("SELECT count(*) FROM items WHERE path = 'pkg/bg/-/bg-1.tgz'"); n != 0 {
			t.Fatalf("a pre-empted fetch left %d rows", n)
		}
		if pk, _ := r.engine.Peek(context.Background(), rem, "pkg/bg/-/bg-1.tgz", ""); pk.Negative {
			t.Fatal("a pre-empted fetch left a negative entry")
		}
		if circuit := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0 OR open_until IS NOT NULL"); circuit != 0 {
			t.Fatal("a pre-emption moved the circuit")
		}
	})

	t.Run("R-REPO-055 the pre-empted item is fetched again later and stored once", func(t *testing.T) {
		waitFor(t, "the first client fetch to end", 6*time.Second, func() bool { return h.cur.Load() == 0 })
		up.Set("/pkg/bg/-/bg-1.tgz", proxytest.Route{Body: []byte("bg-body"), Type: "application/octet-stream"})
		out, err := r.fill(rem, "pkg/bg/-/bg-1.tgz", ceiling)
		if err != nil || out.Present || out.Bytes != int64(len("bg-body")) {
			t.Fatalf("%+v %v", out, err)
		}
		if n := e.number("SELECT count(*) FROM items WHERE path = 'pkg/bg/-/bg-1.tgz'"); n != 1 {
			t.Fatalf("items = %d", n)
		}
	})

	t.Run("R-REPO-055 a stored artifact is Present and costs no upstream request", func(t *testing.T) {
		before := up.Total()
		out, err := r.fill(rem, "pkg/bg/-/bg-1.tgz", ceiling)
		if err != nil || !out.Present || out.Bytes != 0 {
			t.Fatalf("%+v %v", out, err)
		}
		if up.Total() != before {
			t.Fatalf("a stored artifact cost %d upstream requests", up.Total()-before)
		}
	})
}

func TestR_REPO_055_ABackgroundFetchAFollowerJoinedIsNotPreempted(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	h := &held{}
	up.Set("/pkg/bg/-/bg-1.tgz", h.route(1500*time.Millisecond))
	up.Set("/pkg/c1/-/c1-1.tgz", h.route(2500*time.Millisecond))
	up.Set("/pkg/c2/-/c2-1.tgz", proxytest.Route{Body: []byte("c2"), Type: "application/octet-stream"})
	rem := e.remote("joined", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 2 })
	r := e.newReplica("a", replicaOpts{})

	done := make(chan error, 1)
	var bytesStored atomic.Int64
	go func() {
		out, err := r.fill(rem, "pkg/bg/-/bg-1.tgz", 1)
		bytesStored.Store(out.Bytes)
		done <- err
	}()
	waitFor(t, "the background fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/bg/-/bg-1.tgz") == 1 })
	var follower served
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); follower = r.artifact(rem, "pkg/bg/-/bg-1.tgz") }() // joins the flight of the background fetch
	time.Sleep(200 * time.Millisecond)
	go r.artifact(rem, "pkg/c1/-/c1-1.tgz")
	waitFor(t, "the first client fetch to reach the upstream", 5*time.Second, func() bool { return up.Count("GET", "/pkg/c1/-/c1-1.tgz") == 1 })

	// No free slot, and the only background fetch has a follower: the client fetch waits for it.
	start := time.Now()
	if s := r.artifact(rem, "pkg/c2/-/c2-1.tgz"); s.status != 200 {
		t.Fatalf("%d %s", s.status, s.body)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatalf("the client fetch took %v: it took the slot of a joined background fetch", time.Since(start))
	}
	if err := <-done; err != nil {
		t.Fatalf("the joined background fetch returned %v", err)
	}
	wg.Wait()
	if follower.status != 200 || string(follower.body) != "tarball" {
		t.Fatalf("the follower: %d %s", follower.status, follower.body)
	}
	if up.Count("GET", "/pkg/bg/-/bg-1.tgz") != 1 {
		t.Fatalf("the item was fetched %d times", up.Count("GET", "/pkg/bg/-/bg-1.tgz"))
	}
}

func TestR_REPO_055_AClientOnAnotherReplicaMakesTheBackgroundYieldThroughTheSignal(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	h := &held{}
	for _, name := range []string{"b1", "b2"} {
		up.Set("/pkg/"+name+"/-/"+name+"-1.tgz", h.route(8*time.Second))
	}
	for i := range 9 {
		up.Set("/pkg/c"+string(rune('0'+i))+"/-/c"+string(rune('0'+i))+"-1.tgz", h.route(10*time.Second))
	}
	// maxConcurrentFetches 8: B = 2. The replica that serves clients and the one that runs the pass share the installation's
	// Valkey, and only the signal reaches from the first to the second.
	rem := e.remote("yield", up, func(s *repository.RemoteSettings) { s.Limits.MaxConcurrentFetches = 8 })
	clientSide := e.newReplica("clients", replicaOpts{signals: true})
	passSide := e.newReplica("pass", replicaOpts{signals: true})
	ceiling := proxy.BackgroundShare(8, 4)
	if ceiling != 2 {
		t.Fatalf("B = %d, want 2", ceiling)
	}

	results := make(chan error, 2)
	for _, name := range []string{"b1", "b2"} {
		go func() { _, err := passSide.fill(rem, "pkg/"+name+"/-/"+name+"-1.tgz", ceiling); results <- err }()
	}
	waitFor(t, "both background fetches to reach the upstream", 5*time.Second, func() bool { return h.cur.Load() == 2 })
	for i := range 8 {
		go clientSide.artifact(rem, "pkg/c"+string(rune('0'+i))+"/-/c"+string(rune('0'+i))+"-1.tgz")
	}
	waitFor(t, "eight client fetches to take the client replica's slots", 5*time.Second, func() bool { return h.cur.Load() == 10 })
	if w := histogramSum(clientSide, "trove_remote_fetch_queue_wait_seconds", "yield"); w > 0.5 {
		t.Fatalf("the eight client fetches waited %.2f s for slots", w)
	}

	go clientSide.artifact(rem, "pkg/c8/-/c8-1.tgz") // the ninth waits for a slot and raises the signal
	for range 2 {
		select {
		case err := <-results:
			if !errors.Is(err, proxy.ErrPreempted) {
				t.Fatalf("a background fetch returned %v, want a pre-emption", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the process that runs the pass did not pre-empt its background fetches after the signal")
		}
	}
	waitFor(t, "the upstream to hold at most eight connections", time.Second, func() bool { return h.cur.Load() <= 8 })

	// The pass starts nothing for 30 s after the signal: a new background fetch waits.
	up.Set("/pkg/late/-/late-1.tgz", proxytest.Route{Body: []byte("late"), Type: "application/octet-stream"})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := passSide.engine.Fill(ctx, passSide.bg(rem, "pkg/late/-/late-1.tgz", proxy.Artifact, ceiling, "job-1")); err == nil {
		t.Fatal("a background fetch started within 30 s of the signal")
	}
	if up.Count("GET", "/pkg/late/-/late-1.tgz") != 0 {
		t.Fatal("the upstream saw a background request in the window of the signal")
	}
}

func TestR_REPO_054_ABackgroundRequestNeitherMovesNorResetsTheCircuit(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	slowDown := "<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>"
	up.Set("/pkg/limited/-/l-1.tgz", proxytest.Route{Status: 429, Header: map[string]string{"Retry-After": "7"}})
	up.Set("/pkg/slow/-/s-1.tgz", proxytest.Route{Status: 503, Body: []byte(slowDown), Type: "application/xml"})
	up.Set("/pkg/quota/-/q-1.tgz", proxytest.Route{Status: 403, Header: map[string]string{"X-RateLimit-Remaining": "0"}})
	up.Set("/pkg/broken/-/b-1.tgz", proxytest.Route{Status: 500})
	up.Set("/pkg/fine/-/f-1.tgz", proxytest.Route{Body: []byte("fine"), Type: "application/octet-stream"})
	up.Set("/pkg/lag/-/g-1.tgz", proxytest.Route{Delay: 2 * time.Second, Body: []byte("lag"), Type: "application/octet-stream"})
	rem := e.remote("throttle", up, func(s *repository.RemoteSettings) {
		s.Availability.FailureThreshold, s.Availability.CircuitOpenSeconds = 3, 2
		s.ReadTimeoutMs = 400
	})
	r := e.newReplica("a", replicaOpts{})
	circuitRows := func() int64 {
		return e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0 OR open_until IS NOT NULL")
	}

	t.Run("R-REPO-054 a 429, a 503 with SlowDown and a 403 with X-RateLimit-Remaining 0 are rate limits that discover.AsRateLimited finds", func(t *testing.T) {
		for path, want := range map[string]string{"pkg/limited/-/l-1.tgz": "too_many_requests", "pkg/slow/-/s-1.tgz": "slow_down", "pkg/quota/-/q-1.tgz": "quota_header"} {
			_, err := r.fill(rem, path, 2)
			rl, ok := discover.AsRateLimited(err)
			if !ok || rl.RateLimitReason() != want {
				t.Fatalf("%s: %v", path, err)
			}
			if _, ok := errs.As(err); ok {
				t.Fatalf("%s: the rate limit has the shape of a client error: %v", path, err)
			}
		}
		_, err := r.fill(rem, "pkg/limited/-/l-1.tgz", 2)
		if rl, _ := discover.AsRateLimited(err); rl == nil {
			t.Fatal("not a rate limit")
		} else if wait, hinted := rl.RetryAfter(); !hinted || wait != 7*time.Second {
			t.Fatalf("wait %v hinted %v", wait, hinted)
		}
		if n := circuitRows(); n != 0 {
			t.Fatalf("a rate limit moved the circuit (%d rows)", n)
		}
	})

	t.Run("R-REPO-054 a 500 and a timeout are failures of the item, and many of them leave the circuit alone", func(t *testing.T) {
		for range 6 { // above the failure threshold of 3
			_, err := r.fill(rem, "pkg/broken/-/b-1.tgz", 2)
			if code := codeOf(err); code != errs.CodeUpstreamError {
				t.Fatalf("a 500: %v", err)
			}
			if _, ok := discover.AsRateLimited(err); ok {
				t.Fatal("a 500 is no rate limit")
			}
		}
		for range 4 {
			if _, err := r.fill(rem, "pkg/lag/-/g-1.tgz", 2); codeOf(err) != errs.CodeUpstreamTimeout {
				t.Fatalf("a timeout: %v", err)
			}
		}
		if n := circuitRows(); n != 0 {
			t.Fatalf("background failures moved the circuit (%d rows)", n)
		}
		if pk, err := r.engine.Peek(context.Background(), rem, "pkg/fine/-/f-1.tgz", ""); err != nil || pk.CircuitOpenFor != 0 {
			t.Fatalf("%+v %v", pk, err)
		}
	})

	t.Run("R-REPO-029 R-REPO-054 a client's 429 and a client's 503 with SlowDown keep the classification of the circuit: upstream_error counts", func(t *testing.T) {
		s := r.artifact(rem, "pkg/slow/-/s-1.tgz")
		if s.status != 502 || !strings.Contains(string(s.body), errs.CodeUpstreamError) {
			t.Fatalf("a client's 503 SlowDown: %d %s", s.status, s.body)
		}
		if n := e.number("SELECT consecutive_failures FROM remote_upstream_state"); n != 1 {
			t.Fatalf("consecutive_failures = %d after one client failure", n)
		}
		c := r.artifact(rem, "pkg/limited/-/l-1.tgz")
		if c.status != 503 || c.header.Get("Retry-After") == "" {
			t.Fatalf("a client's 429: %d %v", c.status, c.header)
		}
		if pk, _ := r.engine.Peek(context.Background(), rem, "pkg/fine/-/f-1.tgz", ""); pk.CircuitOpenFor <= 0 {
			t.Fatal("a client's 429 did not open the circuit")
		}
	})

	t.Run("R-REPO-054 while the circuit is open a background request is not sent and waits; it never probes", func(t *testing.T) {
		before := up.Total()
		_, err := r.fill(rem, "pkg/fine/-/f-1.tgz", 2)
		var w *proxy.BackgroundWait
		if !errors.As(err, &w) || w.Reason != proxy.WaitCircuitOpen || w.After < time.Second || w.After > 5*time.Second {
			t.Fatalf("%v", err)
		}
		doc := r.bg(rem, "pkg/fine", proxy.Metadata, 2, "job-1")
		if _, err := r.engine.Metadata(context.Background(), doc); !errors.As(err, &w) || w.Reason != proxy.WaitCircuitOpen {
			t.Fatalf("a document: %v", err)
		}
		u, _ := url.Parse(up.URL + "/pkg/fine/-/f-1.tgz")
		if _, err := r.engine.Get(context.Background(), proxy.Get{Remote: rem, URL: u, Ceiling: 2, JobID: "job-1"}); !errors.As(err, &w) || w.Reason != proxy.WaitCircuitOpen {
			t.Fatalf("a Get: %v", err)
		}
		if up.Total() != before {
			t.Fatalf("an open circuit let %d background requests through", up.Total()-before)
		}
	})

	t.Run("R-REPO-054 a background success after the open period does not reset the circuit; a client's does", func(t *testing.T) {
		waitFor(t, "the open period to pass", 5*time.Second, func() bool {
			pk, _ := r.engine.Peek(context.Background(), rem, "pkg/fine/-/f-1.tgz", "")
			return pk.CircuitOpenFor == 0
		})
		out, err := r.fill(rem, "pkg/fine/-/f-1.tgz", 2)
		if err != nil || out.Bytes != 4 {
			t.Fatalf("%+v %v", out, err)
		}
		if n := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0"); n != 1 {
			t.Fatalf("a background success reset the circuit")
		}
		if s := r.artifact(rem, "pkg/fine/-/f-1.tgz"); s.status != 200 {
			t.Fatal(s.status)
		}
		if n := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0"); n != 1 {
			t.Fatal("a hit sends no request and moves nothing")
		}
		up.Set("/pkg/fine2/-/f-1.tgz", proxytest.Route{Body: []byte("fine"), Type: "application/octet-stream"})
		if s := r.artifact(rem, "pkg/fine2/-/f-1.tgz"); s.status != 200 {
			t.Fatal(s.status)
		}
		if n := e.number("SELECT count(*) FROM remote_upstream_state WHERE consecutive_failures > 0"); n != 0 {
			t.Fatal("a client's success did not reset the circuit")
		}
	})
}

// jsonLines parses the records of a JSON log.
func jsonLines(s string) []map[string]any {
	var out []map[string]any
	for _, l := range strings.Split(s, "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestR_REPO_057_TheUpstreamLogRecordOfABackgroundRequestCarriesTheMirrorRoleAndThePassesJob(t *testing.T) {
	e := newEnv(t)
	up := e.upstream()
	up.Set("/pkg/a/-/a-1.tgz", proxytest.Route{Body: []byte("aaa"), Type: "application/octet-stream"})
	up.Set("/pkg/c/-/c-1.tgz", proxytest.Route{Body: []byte("ccc"), Type: "application/octet-stream"})
	up.Set("/pkg/a", proxytest.Route{Body: []byte(`{"name":"a"}`), Type: "application/json"})
	rem := e.remote("logged", up)
	var log syncBuffer
	r := e.newReplica("a", replicaOpts{upstreamLog: &log})

	if _, err := r.engine.Fill(context.Background(), r.bg(rem, "pkg/a/-/a-1.tgz", proxy.Artifact, 2, "job-42")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.engine.Metadata(context.Background(), r.bg(rem, "pkg/a", proxy.Metadata, 2, "job-42")); err != nil {
		t.Fatal(err)
	}
	if s := r.artifact(rem, "pkg/c/-/c-1.tgz"); s.status != 200 {
		t.Fatal(s.status)
	}
	byPath := map[string]map[string]any{}
	for _, l := range jsonLines(log.String()) {
		if u, _ := l["url"].(string); u != "" {
			byPath[u[strings.Index(u, "/pkg/"):]] = l
		}
	}
	for _, p := range []string{"/pkg/a/-/a-1.tgz", "/pkg/a"} {
		l := byPath[p]
		if l == nil || l["flight_role"] != "mirror" || l["job_id"] != "job-42" || l["on_behalf_of"] != "system" {
			t.Fatalf("%s: %v", p, l)
		}
	}
	if l := byPath["/pkg/c/-/c-1.tgz"]; l == nil || l["flight_role"] != "leader" || l["on_behalf_of"] != "it-a" {
		t.Fatalf("a client's record: %v", l)
	} else if _, has := l["job_id"]; has {
		t.Fatalf("a client's record names a job: %v", l)
	}
}

// credentialUpstreams is the fixture of the credential tests: the repository's upstream and a second TLS server that stands for the
// discovery host of a lister (npm's replication host).
func TestR_REPO_051_AMirrorsDiscoveryRequestsKeepToTheHostsAFormatMayReachAndNeverSendTheCredentialToADiscoveryHost(t *testing.T) {
	e := newEnv(t)
	up, other := e.upstream(), e.upstream()
	up.Set("/-/names", proxytest.Route{Body: []byte(`{"from":"upstream"}`), Type: "application/json"})
	other.Set("/-/names", proxytest.Route{Body: []byte(`{"from":"other"}`), Type: "application/json"})
	other.Set("/limited", proxytest.Route{Status: 429, Header: map[string]string{"Retry-After": "3"}})
	other.Set("/big", proxytest.Route{Body: bytes.Repeat([]byte("x"), 4096), Type: "application/json"})
	opts := replicaOpts{
		ups: []*proxytest.Upstream{other}, secrets: bearerSource{},
		env: func(k string) (string, bool) { return "s3cret", k == "TROVE_CAC_IT_QUERY_TOKEN" },
	}
	r := e.newReplica("a", opts)
	bearer := func(st *repository.RemoteSettings) {
		st.AuthMode, st.Password = repository.AuthBearer, &repository.SecretField{Set: true}
	}
	get := func(rem proxy.Remote, target *proxytest.Upstream, path string, hosts ...string) (*proxy.GetResult, error) {
		u, _ := url.Parse(target.URL + path)
		return r.engine.Get(context.Background(), proxy.Get{Remote: rem, URL: u, Accept: "application/json", MirrorHosts: hosts, Ceiling: 2, JobID: "job-9"})
	}
	authOf := func(u *proxytest.Upstream) string {
		seen := u.Seen()
		if len(seen) == 0 {
			t.Fatal("the upstream saw no request")
		}
		return seen[len(seen)-1].Authorization
	}

	t.Run("the upstream of the repository gets the credential and the request counts as the pass's", func(t *testing.T) {
		rem := e.remote("g-origin", up, bearer)
		got, err := get(rem, up, "/-/names")
		if err != nil || got.Status != 200 || !strings.Contains(string(got.Body), "upstream") {
			t.Fatalf("%+v %v", got, err)
		}
		if a := authOf(up); a != "Bearer s3cret" {
			t.Fatalf("Authorization = %q", a)
		}
	})
	t.Run("R-REPO-043 a host that is neither the upstream, an extraHosts entry nor a discovery host is refused and nothing is sent", func(t *testing.T) {
		rem := e.remote("g-foreign", up, bearer)
		if _, err := get(rem, other, "/-/names"); codeOf(err) != errs.CodeForeignHostDenied {
			t.Fatalf("err = %v", err)
		}
		if _, err := get(rem, other, "/-/names", "replicate.example"); codeOf(err) != errs.CodeForeignHostDenied {
			t.Fatalf("a discovery host that is not the host: %v", err)
		}
		if other.Total() != 0 {
			t.Fatalf("the foreign host got %d requests", other.Total())
		}
	})
	t.Run("a discovery host gets the request and no credential, also where an extraHosts entry with sendCredentials matches it", func(t *testing.T) {
		rem := e.remote("g-mirror", up, bearer, func(st *repository.RemoteSettings) {
			st.ExtraHosts = []repository.ExtraHost{{Pattern: "127.0.0.1", SendCredentials: true}}
		})
		got, err := get(rem, other, "/-/names", "127.0.0.1")
		if err != nil || !strings.Contains(string(got.Body), "other") {
			t.Fatalf("%+v %v", got, err)
		}
		if a := authOf(other); a != "" {
			t.Fatalf("the credential went to a discovery host: %q", a)
		}
		// Without the discovery-host rule the same entry of extraHosts sends the credential, as for a client (R-REPO-033).
		other.Reset()
		if _, err := get(rem, other, "/-/names"); err != nil {
			t.Fatal(err)
		}
		if a := authOf(other); a != "Bearer s3cret" {
			t.Fatalf("an extraHosts entry with sendCredentials gets it: %q", a)
		}
	})
	t.Run("R-REPO-054 a rate-limit answer is an error discover.AsRateLimited finds, another status a result, and a body is bounded", func(t *testing.T) {
		rem := e.remote("g-answers", up, bearer)
		_, err := get(rem, other, "/limited", "127.0.0.1")
		rl, ok := discover.AsRateLimited(err)
		if !ok {
			t.Fatalf("%v", err)
		}
		if wait, hinted := rl.RetryAfter(); !hinted || wait != 3*time.Second {
			t.Fatalf("wait %v %v", wait, hinted)
		}
		got, err := get(rem, other, "/missing", "127.0.0.1")
		if err != nil || got.Status != 404 {
			t.Fatalf("%+v %v", got, err)
		}
		u, _ := url.Parse(other.URL + "/big")
		_, err = r.engine.Get(context.Background(), proxy.Get{Remote: rem, URL: u, Max: 1024, MirrorHosts: []string{"127.0.0.1"}})
		if codeOf(err) != errs.CodeUpstreamItemTooLarge {
			t.Fatalf("a body above the bound: %v", err)
		}
	})
}
