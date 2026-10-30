package app

import (
	"testing"
)

// TestR_ARCH_034_OneReplicaCountForEveryLimiter: the count the limiter families
// divide their fallback share by is one component per process, built by the
// limits area (the only step that builds a limiter set, so there is one count),
// and it is at least 1 before the first read.
func TestR_ARCH_034_OneReplicaCountForEveryLimiter(t *testing.T) {
	c := &replicaCount{a: &App{}}
	if c.Get() != 1 {
		t.Fatalf("n = %d before the first read, want 1", c.Get())
	}
	if err := c.Stop(t.Context()); err != nil {
		t.Fatalf("stop of a count that never started: %v", err)
	}
	builders := 0
	for _, s := range wiringSteps() {
		if s.gives.String() == "app.limitsArea" {
			builders++
		}
	}
	if builders != 1 {
		t.Fatalf("%d steps build the limiter set, want 1", builders)
	}
}
