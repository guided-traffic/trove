package oci

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// hideTags refuses read of every tag path whose tag starts with the prefix.
func hideTags(prefix string) func(formats.Target) error {
	return func(t formats.Target) error {
		if strings.Contains(t.Path, "/_tags/"+prefix) && slicesContains(t.Actions, formats.ActionRead) {
			return errs.E(errs.Forbidden, errs.CodeForbidden, "excluded")
		}
		return nil
	}
}

func slicesContains(s []formats.Action, a formats.Action) bool {
	for _, x := range s {
		if x == a {
			return true
		}
	}
	return false
}

// TestALinkNeverNamesATagTheCallerMayNotRead: with one tag per page the Link of each page names the last tag listed, so paging
// does not walk the names of the hidden tags (Q-OCI-76), and the whole list costs one audited decision.
//
// Mechanism of R-OCI-015, not a proof of its criteria: it checks hidden-tag handling or an edge case; the criteria are the n=2
// paging and the conformance content-discovery category.
func TestALinkNeverNamesATagTheCallerMayNotRead(t *testing.T) {
	st := newMemStore()
	for _, tag := range []string{"v1", "v2", "v3", "x01", "x02", "x03", "z9"} {
		st.putTag("app", tag, digestN(1))
	}
	h := listHost(t, st, func(o *formattest.HostOptions) { o.Allow = hideTags("x") })
	var seen []string
	requests := 0
	path := "app/tags/list?n=1"
	for range 10 {
		requests++
		resp := get(t, h, path)
		_, tags := tagsOf(t, resp)
		seen = append(seen, tags...)
		link := resp.Header().Get("Link")
		if strings.Contains(link, "x0") {
			t.Fatalf("a Link names a hidden tag: %s", link)
		}
		if link == "" {
			break
		}
		path = strings.TrimSuffix(strings.TrimPrefix(link, "</v2/it/main/"), `>; rel="next"`)
	}
	if strings.Join(seen, ",") != "v1,v2,v3,z9" {
		t.Fatalf("the pages list %v", seen)
	}
	// One gate decision per request, and the entries are decided in a batch that writes no audit event.
	if got := len(h.Authorizations()); got != requests {
		t.Errorf("%d audited decisions for %d requests", got, requests)
	}
}

// TestAListThatCannotGoOnWithoutNamingAHiddenTagIsRefused: more consecutive hidden tags than the scan budget end the request
// with a refusal and not with a Link at a hidden name.
//
// Mechanism of R-OCI-015, not a proof of its criteria: it checks hidden-tag handling or an edge case; the criteria are the n=2
// paging and the conformance content-discovery category.
func TestAListThatCannotGoOnWithoutNamingAHiddenTagIsRefused(t *testing.T) {
	st := newMemStore()
	st.putTag("app", "a", digestN(1))
	for i := range tagsScanBudget + 500 {
		st.putTag("app", fmt.Sprintf("x%05d", i), digestN(1))
	}
	st.putTag("app", "z", digestN(1))
	h := listHost(t, st, func(o *formattest.HostOptions) { o.Allow = hideTags("x") })
	resp := get(t, h, "app/tags/list?n=2")
	_, tags := tagsOf(t, resp)
	if resp.Code != http.StatusOK || strings.Join(tags, ",") != "a" || strings.Contains(resp.Header().Get("Link"), "x0") {
		t.Fatalf("%d %v Link %q", resp.Code, tags, resp.Header().Get("Link"))
	}
	// From the last readable tag on, the next page finds no readable tag within the budget.
	resp = get(t, h, "app/tags/list?n=2&last=a")
	if resp.Code != http.StatusBadRequest || strings.Contains(resp.Body.String(), "x0") {
		t.Fatalf("%d %s", resp.Code, resp.Body)
	}
}

// TestTheCascadeOfADeleteIsHeldToTheGrantsOfTheCaller: the tags of a referrer go with the referrer, two levels below the manifest
// the caller named; the cascade decides every path it removes for `delete`, so the removal is refused whole (Q-OCI-76).
//
// Mechanism of R-OCI-018, not a proof of its criteria: it checks one step of the cascade, the hook or the grammar of a reference;
// the criteria are the index delete with a shared child, the 409 for a child and the conformance content-management category.
func TestTheCascadeOfADeleteIsHeldToTheGrantsOfTheCaller(t *testing.T) {
	st := graph()
	st.putTag("app", "release-sig", digestN(7))
	h := listHost(t, st, func(o *formattest.HostOptions) {
		o.Allow = func(tg formats.Target) error {
			if slicesContains(tg.Actions, formats.ActionDelete) && strings.Contains(tg.Path, "/_tags/release-") {
				return errs.E(errs.Forbidden, errs.CodeForbidden, "excluded")
			}
			return nil
		}
	})
	for _, n := range []byte{1, 2, 3, 4, 6, 7} {
		h.PutItem(mpath(n), manifestType, []byte{n})
	}
	for _, tag := range []string{"latest", "release-sig"} {
		h.PutItem(TagPath("app", tag), manifestType, []byte("x"))
	}
	if err := h.Seed(context.Background(), func(w formats.PackageWriter) error {
		_, _, err := w.EnsureLocked(context.Background(), h.Ref(), "app", "app")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The direct delete of the excluded tag is refused, and so is the delete of the subject whose cascade reaches it.
	if resp := del(t, h, "app/manifests/release-sig"); resp.Code != http.StatusForbidden {
		t.Fatalf("the excluded tag: %d %s", resp.Code, resp.Body)
	}
	if resp := del(t, h, "app/manifests/"+digestText(digestN(1))); resp.Code != http.StatusForbidden {
		t.Fatalf("the subject: %d %s", resp.Code, resp.Body)
	}
	for _, p := range []string{mpath(1), mpath(7), TagPath("app", "latest"), TagPath("app", "release-sig")} {
		if !stored(h, p) {
			t.Errorf("%s was removed by a refused delete", p)
		}
	}
}
