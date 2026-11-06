package engine

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

// content makes the feed answer one content entry for the repository.
func (s *siteHarness) content(id, repo string) {
	s.main.mu.Lock()
	s.main.feedAns = iface.FeedAnswer{
		Entries: []iface.FeedEntry{{ID: id, At: s.clock.now(), Kind: iface.FeedContent, Repositories: []string{repo}}},
		Cursor:  iface.Cursor{At: epoch, ID: id}.String(),
	}
	s.main.mu.Unlock()
}

// TestR_BAS_022_R_BAS_027_AWithdrawnArtifactIsNotServedWhileTheMainInstallationIsUnreachable: the grace period
// extends the validity of an answer, never past a withdrawal. A content entry received after validatedAt - 15 s
// makes the cache entry unservable while unreachable, whether the entry came before the outage or during it, and
// a repository the entry does not name is not affected (Q-BAS-122, security finding P2).
func TestR_BAS_022_R_BAS_027_AWithdrawnArtifactIsNotServedWhileTheMainInstallationIsUnreachable(t *testing.T) {
	for _, when := range []string{"before the outage", "during the outage"} {
		t.Run(when, func(t *testing.T) {
			s := newSiteHarness(t, options{})
			warm(t, s)
			s.site.docs[doc] = "the document"
			s.get(doc, "Authorization", bearer(s.full))
			s.clock.advance(2 * time.Minute)
			if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
				t.Fatalf("a hit: %d", w.Code)
			}
			if when == "before the outage" {
				s.content("c1", "web/npm-all")
				s.clock.advance(time.Second)
				s.tick()
				s.setUnreachable(true)
			} else {
				s.setUnreachable(true)
				s.content("c1", "web/npm-all")
				s.clock.advance(time.Second)
				s.tick()
			}
			before := s.main.count()
			for _, p := range []string{tgz, doc} {
				w := s.get(p, "Authorization", bearer(s.full))
				if w.Code != 503 || problemCode(w) != "main_unreachable" {
					t.Fatalf("%s: a withdrawn entry was served while unreachable: %d %s", p, w.Code, w.Body)
				}
			}
			if s.main.count() != before {
				t.Fatal("a request went to the main installation while it was unreachable")
			}
			// Back: the replica asks the main installation again.
			s.setUnreachable(false)
			if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
				t.Fatalf("after the outage: %d", w.Code)
			}
			if s.main.count() == before {
				t.Fatal("the entry was served without asking the main installation after a withdrawal")
			}
		})
	}
	t.Run("another repository", func(t *testing.T) {
		s := newSiteHarness(t, options{})
		warm(t, s)
		s.content("c1", "other-repo")
		s.clock.advance(time.Second)
		s.tick()
		s.setUnreachable(true)
		if w := s.get(tgz, "Authorization", bearer(s.full)); w.Code != 200 {
			t.Fatalf("an entry of an unaffected repository: %d", w.Code)
		}
	})
}
