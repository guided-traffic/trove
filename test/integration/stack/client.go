//go:build integration

package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Response is one answer.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body as an object.
func (r Response) JSON(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", r.Status, err, r.Body)
	}
	return m
}

// Items decodes the items of a list answer.
func (r Response) Items(t testing.TB) []map[string]any {
	t.Helper()
	raw, _ := r.JSON(t)["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

// Code is the problem code of an error answer, "" for any other.
func (r Response) Code() string {
	var m map[string]any
	if json.Unmarshal(r.Body, &m) != nil {
		return ""
	}
	c, _ := m["code"].(string)
	return c
}

// Epoch is the Trove-Authz-Epoch header, 0 when absent.
func (r Response) Epoch() int64 {
	var n int64
	for _, c := range r.Header.Get("Trove-Authz-Epoch") {
		n = n*10 + int64(c-'0')
	}
	return n
}

var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}

// Do sends a request to this replica as the named person ("" is anonymous). body
// is nil, bytes, or a value that is sent as JSON. hdr are header name and value
// pairs; a Content-Type is set for a body unless given.
func (r *Replica) Do(person, method, path string, body any, hdr ...string) Response {
	t := r.E.T
	t.Helper()
	req, err := NewRequest(method, r.Server.URL+path, person, body, hdr...)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return Response{Status: res.StatusCode, Header: res.Header, Body: b}
}

// NewRequest builds a request as Do does.
func NewRequest(method, url, person string, body any, hdr ...string) (*http.Request, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rd)
	if err != nil {
		return nil, err
	}
	if person != "" {
		req.Header.Set("X-Test-Person", person)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Cluster is several replicas behind a round-robin "Service": consecutive
// requests land on different replicas (R-ARCH-032).
type Cluster struct {
	Replicas []*Replica

	mu   sync.Mutex
	next int
}

// NewCluster groups replicas.
func NewCluster(rs ...*Replica) *Cluster { return &Cluster{Replicas: rs} }

// Pick returns the next replica in turn.
func (c *Cluster) Pick() *Replica {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.Replicas[c.next%len(c.Replicas)]
	c.next++
	return r
}

// Do sends the request to the next replica in turn.
func (c *Cluster) Do(person, method, path string, body any, hdr ...string) Response {
	return c.Pick().Do(person, method, path, body, hdr...)
}

// Transport adapts the cluster to apitest.Resource.Do: the host of the request
// is replaced by the next replica's and the person's header is added.
func (c *Cluster) Transport(person string) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		r := c.Pick()
		out, err := http.NewRequestWithContext(req.Context(), req.Method, r.Server.URL+req.URL.RequestURI(), req.Body) //nolint:gosec // the URL is the test server of the replica
		if err != nil {
			return nil, err
		}
		out.Header = req.Header.Clone()
		if person != "" {
			out.Header.Set("X-Test-Person", person)
		}
		return client.Do(out) //nolint:gosec // the request goes to the replica's own test server
	}
}
