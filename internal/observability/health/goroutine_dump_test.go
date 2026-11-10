package health

import (
	"strings"
	"testing"
	"time"
)

// parkedForTheDump is the stack that thousands of goroutines share in the test below.
func parkedForTheDump(started chan<- struct{}, release <-chan struct{}) {
	started <- struct{}{}
	<-release
}

// R-OPS-001: a process with more goroutines than the dump limit holds still gets a dump that names every stack with its
// count, instead of an arbitrary part of the full listing.
func TestR_OPS_001_GoroutineDumpAboveTheLimitGroupsTheStacksWithTheirCounts(t *testing.T) {
	const n = 4000
	started := make(chan struct{}, n)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for range n {
		go parkedForTheDump(started, release)
	}
	for range n {
		<-started
	}
	// A goroutine that has sent is parked on release within a few instructions; the dump is taken until it shows all of them.
	var dump string
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		dump = GoroutineDump()
		if strings.Contains(dump, "\n4000 @") || strings.HasPrefix(dump, "4000 @") {
			break
		}
	}
	if len(dump) > dumpLimit {
		t.Fatalf("the dump is %d bytes, over the limit of %d", len(dump), dumpLimit)
	}
	if !strings.Contains(dump, "the stacks are grouped") {
		t.Fatalf("a process with %d goroutines still got the full listing, cut at the limit", n)
	}
	if !strings.Contains(dump, "\n4000 @") {
		t.Errorf("no group of %d goroutines in the dump:\n%.2000s", n, dump)
	}
	if !strings.Contains(dump, "parkedForTheDump") {
		t.Error("the stack of the parked goroutines is not named")
	}
}
