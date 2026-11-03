package engine

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestR_BAS_025_ARefusalOfOneCallerDoesNotSwitchCollapsingOffForOthers: a caller without the right to a popular
// cold tarball gets the main installation's refusal; the key is marked as not shareable for that caller only. Ten
// full readers who ask for the same tarball afterwards still share one fetch (Q-BAS-125, security finding B10).
func TestR_BAS_025_ARefusalOfOneCallerDoesNotSwitchCollapsingOffForOthers(t *testing.T) {
	m := &fakeMain{delay: 40 * time.Millisecond}
	s := newSiteHarness(t, options{main: m})
	body := payload(2000)
	s.site.artifacts[other] = body
	for i := 0; i < 3; i++ {
		if w := s.get(other, "Authorization", bearer(s.part)); w.Code != http.StatusNotFound {
			t.Fatalf("the narrowed reader: %d", w.Code)
		}
	}
	if !s.e.isVolatile(credentialKey(s.part) + "|" + other) {
		t.Fatal("the refusal did not mark the key for its caller")
	}
	if s.e.isVolatile(other) {
		t.Fatal("one caller's refusal marked the key for everybody")
	}
	before := m.count()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := s.get(other, "Authorization", bearer(s.full)); w.Code != http.StatusOK || w.Body.Len() != len(body) {
				t.Errorf("%d %d", w.Code, w.Body.Len())
			}
		}()
	}
	wg.Wait()
	if n := m.count() - before; n != 1 {
		t.Fatalf("%d requests for ten concurrent full readers, want 1", n)
	}
}
