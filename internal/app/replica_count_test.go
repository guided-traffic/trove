package app

import (
	"testing"
)

// TestR_ARCH_034_OneReplicaCountForEveryLimiter: the count the limiter families
// divide their fallback share by is one component per process, started once and
// stopped with the others at shutdown, and it is at least 1 before the first read.
func TestR_ARCH_034_OneReplicaCountForEveryLimiter(t *testing.T) {
	a := &App{}
	first, err := a.servingReplicas(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.servingReplicas(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("two counts were started")
	}
	if first.Get() != 1 {
		t.Fatalf("n = %d before the first read, want 1", first.Get())
	}
	if len(a.comps) != 1 || a.comps[0].Name() != "replica-count" {
		t.Fatalf("components = %v", a.comps)
	}
	if err := first.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
