package jobs

import (
	"strings"
	"testing"
	"time"
)

// TestR_ARCH_016_EnqueueTxWritesTheRowEnqueueWrites: the insert of EnqueueTx, which takes the kind's spec from the caller, is
// the one Enqueue builds from the registry, whatever the spec leaves unset (Q-HA-180). The statements run against PostgreSQL
// in test/integration/backup.
func TestR_ARCH_016_EnqueueTxWritesTheRowEnqueueWrites(t *testing.T) {
	spec := KindSpec{DedupMode: Exclusive}
	r := NewRegistry()
	withHandler := spec
	withHandler.Handler = noop
	r.RegisterKind("demo-rescan", withHandler)
	qu := NewQueue(QueueOptions{Registry: r})
	e := EnqueueSpec{Kind: "demo-rescan", Args: map[string]int{"n": 1}, DedupKey: "demo-rescan:7", TargetKind: "repository", TargetKey: "web/x", Delay: 3 * time.Second}

	fromQueue, _, err := qu.params(e)
	if err != nil {
		t.Fatal(err)
	}
	fromSpec, err := paramsFor(normalize(spec), e)
	if err != nil {
		t.Fatal(err)
	}
	if fromQueue.ID == "" || fromSpec.ID == "" {
		t.Fatal("a row has an ID")
	}
	fromQueue.ID, fromSpec.ID = "", ""
	if fromQueue != fromSpec {
		t.Errorf("rows differ:\n queue %+v\n spec  %+v", fromQueue, fromSpec)
	}
	if fromSpec.Queue != "default" || fromSpec.MaxAttempts != 10 || fromSpec.Priority != 100 || fromSpec.DedupMode != string(Exclusive) {
		t.Errorf("defaults not applied: %+v", fromSpec)
	}

	t.Run("a key on a kind without a dedup mode is refused as Enqueue refuses it", func(t *testing.T) {
		_, err := paramsFor(normalize(KindSpec{}), EnqueueSpec{Kind: "plain", DedupKey: "k"})
		if err == nil || !strings.Contains(err.Error(), "no dedup mode") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a priority of the request overrides the kind's", func(t *testing.T) {
		p, err := paramsFor(normalize(KindSpec{Priority: 5}), EnqueueSpec{Kind: "k", Priority: 2})
		if err != nil || p.Priority != 2 {
			t.Fatalf("priority = %d, %v", p.Priority, err)
		}
	})
}
