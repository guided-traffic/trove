package nugetstub

import (
	"sync"
	"testing"
)

// The open-request counter serves the bound of R-REPO-055 (R-REPO-059's e2e set reads it).
func TestInflightCountsTheRequestsOpenAtOnce(t *testing.T) {
	f := newFixture(t, Config{})
	f.setMode(Mode{DelayMS: map[string]int{"all": 300}})
	f.stub.Reset()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); f.get(f.api + "/v3/index.json") }()
	}
	wg.Wait()
	got := f.stub.Inflight()
	if got.Open != 0 || got.Max != 4 || got.Requests != 4 {
		t.Fatalf("after four requests held 300 ms: %+v, want open 0, max 4, requests 4", got)
	}
	f.stub.Reset()
	if got := f.stub.Inflight(); got.Max != 0 || got.Requests != 0 {
		t.Fatalf("after a reset: %+v", got)
	}
}
