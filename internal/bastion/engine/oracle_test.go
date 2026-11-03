package engine

import "testing"

// TestR_BAS_024_ANarrowedReaderCannotTellACachedHiddenNameFromAMissingOne: a reader who may read part of a
// repository asks for a name that is cached but hidden from them and for one that does not exist, twice each.
// The Bastion treats them alike: two requests to the main installation per name, so neither the count nor the
// timing of the second answer shows what the cache holds (Q-BAS-42, Q-BAS-124, security finding P4).
func TestR_BAS_024_ANarrowedReaderCannotTellACachedHiddenNameFromAMissingOne(t *testing.T) {
	s := newSiteHarness(t, options{})
	const hidden = "/api/npm/npm-all/secret/-/secret-1.0.0.tgz"
	const missing = "/api/npm/npm-all/nothere/-/nothere-1.0.0.tgz"
	s.site.artifacts[hidden] = []byte("secret bytes")
	if w := s.get(hidden, "Authorization", bearer(s.full)); w.Code != 200 {
		t.Fatalf("the full reader: %d", w.Code)
	}
	cost := func(path string) (code, first, second int) {
		before := s.main.count()
		s.get(path, "Authorization", bearer(s.part))
		mid := s.main.count()
		w := s.get(path, "Authorization", bearer(s.part))
		return w.Code, mid - before, s.main.count() - mid
	}
	hc, h1, h2 := cost(hidden)
	mc, m1, m2 := cost(missing)
	if hc != 404 || mc != 404 {
		t.Fatalf("statuses %d and %d, want 404 for both", hc, mc)
	}
	if h2 == 0 || m2 == 0 {
		t.Fatalf("a second request was answered without the main installation: hidden %d, missing %d", h2, m2)
	}
	// The first request of the hidden name may carry a check and a conditional request that the missing name
	// does not need; what counts is that the second request is alike and the cache entry did not change.
	if h2 != m2 {
		t.Fatalf("the second request costs %d for the cached hidden name and %d for the missing name (first: %d and %d)", h2, m2, h1, m1)
	}
}
