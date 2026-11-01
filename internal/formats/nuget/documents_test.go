package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

const feedURL = "https://trove.example.com/api/nuget/main/v3/"

func gunzip(t testing.TB, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// widgets is the package of the acceptance criteria of R-NUGET-004: 1.0.0 and the
// SemVer 2.0.0-specific 1.1.0-ci.5+sha.abc, plus the versions of R-NUGET-008.
func widgets(fx *docFixture) {
	fx.put("1.0.0", true, `{"description":"first"}`)
	fx.put("1.1.0-ci.5+sha.abc", true, `{"description":"ci"}`)
	fx.put("1.2.3", true, `{"description":"third"}`)
	fx.put("1.3.0-beta.1", true, `{"description":"beta"}`)
}

// TestR_NUGET_008_FlatIndexListsEveryVersion: the version list holds every stored version
// in URL form, ascending by precedence, SemVer 1 and 2, listed and unlisted alike.
func TestR_NUGET_008_FlatIndexListsEveryVersion(t *testing.T) {
	fx := newDocFixture(t)
	fx.put("1.10.0", true, `{}`)
	fx.put("1.2.3", true, `{}`)
	fx.put("1.0.0", false, `{}`)
	fx.put("1.1.0-ci.5+sha.abc", true, `{}`)
	fx.put("1.3.0-beta.1", true, `{}`)
	resp := fx.get("v3/flatcontainer/acme.widgets/index.json")
	if resp.Code != 200 {
		t.Fatalf("status %d: %s", resp.Code, resp.Body)
	}
	if want := `{"versions":["1.0.0","1.1.0-ci.5","1.2.3","1.3.0-beta.1","1.10.0"]}`; resp.Body.String() != want {
		t.Errorf("body = %s, want %s", resp.Body, want)
	}
	if ct := resp.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if ce := resp.Header().Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q: the version list is not compressed", ce)
	}

	// The spelling of the ID in the path does not matter (R-NUGET-003).
	if again := fx.get("v3/flatcontainer/ACME.Widgets/index.json"); again.Body.String() != resp.Body.String() {
		t.Errorf("another spelling of the ID: %d %s", again.Code, again.Body)
	}
}

// TestR_NUGET_004_FlatIndexOfTheAcceptanceExample: package Acme.Widgets with 1.0.0 and
// 1.1.0-ci.5+sha.abc lists both, the second without its build metadata.
func TestR_NUGET_004_FlatIndexOfTheAcceptanceExample(t *testing.T) {
	fx := newDocFixture(t)
	fx.put("1.0.0", true, `{}`)
	fx.put("1.1.0-ci.5+sha.abc", true, `{}`)
	if got, want := fx.get("v3/flatcontainer/acme.widgets/index.json").Body.String(), `{"versions":["1.0.0","1.1.0-ci.5"]}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// TestR_NUGET_008_UnknownIDIs404: an ID without a version, with a package row or without,
// is a 404 in the one shape of the format.
func TestR_NUGET_008_UnknownIDIs404(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	for _, path := range []string{
		"v3/flatcontainer/nothing.here/index.json",
		"v3/registration/nothing.here/index.json",
		"v3/registration-gz/nothing.here/1.0.0.json",
		"v3/registration-semver2/nothing.here/page/1.0.0/2.0.0.json",
	} {
		resp := fx.get(path)
		if resp.Code != 404 || !strings.HasPrefix(resp.Body.String(), "not_found: ") {
			t.Errorf("%s = %d %q", path, resp.Code, resp.Body)
		}
	}
	// A package row whose versions are all gone has no document either.
	fx.putAll("emptied")
	if resp := fx.get("v3/flatcontainer/emptied/index.json"); resp.Code != 404 {
		t.Errorf("a package without a version: %d %s", resp.Code, resp.Body)
	}
}

// TestR_NUGET_013_DocumentsAskTheReadPermissionOnTheID: the version list and the
// registrations are authorized as a read of the ID, a refusal is the answer of the decision
// point, and the data port is not read before it.
func TestR_NUGET_013_DocumentsAskTheReadPermissionOnTheID(t *testing.T) {
	deny := errs.E(errs.NotFound, errs.CodeNotFound, "The resource does not exist.")
	f := New()
	st := newDocStore()
	h := formattest.NewHost(f, formattest.HostOptions{
		Data: st,
		Allow: func(target formats.Target) error {
			if target.Package == "secret.pkg" {
				return deny
			}
			return nil
		},
	})
	fx := &docFixture{t: t, f: f, host: h, store: st}
	fx.putAll("secret.pkg", registrationVersion("Secret.Pkg", "1.0.0", true, `{}`))
	fx.put("1.0.0", true, `{}`)
	reads := st.readCount()
	for _, path := range []string{
		"v3/flatcontainer/secret.pkg/index.json", "v3/registration/secret.pkg/index.json",
		"v3/registration-gz/secret.pkg/1.0.0.json", "v3/registration-semver2/secret.pkg/page/1.0.0/1.0.0.json",
	} {
		if resp := fx.get(path); resp.Code != 404 {
			t.Errorf("%s = %d", path, resp.Code)
		}
	}
	if st.readCount() != reads {
		t.Error("the data port was read for a package the caller may not read")
	}
	for _, c := range h.Authorizations() {
		if c.Target.Package == "" || len(c.Target.Actions) != 1 || c.Target.Actions[0] != formats.ActionRead {
			t.Errorf("authorization of %s asked %+v", c.Route, c.Target)
		}
	}
	if resp := fx.get("v3/flatcontainer/acme.widgets/index.json"); resp.Code != 200 {
		t.Errorf("a readable package = %d", resp.Code)
	}
}

// documentURLs are one document of each kind and hive, for the properties every rendered
// document shares.
func documentURLs() map[string]string {
	return map[string]string{
		"version list":         "v3/flatcontainer/acme.widgets/index.json",
		"index":                "v3/registration/acme.widgets/index.json",
		"index gz":             "v3/registration-gz/acme.widgets/index.json",
		"index semver2":        "v3/registration-semver2/acme.widgets/index.json",
		"page":                 "v3/registration/acme.widgets/page/1.0.0/1.2.3.json",
		"page gz":              "v3/registration-gz/acme.widgets/page/1.0.0/1.2.3.json",
		"page semver2":         "v3/registration-semver2/acme.widgets/page/1.0.0/1.3.0-beta.1.json",
		"leaf":                 "v3/registration/acme.widgets/1.2.3.json",
		"leaf gz":              "v3/registration-gz/acme.widgets/1.2.3.json",
		"leaf semver2 version": "v3/registration-semver2/acme.widgets/1.1.0-ci.5.json",
	}
}

// TestR_NUGET_002_HeadEqualsGetMinusBody: HEAD on every rendered document returns the
// status and headers of the GET - Content-Type, ETag, Content-Encoding, Vary and the
// Content-Length of the variant GET sends - and no body, with and without gzip.
func TestR_NUGET_002_HeadEqualsGetMinusBody(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	for name, path := range documentURLs() {
		for _, enc := range []string{"", "gzip"} {
			t.Run(name+"/"+enc, func(t *testing.T) {
				get := fx.do("GET", path, nil, "Accept-Encoding", enc)
				head := fx.do("HEAD", path, nil, "Accept-Encoding", enc)
				if get.Code != 200 || head.Code != 200 {
					t.Fatalf("status GET %d, HEAD %d: %s", get.Code, head.Code, get.Body)
				}
				if head.Body.Len() != 0 {
					t.Errorf("HEAD sent %d bytes", head.Body.Len())
				}
				for _, h := range []string{"Content-Type", "ETag", "Content-Encoding", "Vary", "Content-Length"} {
					if g, hd := get.Header().Get(h), head.Header().Get(h); g != hd {
						t.Errorf("%s: GET %q, HEAD %q", h, g, hd)
					}
				}
				if n := get.Header().Get("Content-Length"); n != strconv.Itoa(get.Body.Len()) {
					t.Errorf("Content-Length %s, but GET sent %d bytes", n, get.Body.Len())
				}
			})
		}
	}
}

// TestR_NUGET_002_WeakETagIsSharedByGzipAndIdentity: every rendered document carries a weak
// ETag; the gzip and the identity variant carry the same one; If-None-Match with it - in
// either variant's request, as a list, or as a strong copy - answers 304 with no body.
func TestR_NUGET_002_WeakETagIsSharedByGzipAndIdentity(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	for name, path := range documentURLs() {
		t.Run(name, func(t *testing.T) {
			plain := fx.get(path)
			zipped := fx.get(path, "Accept-Encoding", "gzip")
			etag := plain.Header().Get("ETag")
			if !strings.HasPrefix(etag, `W/"`) {
				t.Fatalf("ETag %q is not weak", etag)
			}
			if zipped.Header().Get("ETag") != etag {
				t.Errorf("gzip ETag %q, identity %q", zipped.Header().Get("ETag"), etag)
			}
			for _, inm := range []string{etag, `"x", ` + etag, strings.TrimPrefix(etag, "W/")} {
				for _, enc := range []string{"", "gzip"} {
					resp := fx.get(path, "If-None-Match", inm, "Accept-Encoding", enc)
					if resp.Code != 304 || resp.Body.Len() != 0 || resp.Header().Get("ETag") != etag {
						t.Errorf("If-None-Match %q (%q) = %d, ETag %q, %d body bytes", inm, enc, resp.Code, resp.Header().Get("ETag"), resp.Body.Len())
					}
				}
			}
			if resp := fx.get(path, "If-None-Match", `W/"other"`); resp.Code != 200 {
				t.Errorf("another validator = %d", resp.Code)
			}
		})
	}
}

// TestR_NUGET_002_ETagChangesWithAPush: after a push of a new version the validator of the
// earlier answer no longer matches, in every document of the ID, and the next answer holds
// the version.
func TestR_NUGET_002_ETagChangesWithAPush(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	before := map[string]string{}
	for name, path := range documentURLs() {
		before[name] = fx.get(path).Header().Get("ETag")
	}
	fx.put("1.4.0", true, `{}`)
	for name, path := range documentURLs() {
		resp := fx.get(path, "If-None-Match", before[name])
		if resp.Code != 200 || resp.Header().Get("ETag") == before[name] {
			t.Errorf("%s: after a push If-None-Match = %d, ETag %q", name, resp.Code, resp.Header().Get("ETag"))
		}
	}
	if got := fx.get("v3/flatcontainer/acme.widgets/index.json").Body.String(); !strings.Contains(got, `"1.4.0"`) {
		t.Errorf("version list after the push: %s", got)
	}
}

// TestR_NUGET_007_UnlistKeepsTheVersionInTheDocuments: an unlisted version stays in the
// version list and in the registration, with "listed": false, and the unlist raises the
// validator.
func TestR_NUGET_007_UnlistKeepsTheVersionInTheDocuments(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	idx := "v3/registration/acme.widgets/index.json"
	before := fx.get(idx).Header().Get("ETag")
	fx.setListed("acme.widgets", "1.2.3", false)
	resp := fx.get(idx, "If-None-Match", before)
	if resp.Code != 200 {
		t.Fatalf("an unlist answers %d to the earlier validator", resp.Code)
	}
	var doc struct {
		Items []struct {
			Items []struct {
				CatalogEntry struct {
					Version string `json:"version"`
					Listed  bool   `json:"listed"`
				} `json:"catalogEntry"`
			} `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range doc.Items[0].Items {
		got[it.CatalogEntry.Version] = it.CatalogEntry.Listed
	}
	if len(got) != 2 || got["1.0.0"] != true || got["1.2.3"] != false {
		t.Errorf("listed by version = %v", got)
	}
	if l := fx.get("v3/flatcontainer/acme.widgets/index.json").Body.String(); !strings.Contains(l, `"1.2.3"`) {
		t.Errorf("an unlisted version left the version list: %s", l)
	}
	leaf := fx.get("v3/registration/acme.widgets/1.2.3.json")
	if leaf.Code != 200 || !strings.Contains(leaf.Body.String(), `"listed":false`) {
		t.Errorf("leaf of an unlisted version = %d %s", leaf.Code, leaf.Body)
	}
}

// TestR_NUGET_009_GzipHivesNegotiateAndThePlainHiveNever: registration-gz/ and
// registration-semver2/ answer gzip to a client that accepts it and identity otherwise,
// both with Vary: Accept-Encoding; registration/ is identity always and sends no Vary.
func TestR_NUGET_009_GzipHivesNegotiateAndThePlainHiveNever(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	for _, hive := range []string{"registration", "registration-gz", "registration-semver2"} {
		path := "v3/" + hive + "/acme.widgets/index.json"
		gz := hive != "registration"
		plain := fx.get(path)
		zipped := fx.get(path, "Accept-Encoding", "gzip, deflate")
		refused := fx.get(path, "Accept-Encoding", "gzip;q=0")
		if plain.Header().Get("Content-Encoding") != "" || refused.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: identity expected, got %q / %q", hive, plain.Header().Get("Content-Encoding"), refused.Header().Get("Content-Encoding"))
		}
		if gz != (zipped.Header().Get("Content-Encoding") == "gzip") {
			t.Errorf("%s: Content-Encoding %q for a client that accepts gzip", hive, zipped.Header().Get("Content-Encoding"))
		}
		for _, r := range []*formattest.Response{plain, zipped, refused} {
			if (r.Header().Get("Vary") == "Accept-Encoding") != gz {
				t.Errorf("%s: Vary %q", hive, r.Header().Get("Vary"))
			}
		}
		if gz {
			if !bytes.Equal(gunzip(t, zipped.Body.Bytes()), plain.Body.Bytes()) {
				t.Errorf("%s: the gzip variant does not expand to the identity bytes", hive)
			}
			if zipped.Header().Get("Content-Length") != strconv.Itoa(zipped.Body.Len()) {
				t.Errorf("%s: Content-Length %s for %d gzip bytes", hive, zipped.Header().Get("Content-Length"), zipped.Body.Len())
			}
		} else if !bytes.Equal(zipped.Body.Bytes(), plain.Body.Bytes()) {
			t.Errorf("%s: a client that accepts gzip got other bytes from the plain hive", hive)
		}
	}
	// The conditional answer of a gzip hive names the Vary too.
	path := "v3/registration-gz/acme.widgets/index.json"
	etag := fx.get(path).Header().Get("ETag")
	if r := fx.get(path, "If-None-Match", etag); r.Code != 304 || r.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("304 of a gzip hive = %d, Vary %q", r.Code, r.Header().Get("Vary"))
	}
}

// TestR_NUGET_004_HivesFilterSemver2: the SemVer 2.0.0-specific version is in the semver2
// hive only; a package whose versions are all SemVer 2 is absent from the SemVer 1 hives.
func TestR_NUGET_004_HivesFilterSemver2(t *testing.T) {
	fx := newDocFixture(t)
	fx.put("1.0.0", true, `{}`)
	fx.put("1.1.0-ci.5+sha.abc", true, `{}`)
	fx.putAll("only.ci", registrationVersion("Only.CI", "1.0.0-ci.1", true, `{}`))

	versionsIn := func(path string) []string {
		t.Helper()
		resp := fx.get(path)
		if resp.Code != 200 {
			t.Fatalf("%s = %d %s", path, resp.Code, resp.Body)
		}
		var d struct {
			Items []struct {
				Items []struct {
					CatalogEntry struct {
						Version string `json:"version"`
					} `json:"catalogEntry"`
				} `json:"items"`
			} `json:"items"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range d.Items {
			for _, it := range p.Items {
				out = append(out, it.CatalogEntry.Version)
			}
		}
		return out
	}
	for hive, want := range map[string]string{
		"registration": "[1.0.0]", "registration-gz": "[1.0.0]", "registration-semver2": "[1.0.0 1.1.0-ci.5+sha.abc]",
	} {
		if got := fmt.Sprint(versionsIn("v3/" + hive + "/acme.widgets/index.json")); got != want {
			t.Errorf("%s lists %s, want %s", hive, got, want)
		}
	}
	for _, hive := range []string{"registration", "registration-gz"} {
		for _, path := range []string{"/only.ci/index.json", "/acme.widgets/1.1.0-ci.5.json", "/only.ci/page/1.0.0-ci.1/1.0.0-ci.1.json"} {
			if resp := fx.get("v3/" + hive + path); resp.Code != 404 {
				t.Errorf("%s%s = %d, want 404", hive, path, resp.Code)
			}
		}
	}
	if resp := fx.get("v3/registration-semver2/only.ci/index.json"); resp.Code != 200 {
		t.Errorf("semver2 index of the package = %d", resp.Code)
	}
	// The flat container shows every version.
	if got := fx.get("v3/flatcontainer/only.ci/index.json").Body.String(); got != `{"versions":["1.0.0-ci.1"]}` {
		t.Errorf("flat container of a SemVer 2 package = %s", got)
	}
}

// indexPage is a page as the registration index lists it.
type indexPage struct {
	ID     string            `json:"@id"`
	Count  int               `json:"count"`
	Lower  string            `json:"lower"`
	Upper  string            `json:"upper"`
	Parent string            `json:"parent"`
	Items  []json.RawMessage `json:"items"`
}

// pagesOf reads the registration index of acme.widgets in a hive and returns its count and
// pages.
func pagesOf(t *testing.T, fx *docFixture, hive string) (count int, pages []indexPage) {
	t.Helper()
	resp := fx.get("v3/" + hive + "/acme.widgets/index.json")
	if resp.Code != 200 {
		t.Fatalf("index = %d %s", resp.Code, resp.Body)
	}
	var d struct {
		Count int         `json:"count"`
		Items []indexPage `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d.Count, d.Items
}

// TestR_NUGET_009_PagingOfTheAcceptanceFixtures: with 130 versions the SemVer 2 index has 3
// pages without items and each page URL returns its leaves; with 100 versions it has 2
// inlined pages; with 10 versions one.
func TestR_NUGET_009_PagingOfTheAcceptanceFixtures(t *testing.T) {
	for _, tc := range []struct {
		n       int
		pages   int
		inlined bool
	}{{130, 3, false}, {100, 2, true}, {10, 1, true}} {
		t.Run(fmt.Sprintf("%d versions", tc.n), func(t *testing.T) {
			fx := newDocFixture(t)
			fx.putAll("acme.widgets", versionsOf(tc.n)...)
			count, pages := pagesOf(t, fx, "registration-semver2")
			if count != tc.pages || len(pages) != tc.pages {
				t.Fatalf("count %d, %d pages, want %d", count, len(pages), tc.pages)
			}
			seen := 0
			for _, p := range pages {
				if (len(p.Items) > 0) != tc.inlined {
					t.Errorf("page %s-%s has %d inlined items, inlined want %v", p.Lower, p.Upper, len(p.Items), tc.inlined)
				}
				u, err := url.Parse(p.ID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.inlined {
					seen += len(p.Items)
					continue
				}
				// A page that is not inlined is fetched from its own URL, which the @id is.
				rel, ok := strings.CutPrefix(u.String(), feedURL)
				if !ok {
					t.Fatalf("page @id %q is outside the feed", p.ID)
				}
				resp := fx.get(FeedSegment+"/"+rel, "Accept-Encoding", "gzip")
				if resp.Code != 200 {
					t.Fatalf("page %s = %d %s", p.ID, resp.Code, resp.Body)
				}
				var page indexPage
				if err := json.Unmarshal(gunzip(t, resp.Body.Bytes()), &page); err != nil {
					t.Fatal(err)
				}
				if page.ID != p.ID || page.Count != p.Count || len(page.Items) != p.Count || page.Lower != p.Lower || page.Upper != p.Upper ||
					page.Parent != feedURL+"registration-semver2/acme.widgets/index.json" {
					t.Errorf("page document %+v does not match its entry %+v", page, p)
				}
				seen += len(page.Items)
			}
			if seen != tc.n {
				t.Errorf("the pages hold %d leaves, want %d", seen, tc.n)
			}
		})
	}
}

// TestR_NUGET_009_PagingFollowsTheSettings: the page size and the inline threshold are the
// replica's nuget.registration settings, and a replica with other settings sends another
// ETag for the index.
func TestR_NUGET_009_PagingFollowsTheSettings(t *testing.T) {
	fx := newDocFixture(t)
	fx.putAll("acme.widgets", versionsOf(10)...)
	count, pages := pagesOf(t, fx, "registration")
	if count != 1 || len(pages[0].Items) != 10 {
		t.Fatalf("defaults: %d pages", count)
	}
	etag := fx.get("v3/registration/acme.widgets/index.json").Header().Get("ETag")

	small := newDocFixture(t)
	small.putAll("acme.widgets", versionsOf(10)...)
	bindPaging(small, 4, 5)
	count, pages = pagesOf(t, small, "registration")
	if count != 3 || pages[0].Count != 4 || pages[2].Count != 2 || len(pages[0].Items) != 0 {
		t.Errorf("page size 4, inline below 5: %d pages, first %+v", count, pages[0])
	}
	if got := small.get("v3/registration/acme.widgets/index.json").Header().Get("ETag"); got == etag {
		t.Error("another paging gives the same ETag")
	}
}

// TestR_NUGET_009_LeafDocument: the leaf URL answers @id, listed, packageContent,
// published and registration.
func TestR_NUGET_009_LeafDocument(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	resp := fx.get("v3/registration-gz/Acme.Widgets/1.2.3.0.json")
	want := `{"@id":"` + feedURL + `registration-gz/acme.widgets/1.2.3.json","listed":true,` +
		`"packageContent":"` + feedURL + `flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",` +
		`"published":"2026-10-28T09:14:03Z","registration":"` + feedURL + `registration-gz/acme.widgets/index.json"}`
	if resp.Code != 200 || resp.Body.String() != want {
		t.Errorf("leaf = %d %s\nwant %s", resp.Code, resp.Body, want)
	}
	if r := fx.get("v3/registration/acme.widgets/9.9.9.json"); r.Code != 404 {
		t.Errorf("leaf of an unknown version = %d", r.Code)
	}
}

// TestR_NUGET_030_ReplicasAnswerAlike: two replicas of one release over the same rows send
// the same bytes and the same ETag for every document, in both codings, and a document
// rendered twice on one replica (the second time from the cache) is the same.
func TestR_NUGET_030_ReplicasAnswerAlike(t *testing.T) {
	a, b := newDocFixture(t), newDocFixture(t)
	for _, fx := range []*docFixture{a, b} {
		widgets(fx)
		fx.putAll("acme.core", registrationVersion("Acme.Core", "2.0.0", true, `{}`))
	}
	for name, path := range documentURLs() {
		for _, enc := range []string{"", "gzip"} {
			first := a.get(path, "Accept-Encoding", enc)
			second := a.get(path, "Accept-Encoding", enc)
			other := b.get(path, "Accept-Encoding", enc)
			if first.Code != 200 {
				t.Fatalf("%s = %d", name, first.Code)
			}
			for label, r := range map[string]*formattest.Response{"again": second, "other replica": other} {
				if !bytes.Equal(first.Body.Bytes(), r.Body.Bytes()) || first.Header().Get("ETag") != r.Header().Get("ETag") {
					t.Errorf("%s (%q) differs on %s", name, enc, label)
				}
			}
		}
	}
}

// TestR_NUGET_002_RenditionsAreCachedByRevision: a second request for a document is served
// from the replica's cache without a read of the rows, the gzip variant is made from the
// cached identity bytes, and a commit makes the next request read again.
func TestR_NUGET_002_RenditionsAreCachedByRevision(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	path := "v3/registration-gz/acme.widgets/index.json"
	fx.get(path)
	reads := fx.store.readCount()
	fx.get(path)
	fx.get(path, "Accept-Encoding", "gzip")
	fx.get(path, "Accept-Encoding", "gzip")
	fx.do("HEAD", path, nil)
	if fx.store.readCount() != reads {
		t.Errorf("rows were read %d more times for documents already rendered", fx.store.readCount()-reads)
	}
	// A conditional hit reads nothing, with an empty cache of this document.
	fx.put("1.4.0", true, `{}`)
	etag := fx.get("v3/flatcontainer/acme.widgets/index.json").Header().Get("ETag")
	reads = fx.store.readCount()
	if r := fx.get("v3/flatcontainer/acme.widgets/index.json", "If-None-Match", etag); r.Code != 304 {
		t.Fatalf("conditional = %d", r.Code)
	}
	if fx.store.readCount() != reads {
		t.Error("a 304 read the rows")
	}
	fx.put("1.5.0", true, `{}`)
	if got := fx.get(path); !strings.Contains(string(gunzipIf(t, got)), "1.5.0") {
		t.Errorf("a document after a commit is the old one: %s", got.Body)
	}
}

func gunzipIf(t testing.TB, r *formattest.Response) []byte {
	if r.Header().Get("Content-Encoding") == "gzip" {
		return gunzip(t, r.Body.Bytes())
	}
	return r.Body.Bytes()
}

// exposureView is a view that exposes the versions of one package a test names.
type exposureView struct {
	packages map[string]bool
	versions map[string]bool
}

func (v exposureView) Package(name string) bool { return v.packages[name] }
func (v exposureView) Version(name, ver string) bool {
	return v.packages[name] && v.versions[ver]
}

// TestR_NUGET_034_DocumentsOnAnEndpointWithExposureRules: the version list and the three
// hives hold only exposed versions, paging runs over what is left, an ID with none left or
// not exposed is a 404, the validator carries the endpoint and its exposure fingerprint,
// and an endpoint with rules and no view exposes nothing.
func TestR_NUGET_034_DocumentsOnAnEndpointWithExposureRules(t *testing.T) {
	fx := newDocFixture(t)
	for _, v := range []string{"1.9.0", "2.0.0", "2.1.0-beta", "2.1.0"} {
		fx.putAll("acme.sdk", registrationVersion("Acme.Sdk", v, true, `{}`))
	}
	fx.put("1.0.0", true, `{}`)
	view := exposureView{packages: map[string]bool{"acme.sdk": true}, versions: map[string]bool{"2.0.0": true, "2.1.0": true}}
	customers := &formats.EndpointFacts{Name: "customers", Rules: true, ExposureFingerprint: "fp1", Exposure: view}

	body := func(path string, ep *formats.EndpointFacts) *formattest.Response { return fx.do("GET", path, ep) }
	if r := body("v3/flatcontainer/acme.sdk/index.json", customers); r.Body.String() != `{"versions":["2.0.0","2.1.0"]}` {
		t.Errorf("exposed version list = %d %s", r.Code, r.Body)
	}
	if r := body("v3/flatcontainer/acme.sdk/index.json", nil); r.Body.String() != `{"versions":["1.9.0","2.0.0","2.1.0-beta","2.1.0"]}` {
		t.Errorf("default endpoint list = %s", r.Body)
	}
	idx := body("v3/registration-semver2/acme.sdk/index.json", customers)
	var d struct {
		Count int `json:"count"`
		Items []struct {
			Count int    `json:"count"`
			Lower string `json:"lower"`
			Upper string `json:"upper"`
		} `json:"items"`
	}
	if err := json.Unmarshal(idx.Body.Bytes(), &d); err != nil || d.Count != 1 || d.Items[0].Count != 2 || d.Items[0].Lower != "2.0.0" || d.Items[0].Upper != "2.1.0" {
		t.Errorf("exposed index = %s (%v)", idx.Body, err)
	}
	if r := body("v3/registration/acme.sdk/1.9.0.json", customers); r.Code != 404 {
		t.Errorf("leaf of a hidden version = %d", r.Code)
	}
	if r := body("v3/registration/acme.sdk/page/1.9.0/1.9.0.json", customers); r.Code != 200 || !strings.Contains(r.Body.String(), `"count":0`) {
		t.Errorf("page of a hidden range = %d %s", r.Code, r.Body)
	}
	// A package the rules do not expose, and one with no exposed version, is missing.
	if r := body("v3/flatcontainer/acme.widgets/index.json", customers); r.Code != 404 {
		t.Errorf("a package outside the rules = %d", r.Code)
	}
	none := &formats.EndpointFacts{Name: "none", Rules: true, ExposureFingerprint: "fp2", Exposure: exposureView{
		packages: map[string]bool{"acme.sdk": true}, versions: map[string]bool{"0.0.1": true},
	}}
	for _, p := range []string{"v3/flatcontainer/acme.sdk/index.json", "v3/registration/acme.sdk/index.json", "v3/registration-semver2/acme.sdk/index.json"} {
		if r := body(p, none); r.Code != 404 {
			t.Errorf("%s with no exposed version = %d", p, r.Code)
		}
	}
	if r := body("v3/flatcontainer/acme.sdk/index.json", &formats.EndpointFacts{Name: "x", Rules: true}); r.Code != 404 {
		t.Errorf("rules without a view = %d", r.Code)
	}
	// The validator follows the endpoint and the fingerprint.
	etag := func(ep *formats.EndpointFacts) string {
		return body("v3/flatcontainer/acme.sdk/index.json", ep).Header().Get("ETag")
	}
	changed := *customers
	changed.ExposureFingerprint = "fp3"
	renamed := *customers
	renamed.Name = "partners"
	if etag(customers) == etag(nil) || etag(customers) == etag(&changed) || etag(customers) == etag(&renamed) {
		t.Error("the ETag ignores the endpoint or its exposure fingerprint")
	}
	if r := fx.do("GET", "v3/flatcontainer/acme.sdk/index.json", customers, "If-None-Match", etag(nil)); r.Code != 200 {
		t.Errorf("the validator of the default endpoint matched on customers: %d", r.Code)
	}
}

// fileFixture stores the files of 1.2.3 the way ingestion does (R-NUGET-005).
func fileFixture(fx *docFixture) (nupkg []byte) {
	fx.put("1.2.3", true, `{}`)
	return putFiles(fx.host)
}

// putFiles stores the .nupkg and the .nuspec of acme.widgets 1.2.3 the way ingestion does.
func putFiles(h *formattest.Host) (nupkg []byte) {
	nupkg = []byte("PK\x03\x04 not really a zip, but bytes")
	h.PutItem(NupkgPath("acme.widgets", "1.2.3"), "application/zip", nupkg)
	h.PutItem(NuspecPath("acme.widgets", "1.2.3"), "text/plain", []byte(`<?xml version="1.0"?><package/>`))
	return nupkg
}

// TestR_NUGET_008_FilesOfAVersion: the package and the .nuspec of a version are the stored
// bytes with the media type of the protocol, whatever the spelling of ID, version and file in
// the path; a version that is not stored is a 404.
func TestR_NUGET_008_FilesOfAVersion(t *testing.T) {
	fx := newDocFixture(t)
	nupkg := fileFixture(fx)
	for _, path := range []string{
		"v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg",
		"v3/flatcontainer/ACME.Widgets/1.2.3.0/Acme.Widgets.1.2.3.0.NUPKG",
		"v3/flatcontainer/acme.widgets/1.2.3+build.5/acme.widgets.1.2.3.nupkg",
	} {
		resp := fx.get(path)
		if resp.Code != 200 || !bytes.Equal(resp.Body.Bytes(), nupkg) {
			t.Errorf("%s = %d (%d bytes)", path, resp.Code, resp.Body.Len())
		}
		if ct := resp.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
		if resp.Header().Get("Content-Disposition") == "" || resp.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: the content headers are missing: %v", path, resp.Header())
		}
	}
	spec := fx.get("v3/flatcontainer/Acme.Widgets/1.2.3/Acme.Widgets.nuspec")
	if spec.Code != 200 || spec.Body.String() != `<?xml version="1.0"?><package/>` || spec.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("nuspec = %d %q %s", spec.Code, spec.Header().Get("Content-Type"), spec.Body)
	}
	head := fx.do("HEAD", "v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(nupkg)) {
		t.Errorf("HEAD = %d, %d body bytes, Content-Length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
	for _, path := range []string{
		"v3/flatcontainer/acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg",
		"v3/flatcontainer/acme.widgets/9.9.9/acme.widgets.nuspec",
		"v3/flatcontainer/nothing.here/1.2.3/nothing.here.1.2.3.nupkg",
	} {
		if resp := fx.get(path); resp.Code != 404 {
			t.Errorf("%s = %d, want 404", path, resp.Code)
		}
	}
	// A file name that disagrees with the directories above it never reaches the handler.
	for _, path := range []string{
		"v3/flatcontainer/acme.widgets/1.2.3/other.1.2.3.nupkg",
		"v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.4.nupkg",
	} {
		_, _, err := fx.f.Match(http.MethodGet, path)
		if e, ok := errs.As(err); !ok || e.Code != errs.CodeNotFound {
			t.Errorf("%s: Match = %v, want not_found", path, err)
		}
	}
}

// TestR_NUGET_013_FilesAskTheReadPermissionOnTheVersion: a download is authorized as a read
// of the ID and version, and a refusal serves no byte.
func TestR_NUGET_013_FilesAskTheReadPermissionOnTheVersion(t *testing.T) {
	deny := errs.E(errs.Forbidden, errs.CodeForbidden, "no")
	f := New()
	st := newDocStore()
	h := formattest.NewHost(f, formattest.HostOptions{Data: st, Allow: func(t formats.Target) error {
		if t.Version == "1.2.3" {
			return deny
		}
		return nil
	}})
	fx := &docFixture{t: t, f: f, host: h, store: st}
	fileFixture(fx)
	resp := fx.get("v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg")
	if resp.Code != 403 || resp.Body.Len() == 0 || strings.Contains(resp.Body.String(), "PK") {
		t.Errorf("denied download = %d %q", resp.Code, resp.Body)
	}
	var asked bool
	for _, c := range h.Authorizations() {
		if c.Target.Package == "acme.widgets" && c.Target.Version == "1.2.3" && len(c.Target.Actions) == 1 && c.Target.Actions[0] == formats.ActionRead {
			asked = true
		}
	}
	if !asked {
		t.Errorf("no read authorization of acme.widgets 1.2.3: %+v", h.Authorizations())
	}
}

// TestR_NUGET_034_FilesOfAHiddenVersion: the .nupkg and .nuspec of a version the endpoint
// does not expose answer 404, those of an exposed one are served.
func TestR_NUGET_034_FilesOfAHiddenVersion(t *testing.T) {
	fx := newDocFixture(t)
	fileFixture(fx)
	ep := func(versions ...string) *formats.EndpointFacts {
		v := exposureView{packages: map[string]bool{"acme.widgets": true}, versions: map[string]bool{}}
		for _, s := range versions {
			v.versions[s] = true
		}
		return &formats.EndpointFacts{Name: "customers", Rules: true, ExposureFingerprint: "fp", Exposure: v}
	}
	for _, file := range []string{"acme.widgets.1.2.3.nupkg", "acme.widgets.nuspec"} {
		path := "v3/flatcontainer/acme.widgets/1.2.3/" + file
		if r := fx.do("GET", path, ep("2.0.0")); r.Code != 404 {
			t.Errorf("hidden %s = %d", file, r.Code)
		}
		if r := fx.do("GET", path, ep("1.2.3")); r.Code != 200 {
			t.Errorf("exposed %s = %d", file, r.Code)
		}
	}
}

// TestR_NUGET_002_ErrorsOfTheDataPortAreServerFaults: a failing data port answers 5xx in the
// native error shape and a data port that is not bound is an internal error.
func TestR_NUGET_002_ErrorsOfTheDataPortAreServerFaults(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	broken := &failingStore{docStore: fx.store}
	fx.host = formattest.NewHost(fx.f, formattest.HostOptions{Data: broken, BaseURL: "https://trove.example.com"})
	fx.putAll("acme.widgets", registrationVersion("Acme.Widgets", "1.0.0", true, `{}`))
	for _, path := range []string{"v3/flatcontainer/acme.widgets/index.json", "v3/registration/acme.widgets/index.json"} {
		resp := fx.get(path)
		if resp.Code != 503 || !strings.HasPrefix(resp.Body.String(), "unavailable: ") {
			t.Errorf("%s = %d %q", path, resp.Code, resp.Body)
		}
	}
	// A damaged metadata object is a fault of the server, not of the client.
	bad := newDocFixture(t)
	bad.putAll("acme.widgets", registrationVersion("Acme.Widgets", "1.0.0", true, `{"tags":5}`))
	if resp := bad.get("v3/registration/acme.widgets/index.json"); resp.Code != 500 {
		t.Errorf("damaged metadata = %d %s", resp.Code, resp.Body)
	}
}

type failingStore struct{ *docStore }

func (*failingStore) Versions(context.Context, int64, string) ([]rows.VersionEntry, error) {
	return nil, errs.E(errs.Unavailable, errs.CodeUnavailable, "The database is not reachable.", errs.WithCause(errors.New("connection refused")))
}

func (*failingStore) Registration(context.Context, int64, string) (rows.RegistrationPackage, bool, error) {
	return rows.RegistrationPackage{}, false, errs.E(errs.Unavailable, errs.CodeUnavailable, "The database is not reachable.")
}

// TestR_NUGET_009_UnknownHiveIsNotARoute: a hive name the table does not know is not a
// route.
func TestR_NUGET_009_UnknownHiveIsNotARoute(t *testing.T) {
	fx := newDocFixture(t)
	widgets(fx)
	if _, err := fx.host.NewRequest(context.Background(), http.MethodGet, "v3/registration-semver3/acme.widgets/index.json", nil); err == nil {
		t.Error("an unknown hive is routed")
	}
}

// bindPaging gives the fixture's replica the paging settings.
func bindPaging(fx *docFixture, size, inline int) {
	fx.f.Bind(fx.host, &config.Config{NuGet: config.NuGet{Registration: config.NuGetRegistration{PageSize: size, InlineBelow: inline}}}, nil)
}
