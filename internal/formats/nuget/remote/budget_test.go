package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// pagesIndex is a registration index that lists n pages by address.
func pagesIndex(n int) []byte {
	pages := make([]string, n)
	for i := range pages {
		pages[i] = fmt.Sprintf(`{"@id":"https://up.test/reg/a/page/%d.0.0/%d.0.0.json","count":1,"lower":"%d.0.0","upper":"%d.0.0"}`, i, i, i, i)
	}
	return []byte(`{"count":` + fmt.Sprint(n) + `,"items":[` + strings.Join(pages, ",") + `]}`)
}

// TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIsNoDocument (Q-NUGET-83): every page an index
// lists by address costs an upstream request, a stored document and a URL-map row. An index that
// lists more than MaxPages is refused as the document its path does not promise, so a fill
// stores nothing and fetches no page; before, any number of pages was read.
func TestR_NUGET_015_AnIndexOfMorePagesThanTroveReadsIsNoDocument(t *testing.T) {
	if _, err := ParseIndex(pagesIndex(MaxPages), 64<<20); err != nil {
		t.Fatalf("an index of %d pages: %v", MaxPages, err)
	}
	for name, doc := range map[string][]byte{"plain": pagesIndex(MaxPages + 1), "gzip": gz(t, pagesIndex(10*MaxPages))} {
		_, err := ParseIndex(doc, 64<<20)
		if e, ok := errs.As(err); !ok || e.Kind != errs.UpstreamFailed || e.Code != errs.CodeNuGetInvalidUpstreamDocument {
			t.Errorf("%s: %v, want 502 nuget_invalid_upstream_document", name, err)
		}
	}
}

// TestR_NUGET_015_ARenderingReadsOneDocumentsWorthOfAnUpstreamRegistration (Q-NUGET-83): the bound
// on what is held in memory applies to the index and its pages together, and the leaves of one
// rendering are bounded too, so pages of tiny leaves cannot turn the byte bound into millions of
// rows. Before, each page was bounded on its own, and nothing bounded their number or leaves.
func TestR_NUGET_015_ARenderingReadsOneDocumentsWorthOfAnUpstreamRegistration(t *testing.T) {
	page := func(n int) []byte {
		leaves := make([]string, n)
		for i := range leaves {
			leaves[i] = fmt.Sprintf(`{"catalogEntry":{"version":"1.%d.0"}}`, i)
		}
		return []byte(`{"@id":"p","lower":"1.0.0","upper":"9.0.0","items":[` + strings.Join(leaves, ",") + `]}`)
	}
	one := page(100)
	b := NewBudget(int64(len(one))*3 + int64(len(pagesIndex(3))))
	if _, err := b.Index(pagesIndex(3)); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := b.Page(gz(t, one)); err != nil {
			t.Fatalf("page %d within the budget: %v", i, err)
		}
	}
	_, err := b.Page(one)
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeUpstreamItemTooLarge || e.Kind != errs.UpstreamFailed {
		t.Errorf("the fourth page: %v, want 502 upstream_item_too_large", err)
	}

	leaves := NewBudget(1 << 30)
	big := page(MaxLeaves/2 + 1)
	if _, err := leaves.Page(big); err != nil {
		t.Fatal(err)
	}
	_, err = leaves.Page(big)
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeNuGetInvalidUpstreamDocument {
		t.Errorf("more than %d leaves: %v, want 502 nuget_invalid_upstream_document", MaxLeaves, err)
	}
}

// TestR_NUGET_015_CollectorConvertsPageByPageAndKeepsTheFirstLeaf: the collector the rendering
// converts the pages with lists a version once, by the leaf added first, ascending.
func TestR_NUGET_015_CollectorConvertsPageByPageAndKeepsTheFirstLeaf(t *testing.T) {
	c := NewCollector("a")
	c.Add([]Leaf{leafOf(t, `{"catalogEntry":{"version":"2.0.0","description":"first"}}`)})
	c.Add([]Leaf{leafOf(t, `{"catalogEntry":{"version":"2.0.0.0","description":"second"}}`), leafOf(t, `{"catalogEntry":{"version":"1.0.0"}}`)})
	vs := c.Versions()
	if len(vs) != 2 || vs[0].Row.LowerVersion != "1.0.0" || !strings.Contains(string(vs[1].Row.Metadata), "first") {
		t.Errorf("versions = %+v", vs)
	}
}

// TestR_NUGET_015_DeprecationAndVulnerabilitiesComeWithoutTheUpstreamsAddresses (Q-NUGET-85): the
// members are passed through for the client as nuget.org writes them, without the JSON-LD @id and
// @type at any depth, which name the upstream's catalog and registration; advisoryUrl stays, a
// link for people, when it is an absolute web address. Before, both members were copied verbatim.
func TestR_NUGET_015_DeprecationAndVulnerabilitiesComeWithoutTheUpstreamsAddresses(t *testing.T) {
	v, ok := Convert(leafOf(t, `{"catalogEntry":{"id":"Newtonsoft.Json","version":"12.0.1",
	  "deprecation":{"@id":"https://api.nuget.org/v3/catalog0/data/x.json#deprecation","@type":"deprecation","reasons":["Legacy","CriticalBugs"],
	    "message":"Use 13.0.1 & later <b>","alternatePackage":{"@id":"https://api.nuget.org/v3/registration5-gz-semver2/newtonsoft.json/index.json","id":"Newtonsoft.Json","range":"[13.0.1, )"}},
	  "vulnerabilities":[
	    {"@id":"https://api.nuget.org/v3/catalog0/data/x.json#vulnerability/GitHub/1","@type":"Vulnerability","advisoryUrl":"https://github.com/advisories/GHSA-5crp-9r3c-p9vr","severity":"2"},
	    {"advisoryUrl":"javascript:alert(1)","severity":"3"},
	    {"advisoryUrl":"https://user:pw@advisories.test/x","severity":"1","note":"https://elsewhere.test/"}]}}`), "newtonsoft.json")
	if !ok {
		t.Fatal("not converted")
	}
	if got, want := string(v.Extras.Deprecation), `{"alternatePackage":{"id":"Newtonsoft.Json","range":"[13.0.1, )"},"message":"Use 13.0.1 & later <b>","reasons":["Legacy","CriticalBugs"]}`; got != want {
		t.Errorf("deprecation = %s\nwant          %s", got, want)
	}
	if got, want := string(v.Extras.Vulnerabilities), `[{"advisoryUrl":"https://github.com/advisories/GHSA-5crp-9r3c-p9vr","severity":"2"},{"severity":"3"},{"severity":"1"}]`; got != want {
		t.Errorf("vulnerabilities = %s\nwant              %s", got, want)
	}
	var anything any
	if err := json.Unmarshal(v.Extras.Vulnerabilities, &anything); err != nil || !json.Valid(v.Extras.Deprecation) {
		t.Errorf("not JSON: %v", err)
	}
	if bytes.Contains(v.Extras.Deprecation, []byte("api.nuget.org")) || bytes.Contains(v.Extras.Vulnerabilities, []byte("api.nuget.org")) {
		t.Error("an address of the upstream is passed through")
	}
}

// TestR_NUGET_015_APassThroughMemberWithNothingLeftIsAbsent: a member that is null, absent, or
// holds only the upstream's addresses is left out of the entry.
func TestR_NUGET_015_APassThroughMemberWithNothingLeftIsAbsent(t *testing.T) {
	for name, raw := range map[string]string{"null": `null`, "only addresses": `{"@id":"https://up.test/x","@type":"deprecation"}`, "absent": ``} {
		if got := passThrough(json.RawMessage(raw), false); got != nil {
			t.Errorf("%s: %s, want nothing", name, got)
		}
	}
}

// leavesJSON is n leaves of the ID a, versions 1.0.0 to 1.0.<n-1>, each naming a package address.
func leavesJSON(n int) string {
	leaves := make([]string, n)
	for i := range leaves {
		leaves[i] = fmt.Sprintf(`{"packageContent":"https://elsewhere.test/a.1.0.%d.nupkg","catalogEntry":{"id":"A","version":"1.0.%d"}}`, i, i)
	}
	return strings.Join(leaves, ",")
}

// TestR_NUGET_015_AFilledPageOrIndexHoldsNoMoreLeavesThanOneRenderingReads (Q-NUGET-83): the fill
// of a registration page or index reads, converts and maps its leaves before anything is stored,
// so the bound one rendering reads holds there already: a page of more than MaxLeaves leaves and
// an index that inlines more than MaxLeaves over its pages are no documents
// (nuget_invalid_upstream_document); MaxLeaves itself is read.
func TestR_NUGET_015_AFilledPageOrIndexHoldsNoMoreLeavesThanOneRenderingReads(t *testing.T) {
	const max = 1 << 30
	page := func(n int) []byte {
		return []byte(fmt.Sprintf(`{"@id":"https://up.test/reg/a/page/1.0.0/1.0.%d.json","count":%d,"lower":"1.0.0","upper":"1.0.%d","items":[%s]}`, n-1, n, n-1, leavesJSON(n)))
	}
	if _, err := ParsePage(page(40_000), max); !isInvalidDocument(err) {
		t.Errorf("a page of 40,000 leaves: %v, want nuget_invalid_upstream_document", err)
	}
	if leaves, err := ParsePage(page(MaxLeaves), max); err != nil || len(leaves) != MaxLeaves {
		t.Errorf("a page of MaxLeaves leaves: %d %v", len(leaves), err)
	}
	inline := func(per, pages int) []byte {
		items := make([]string, pages)
		for i := range items {
			items[i] = fmt.Sprintf(`{"@id":"https://up.test/reg/a/index.json#page/%d","count":%d,"lower":"1.0.0","upper":"1.0.%d","items":[%s]}`, i, per, per-1, leavesJSON(per))
		}
		return []byte(`{"count":` + fmt.Sprint(pages) + `,"items":[` + strings.Join(items, ",") + `]}`)
	}
	if _, err := ParseIndex(inline(20_000, 2), max); !isInvalidDocument(err) {
		t.Errorf("an index that inlines 40,000 leaves: %v, want nuget_invalid_upstream_document", err)
	}
	if _, err := ParseIndex(inline(MaxLeaves/2, 2), max); err != nil {
		t.Errorf("an index that inlines MaxLeaves leaves: %v", err)
	}
}

func isInvalidDocument(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Code == errs.CodeNuGetInvalidUpstreamDocument
}
