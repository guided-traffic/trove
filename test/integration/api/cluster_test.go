//go:build integration

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// answer is one HTTP answer.
type answer = testenv.Answer

// webCluster is several web replicas behind a round-robin "Service": consecutive
// requests land on different replicas (R-ARCH-032).
type webCluster struct {
	nodes []*node
	next  int
	// token, when set, authenticates every request that carries no Authorization
	// header of its own.
	token string
	// val checks the answers of the management API against the document.
	val *apitest.Validator
}

// cluster is the Service of the given replicas; the answers of send are checked
// against api/openapi.yaml, and the validator records which operations the test
// reached.
func (e *env) cluster(nodes ...*node) *webCluster {
	return &webCluster{nodes: nodes, val: e.stack.Validator()}
}

// do sends the request to the next replica in turn, rewriting the host.
func (c *webCluster) do(req *http.Request) (*http.Response, error) {
	n := c.nodes[c.next%len(c.nodes)]
	c.next++
	out, err := http.NewRequestWithContext(req.Context(), req.Method, n.url()+req.URL.RequestURI(), req.Body)
	if err != nil {
		return nil, err
	}
	out.Header = req.Header.Clone()
	if c.token != "" && out.Header.Get("Authorization") == "" {
		out.Header.Set("Authorization", "Bearer "+c.token)
	}
	return testenv.Client.Do(out)
}

// send sends one request through the cluster's next replica.
func send(t testing.TB, c *webCluster, method, path string, body any, hdr ...string) answer {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, "http://cluster"+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if c.val != nil && strings.HasPrefix(path, "/api/v1/") {
		check, _ := http.NewRequest(method, "http://"+testenv.DefaultHost+path, nil)
		if _, err := c.val.Check(check, res.StatusCode, res.Header, b); err != nil {
			t.Errorf("response validation: %v\n%s", err, b)
		}
	}
	return answer{Status: res.StatusCode, Header: res.Header, Body: b}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
