//go:build e2e

package e2e

// The floods of R-BAS-049 that fit the short set: the edge limit of R-BAS-038 and the flooding address of R-BAS-037. They
// come last: the address of this machine is blocked at the Bastion afterwards (15 minutes), the client pods' addresses are
// not.

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// floodClient is a client of the Bastion's host from this machine with connections that live on.
func (s *basSite) floodClient(t testing.TB, conns int) *http.Client {
	t.Helper()
	var d net.Dialer
	port := edgePort(t)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
		TLSClientConfig:     &tls.Config{RootCAs: caPool(t), ServerName: s.host, MinVersion: tls.VersionTLS12},
		MaxIdleConnsPerHost: conns,
	}}
}

type floodResult struct {
	sent        int
	codes       map[int]int
	edge429     int // a 429 that is no problem document: HAProxy Ingress answered
	problem429  int // a 429 of the Bastion
	problemCode map[string]int
	took        time.Duration
}

// flood sends n GETs of the path with the token on `workers` goroutines and sorts the answers.
func (s *basSite) flood(t testing.TB, n, workers, ratePerSecond int, path func(i int) string, tokenOf func(i int) string) floodResult {
	t.Helper()
	c := s.floodClient(t, workers)
	res := floodResult{codes: map[int]int{}, problemCode: map[string]int{}}
	var mu sync.Mutex
	var next atomic.Int64
	start := time.Now()
	// ratePerSecond paces the requests (0: as fast as the connections allow): the flood of one address that stays below the ingress's
	// limit reaches the Bastion's own stage
	var gap time.Duration
	if ratePerSecond > 0 {
		gap = time.Second / time.Duration(ratePerSecond)
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				if gap > 0 {
					time.Sleep(time.Until(start.Add(time.Duration(i) * gap)))
				}
				req, _ := http.NewRequest(http.MethodGet, s.baseURL(t)+path(i), nil)
				if tk := tokenOf(i); tk != "" {
					req.Header.Set("Authorization", "Bearer "+tk)
				}
				code, ctype, body := 0, "", ""
				if resp, err := c.Do(req); err == nil {
					code, ctype = resp.StatusCode, resp.Header.Get("Content-Type")
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
					body = string(b)
					resp.Body.Close()
				}
				mu.Lock()
				res.sent++
				res.codes[code]++
				if code == 429 {
					if strings.Contains(ctype, "problem+json") {
						res.problem429++
						res.problemCode[codeOf(body)]++
					} else {
						res.edge429++
					}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	res.took = time.Since(start)
	return res
}

func codeOf(body string) string {
	r := reply{Body: []byte(body)}
	return r.Code()
}

// requestsReached adds the requests the replicas of the release counted (every class and result).
func requestsReached(t testing.TB, s *basSite) float64 {
	return basSum(t, s, "trove_bastion_requests_total")
}

// TestBastion_R_BAS_049_EdgeLimit (R-BAS-038): 20,000 requests from one address within about 10 s are partly refused by the
// ingress (a 429 that is no document of the Bastion, and no request at the Bastion), and the replicas stay below their in-flight
// limit.
func TestBastion_R_BAS_049_EdgeLimit(t *testing.T) {
	env := basFx(t)
	site := env.vol
	doc := npmDocPath(env.open)
	site.get(t, env.full.Token, doc).want(t, "the Bastion serves before the flood", 200)
	before := requestsReached(t, site)
	shedBefore := basSum(t, site, "trove_http_requests_shed_total")
	res := site.flood(t, 20000, 200, 0, func(int) string { return doc }, func(int) string { return env.full.Token })
	reached := requestsReached(t, site) - before
	t.Logf("%d requests in %s: statuses %v, %d from the ingress, %d from the Bastion %v; %v requests reached the Bastion",
		res.sent, res.took.Round(time.Millisecond), res.codes, res.edge429, res.problem429, res.problemCode, reached)
	if res.edge429 == 0 {
		t.Errorf("the ingress refused nothing of %d requests in %s", res.sent, res.took.Round(time.Second))
	}
	if reached > float64(res.sent-res.edge429)+50 {
		t.Errorf("%v requests reached the Bastion, but the ingress refused %d of %d: the refused ones reached it as well", reached, res.edge429, res.sent)
	}
	if shed := basSum(t, site, "trove_http_requests_shed_total") - shedBefore; shed != 0 || res.codes[503] != 0 {
		t.Errorf("the replicas shed %v requests and answered %d 503: the in-flight limit was reached", shed, res.codes[503])
	}
	for _, p := range basReadyPods(t, site) {
		if !p.Ready || p.Restarts != 0 {
			t.Errorf("replica %s is not healthy after the flood: ready %v restarts %d", p.Name, p.Ready, p.Restarts)
		}
	}
}

// TestBastion_R_BAS_049_FloodingAddress (R-BAS-037): an address that sends 2,000 requests a minute is blocked on both replicas
// (the block list is shared through Valkey) and answers 429 address_blocked; another address is unaffected.
func TestBastion_R_BAS_049_FloodingAddress(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	doc := npmDocPath(env.open)
	other := func() string {
		return strings.TrimSpace(npmClientExec(t, 0, "curl", "-sS", "--cacert", "/etc/trove-ca/ca.crt", "-o", "/dev/null", "-w", "%{http_code}",
			"-H", "Authorization: Bearer "+env.full.Token, site.baseURL(t)+doc))
	}
	if got := other(); got != "200" {
		t.Fatalf("another address before the flood: %s, want 200", got)
	}
	blocksBefore := basSum(t, site, "trove_bastion_blocks_total")
	res := site.flood(t, 2000, 20, 80, func(int) string { return doc }, func(int) string { return env.full.Token })
	t.Logf("%d requests in %s: statuses %v, codes of the Bastion's 429 %v, %d from the ingress", res.sent, res.took.Round(time.Millisecond), res.codes, res.problemCode, res.edge429)
	waitFor(t, "the address to be blocked", 30*time.Second, time.Second, func() bool {
		return basSum(t, site, "trove_bastion_blocks_total") > blocksBefore
	})
	// blocked on every replica: the requests of this address are refused by both
	refusedBefore := map[string]float64{}
	for _, p := range basReadyPods(t, site) {
		refusedBefore[p.Name] = msum(basMetrics(t, p.Name), "trove_bastion_refused_total", `reason="blocked"`)
	}
	blocked := 0
	for range 60 {
		r := site.get(t, env.full.Token, doc)
		if r.Status == 429 && r.Code() == "address_blocked" {
			blocked++
			if r.Header.Get("Retry-After") == "" {
				t.Errorf("address_blocked without Retry-After: %s", r)
			}
		}
	}
	if blocked < 55 {
		t.Errorf("only %d of 60 requests after the block answered 429 address_blocked", blocked)
	}
	for _, p := range basReadyPods(t, site) {
		if d := msum(basMetrics(t, p.Name), "trove_bastion_refused_total", `reason="blocked"`) - refusedBefore[p.Name]; d == 0 {
			t.Errorf("replica %s refused nothing as blocked: the block did not reach it", p.Name)
		}
	}
	if got := other(); got != "200" {
		t.Errorf("another address after the flood: %s, want 200", got)
	}
}
