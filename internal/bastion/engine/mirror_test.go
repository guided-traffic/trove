package engine

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// TestR_BAS_023_A304SharedAgainstThePrivateCopyAnswersTheCallerWithTheCopysBytes: the mirror of
// TestR_BAS_023_A304ForAPrivateDocumentAnswersTheCallerWithTheSharedEntrysBytes. The caller has a private copy of
// a document and the Bastion's conditional request names its validator; the main installation now answers 304
// marked shared (it no longer filters the document for the caller). The caller sent no condition, so it gets the
// copy's bytes as a 200, and the stale private copy is dropped (security finding B1, mirror case).
func TestR_BAS_023_A304SharedAgainstThePrivateCopyAnswersTheCallerWithTheCopysBytes(t *testing.T) {
	s := newSiteHarness(t, options{})
	s.site.docs[doc] = "all packages"
	w := s.get(doc, "Authorization", bearer(s.part))
	if w.Code != 200 || w.Body.String() != "filtered:all packages" {
		t.Fatalf("the private document: %d %q", w.Code, w.Body)
	}
	etag := w.Header().Get("Etag")
	if etag == "" {
		t.Fatal("the private document has no validator")
	}
	viaSite := s.main.doFn
	s.main.doFn = func(f mainclient.Forward) (*mainclient.Response, error) {
		if f.IfNoneMatch == etag {
			sub := tokenSubject("part", nil)
			return resp(http.StatusNotModified, marking(iface.ClassShared, iface.ReadFull, "npm-all", sub), http.Header{"Etag": {etag}, "Content-Type": {"application/json"}}, ""), nil
		}
		return viaSite(f)
	}
	w = s.get(doc, "Authorization", bearer(s.part))
	if w.Code != 200 || w.Body.String() != "filtered:all packages" {
		t.Fatalf("a 304 shared against the private copy: %d %q", w.Code, w.Body)
	}
	if s.e.privGet(credentialKey(s.part), doc) != nil {
		t.Fatal("the private copy of a caller the main installation no longer filters for was kept")
	}
}
