package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

func listHost(t *testing.T, st *memStore, mods ...func(*formattest.HostOptions)) *formattest.Host {
	t.Helper()
	o := formattest.HostOptions{Data: st}
	for _, m := range mods {
		m(&o)
	}
	return formattest.NewHost(New(), o)
}

func get(t *testing.T, h *formattest.Host, path string) *formattest.Response {
	t.Helper()
	path, query, _ := strings.Cut(path, "?")
	resp, err := h.Serve(context.Background(), http.MethodGet, path, nil, false, func(r *http.Request) { r.URL.RawQuery = query })
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func registryCode(t *testing.T, resp *formattest.Response) string {
	t.Helper()
	var body struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("not an error body: %s (%v)", resp.Body, err)
	}
	return body.Errors[0].Code
}

func tagsOf(t *testing.T, resp *formattest.Response) (name string, tags []string) {
	t.Helper()
	var doc struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if doc.Tags == nil {
		t.Fatalf("tags is null, not a list: %s", resp.Body)
	}
	return doc.Name, doc.Tags
}

// TestR_OCI_015_TagsListPagesInByteOrder: with the tags a, b, c, n=2 answers a and b and a Link to last=b; following it answers c
// without a Link (R-OCI-015).
func TestR_OCI_015_TagsListPagesInByteOrder(t *testing.T) {
	st := newMemStore()
	for _, tag := range []string{"c", "a", "b"} {
		st.putTag("app", tag, digestN(1))
	}
	h := listHost(t, st)
	resp := get(t, h, "app/tags/list?n=2")
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d: %s", resp.Code, resp.Body)
	}
	name, tags := tagsOf(t, resp)
	if name != "it/main/app" || strings.Join(tags, ",") != "a,b" {
		t.Fatalf("name %q tags %q", name, tags)
	}
	link := resp.Header().Get("Link")
	const want = `</v2/it/main/app/tags/list>; rel="next"`
	if !strings.Contains(link, "n=2&last=b") || !strings.HasSuffix(link, `>; rel="next"`) || !strings.HasPrefix(link, "</v2/it/main/app/tags/list?") {
		t.Fatalf("Link %q, want %s with n=2&last=b", link, want)
	}
	if ct := resp.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	// Follow the link: the path and query are the ones the client sends.
	next := strings.TrimSuffix(strings.TrimPrefix(link, "</v2/it/main/"), `>; rel="next"`)
	resp = get(t, h, next)
	_, tags = tagsOf(t, resp)
	if resp.Code != http.StatusOK || strings.Join(tags, ",") != "c" || resp.Header().Get("Link") != "" {
		t.Fatalf("second page: %d %q link %q", resp.Code, tags, resp.Header().Get("Link"))
	}
}

func TestR_OCI_015_TagsListEdgeCases(t *testing.T) {
	st := newMemStore()
	for i := range 1005 {
		st.putTag("big", fmt.Sprintf("t%04d", i), digestN(1))
	}
	st.putTag("app", "1", digestN(1))
	st.putManifest(rows.Manifest{Name: "untagged", Digest: digestN(2), Size: 10})
	h := listHost(t, st)

	t.Run("the default and the cap are 1,000", func(t *testing.T) {
		for _, path := range []string{"big/tags/list", "big/tags/list?n=5000"} {
			resp := get(t, h, path)
			_, tags := tagsOf(t, resp)
			if len(tags) != 1000 || tags[999] != "t0999" || !strings.Contains(resp.Header().Get("Link"), "last=t0999") {
				t.Errorf("%s: %d tags, Link %q", path, len(tags), resp.Header().Get("Link"))
			}
		}
	})
	t.Run("n=0 is an empty list, not null", func(t *testing.T) {
		resp := get(t, h, "app/tags/list?n=0")
		_, tags := tagsOf(t, resp)
		if resp.Code != http.StatusOK || len(tags) != 0 || resp.Header().Get("Link") != "" {
			t.Fatalf("%d %q link %q", resp.Code, tags, resp.Header().Get("Link"))
		}
	})
	t.Run("a name with a manifest and no tag is an empty list", func(t *testing.T) {
		resp := get(t, h, "untagged/tags/list")
		_, tags := tagsOf(t, resp)
		if resp.Code != http.StatusOK || len(tags) != 0 {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
	})
	t.Run("a page past the last tag of a held name is empty", func(t *testing.T) {
		resp := get(t, h, "app/tags/list?last=zzz")
		_, tags := tagsOf(t, resp)
		if resp.Code != http.StatusOK || len(tags) != 0 {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
	})
	t.Run("a name the repository holds nothing of is NAME_UNKNOWN", func(t *testing.T) {
		resp := get(t, h, "nothing/tags/list")
		if resp.Code != http.StatusNotFound || registryCode(t, resp) != "NAME_UNKNOWN" {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
	})
	t.Run("a name with only a blob is held", func(t *testing.T) {
		st.l().blobs["blobonly"] = true
		if resp := get(t, h, "blobonly/tags/list"); resp.Code != http.StatusOK {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
	})
	t.Run("parameters outside their grammar are refused", func(t *testing.T) {
		for path, code := range map[string]string{
			"app/tags/list?n=x":      "UNSUPPORTED",
			"app/tags/list?n=-1":     "UNSUPPORTED",
			"app/tags/list?n=1.5":    "UNSUPPORTED",
			"app/tags/list?last=a/b": "TAG_INVALID",
			"app/tags/list?last=-x":  "TAG_INVALID",
		} {
			resp := get(t, h, path)
			if resp.Code != http.StatusBadRequest || registryCode(t, resp) != code {
				t.Errorf("%s: %d %s", path, resp.Code, resp.Body)
			}
		}
	})
	t.Run("the read is decided for the image name", func(t *testing.T) {
		denied := listHost(t, st, func(o *formattest.HostOptions) {
			o.Allow = func(tg formats.Target) error {
				if tg.Package == "app" {
					return errs.E(errs.Forbidden, errs.CodeForbidden, "no")
				}
				return nil
			}
		})
		if resp := get(t, denied, "app/tags/list"); resp.Code != http.StatusForbidden || registryCode(t, resp) != "DENIED" {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
		if resp := get(t, denied, "untagged/tags/list"); resp.Code != http.StatusOK {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
		found := false
		for _, a := range denied.Authorizations() {
			found = found || (a.Target.Package == "app" && len(a.Target.Actions) == 1 && a.Target.Actions[0] == formats.ActionRead)
		}
		if !found {
			t.Errorf("no read decision for the name: %+v", denied.Authorizations())
		}
	})
}

func referrer(n byte, artifactType string, subject [32]byte, annotations string) rows.Manifest {
	m := rows.Manifest{
		Name: "app", Digest: digestN(n), MediaType: "application/vnd.oci.image.manifest.v1+json", ArtifactType: artifactType,
		Subject: &subject, Size: 100 + int64(n),
	}
	if annotations != "" {
		m.Annotations = json.RawMessage(annotations)
	}
	return m
}

type indexDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Manifests     []struct {
		MediaType    string            `json:"mediaType"`
		Digest       string            `json:"digest"`
		Size         int64             `json:"size"`
		ArtifactType string            `json:"artifactType"`
		Annotations  map[string]string `json:"annotations"`
	} `json:"manifests"`
}

func referrersIndexOf(t *testing.T, resp *formattest.Response) indexDoc {
	t.Helper()
	var doc indexDoc
	if err := json.Unmarshal(resp.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %s: %v", resp.Body, err)
	}
	if doc.Manifests == nil {
		t.Fatalf("manifests is null, not a list: %s", resp.Body)
	}
	return doc
}

// TestR_OCI_016_ReferrersListTheManifestsOfTheSubject: two signatures with the subject are both listed; with the artifactType filter
// only the matching one is, and the answer says the filter was applied (R-OCI-016).
func TestR_OCI_016_ReferrersListTheManifestsOfTheSubject(t *testing.T) {
	const sig, sbom = "application/vnd.dev.cosign.artifact.sig.v1+json", "application/spdx+json"
	subject, other := digestN(9), digestN(8)
	st := newMemStore()
	st.putManifest(referrer(1, sig, subject, `{"org.example.k":"v1"}`))
	st.putManifest(referrer(2, sig, subject, ""))
	st.putManifest(referrer(3, sbom, subject, ""))
	st.putManifest(referrer(4, sig, other, ""))
	h := listHost(t, st)
	base := "app/referrers/" + digestText(subject)

	resp := get(t, h, base)
	doc := referrersIndexOf(t, resp)
	if resp.Code != http.StatusOK || resp.Header().Get("Content-Type") != "application/vnd.oci.image.index.v1+json" || doc.SchemaVersion != 2 ||
		doc.MediaType != "application/vnd.oci.image.index.v1+json" {
		t.Fatalf("%d %v %+v", resp.Code, resp.Header(), doc)
	}
	if len(doc.Manifests) != 3 || doc.Manifests[0].Digest != digestText(digestN(1)) || doc.Manifests[0].ArtifactType != sig ||
		doc.Manifests[0].Size != 101 || doc.Manifests[0].Annotations["org.example.k"] != "v1" || doc.Manifests[2].ArtifactType != sbom {
		t.Fatalf("descriptors %+v", doc.Manifests)
	}
	if resp.Header().Get("OCI-Filters-Applied") != "" || resp.Header().Get("Link") != "" {
		t.Errorf("headers without a filter: %v", resp.Header())
	}

	resp = get(t, h, base+"?artifactType="+url.QueryEscape(sig))
	doc = referrersIndexOf(t, resp)
	if len(doc.Manifests) != 2 || resp.Header().Get("OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("filtered: %+v %v", doc.Manifests, resp.Header())
	}
	// A filter that matches nothing is an empty index with the header.
	resp = get(t, h, base+"?artifactType=application/none")
	if doc = referrersIndexOf(t, resp); resp.Code != http.StatusOK || len(doc.Manifests) != 0 || resp.Header().Get("OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("no match: %d %s %v", resp.Code, resp.Body, resp.Header())
	}
}

func TestR_OCI_016_ReferrersEdgeCases(t *testing.T) {
	st := newMemStore()
	h := listHost(t, st)
	t.Run("a subject that does not exist is an empty index", func(t *testing.T) {
		resp := get(t, h, "app/referrers/"+digestText(digestN(5)))
		if doc := referrersIndexOf(t, resp); resp.Code != http.StatusOK || len(doc.Manifests) != 0 || doc.SchemaVersion != 2 {
			t.Fatalf("%d %s", resp.Code, resp.Body)
		}
	})
	t.Run("an invalid digest is DIGEST_INVALID", func(t *testing.T) {
		for _, d := range []string{"sha256:xyz", "sha512:" + strings.Repeat("a", 128), "md5:abc", "sha256:" + strings.Repeat("A", 64)} {
			resp := get(t, h, "app/referrers/"+d)
			if resp.Code != http.StatusBadRequest || registryCode(t, resp) != "DIGEST_INVALID" {
				t.Errorf("%s: %d %s", d, resp.Code, resp.Body)
			}
		}
		resp := get(t, h, "app/referrers/"+digestText(digestN(5))+"?last=nope")
		if resp.Code != http.StatusBadRequest || registryCode(t, resp) != "DIGEST_INVALID" {
			t.Errorf("last: %d %s", resp.Code, resp.Body)
		}
	})
	t.Run("an empty artifactType filters nothing and says nothing", func(t *testing.T) {
		resp := get(t, h, "app/referrers/"+digestText(digestN(5))+"?artifactType=")
		if resp.Code != http.StatusOK || resp.Header().Get("OCI-Filters-Applied") != "" {
			t.Fatalf("%d %v", resp.Code, resp.Header())
		}
	})
	t.Run("an artifactType above 255 bytes is refused", func(t *testing.T) {
		resp := get(t, h, "app/referrers/"+digestText(digestN(5))+"?artifactType="+strings.Repeat("a", 256))
		if resp.Code != http.StatusBadRequest {
			t.Errorf("%d %s", resp.Code, resp.Body)
		}
	})
}

// TestR_OCI_016_ReferrersContinueWithALink: a page holds at most 1,000 descriptors; a longer list continues with a Link whose last
// parameter is the digest the page ended at (R-OCI-016).
func TestR_OCI_016_ReferrersContinueWithALink(t *testing.T) {
	subject := digestN(250)
	st := newMemStore()
	for i := range 1003 {
		var d [32]byte
		d[0], d[1], d[31] = byte(i>>8), byte(i), 1
		m := referrer(0, "a/b", subject, "")
		m.Digest = d
		st.putManifest(m)
	}
	h := listHost(t, st)
	base := "app/referrers/" + digestText(subject)
	resp := get(t, h, base+"?artifactType=a/b")
	doc := referrersIndexOf(t, resp)
	link := resp.Header().Get("Link")
	if len(doc.Manifests) != 1000 || !strings.Contains(link, "last="+strings.ReplaceAll(doc.Manifests[999].Digest, ":", "%3A")) || !strings.Contains(link, "artifactType=a%2Fb") {
		t.Fatalf("%d descriptors, Link %q", len(doc.Manifests), link)
	}
	next := strings.TrimSuffix(strings.TrimPrefix(link, "</v2/it/main/"), `>; rel="next"`)
	resp = get(t, h, next)
	doc = referrersIndexOf(t, resp)
	if len(doc.Manifests) != 3 || resp.Header().Get("Link") != "" {
		t.Fatalf("second page: %d descriptors, Link %q", len(doc.Manifests), resp.Header().Get("Link"))
	}
}

func allowAll(ms []rows.Manifest) ([]bool, error) {
	out := make([]bool, len(ms))
	for i := range out {
		out[i] = true
	}
	return out, nil
}

// each lifts a decision on one row to the listing form.
func each(f func(rows.Manifest) bool) func([]rows.Manifest) ([]bool, error) {
	return func(ms []rows.Manifest) ([]bool, error) {
		out := make([]bool, len(ms))
		for i, m := range ms {
			out[i] = f(m)
		}
		return out, nil
	}
}

func TestR_OCI_016_APageIsBoundedByBytesAndReportsCutAnnotations(t *testing.T) {
	big := `{"k":"` + strings.Repeat("x", 60<<10) + `"}`
	subject := digestN(1)
	st := newMemStore()
	for i := range 100 {
		m := referrer(byte(i+2), "t", subject, big)
		if i == 1 {
			m.AnnotationsCut = true
		}
		st.putManifest(m)
	}
	pg, err := readReferrers(context.Background(), st, 0, "app", subject, "", nil, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range pg.descs {
		total += len(d.Annotations)
	}
	if !pg.more || total > MaxReferrersBytes || len(pg.descs) < 60 || len(pg.descs) > 70 || !pg.cut {
		t.Fatalf("%d descriptors, %d bytes, more %v, cut %v", len(pg.descs), total, pg.more, pg.cut)
	}
	if pg.cursor != digestN(byte(len(pg.descs)+1)) {
		t.Errorf("the page ends after the digest of its last descriptor, cursor %x", pg.cursor[:2])
	}
	// The first descriptor always goes in, whatever its size; no rows is an empty list and no Link.
	one := newMemStore()
	one.putManifest(referrer(2, "t", subject, big))
	if pg, _ = readReferrers(context.Background(), one, 0, "app", subject, "", nil, allowAll); len(pg.descs) != 1 || pg.more {
		t.Fatalf("one row: %d, more %v", len(pg.descs), pg.more)
	}
	if pg, _ = readReferrers(context.Background(), newMemStore(), 0, "app", subject, "", nil, allowAll); pg.descs == nil || len(pg.descs) != 0 || pg.more || pg.cut {
		t.Fatalf("no rows: %+v", pg)
	}
}

// TestR_OCI_016_ARowTheCallerMayNotReadIsLeftOutAndTheCursorPassesIt: the rows the allow function refuses are not listed, are not counted
// towards the page, and the Link continues after the last row read.
func TestR_OCI_016_ARowTheCallerMayNotReadIsLeftOutAndTheCursorPassesIt(t *testing.T) {
	subject := digestN(250)
	st := newMemStore()
	for i := range 4500 {
		var d [32]byte
		d[0], d[1], d[31] = byte(i>>8), byte(i), 1
		m := referrer(0, "t", subject, "")
		m.Digest = d
		st.putManifest(m)
	}
	odd := each(func(m rows.Manifest) bool { return m.Digest[1]%2 == 0 })
	pg, err := readReferrers(context.Background(), st, 0, "app", subject, "", nil, odd)
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.descs) != 1000 || !pg.more {
		t.Fatalf("%d descriptors, more %v", len(pg.descs), pg.more)
	}
	for _, d := range pg.descs {
		if dd, _ := digestFromText(d.Digest); dd[1]%2 != 0 {
			t.Fatalf("a hidden row is listed: %s", d.Digest)
		}
	}
	// Rows that are all hidden end the request at the scan budget with a refusal, not with a read of the whole table and not with a Link that
	// names a hidden row.
	none := each(func(rows.Manifest) bool { return false })
	c := &chunkCounter{memStore: st}
	pg, err = readReferrers(context.Background(), c, 0, "app", subject, "", nil, none)
	if err == nil || len(pg.descs) != 0 || c.rows > referrersScanBudget+referrersChunk {
		t.Fatalf("%+v %v after %d rows: the list cannot go on without naming a hidden row and is refused", pg, err, c.rows)
	}
}

func TestR_OCI_016_ACutAnnotationSetIsReportedInAHeader(t *testing.T) {
	st := newMemStore()
	m := referrer(1, "t", digestN(9), `{"a":"b"}`)
	m.AnnotationsCut = true
	st.putManifest(m)
	resp := get(t, listHost(t, st), "app/referrers/"+digestText(digestN(9)))
	if resp.Header().Get("Trove-Annotations-Cut") != "true" {
		t.Fatalf("headers %v", resp.Header())
	}
}

// chunkCounter counts the rows and the statements of the referrers reads.
type chunkCounter struct {
	*memStore
	statements, rows int
}

func (c *chunkCounter) Referrers(ctx context.Context, repo int64, name string, subject [32]byte, at string, after *[32]byte, limit int) ([]rows.Manifest, error) {
	out, err := c.memStore.Referrers(ctx, repo, name, subject, at, after, limit)
	c.statements++
	c.rows += len(out)
	return out, err
}

// TestR_OCI_016_AReferrersPageReadsOnlyWhatItCanHold: rows with the largest annotations the table admits are read in chunks until the
// page of 4 MiB is full, not 1,001 at a time (R-OCI-016).
func TestR_OCI_016_AReferrersPageReadsOnlyWhatItCanHold(t *testing.T) {
	subject := digestN(250)
	st := newMemStore()
	big := `{"k":"` + strings.Repeat("x", 60<<10) + `"}`
	for i := range 400 {
		var d [32]byte
		d[0], d[1], d[31] = byte(i>>8), byte(i), 1
		m := referrer(0, "t", subject, big)
		m.Digest = d
		st.putManifest(m)
	}
	c := &chunkCounter{memStore: st}
	pg, err := readReferrers(context.Background(), c, 0, "app", subject, "", nil, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if !pg.more || len(pg.descs) < 60 || len(pg.descs) > 70 {
		t.Fatalf("%d descriptors, more %v", len(pg.descs), pg.more)
	}
	if c.rows > 140 || c.statements < 2 {
		t.Fatalf("%d rows read in %d statements for a page of %d descriptors", c.rows, c.statements, len(pg.descs))
	}
}

// TestR_OCI_016_AListThatGoesOnIsNeverAnsweredWithoutALink: rows whose annotations are stored in the jsonb text form (a space after every
// colon and comma) are larger as read than as encoded in the answer; the page is measured on the answer's own encoding, so it ends
// where the 4 MiB end and says so, and never stops early with every row it read.
func TestR_OCI_016_AListThatGoesOnIsNeverAnsweredWithoutALink(t *testing.T) {
	subject := digestN(250)
	st := newMemStore()
	var b strings.Builder
	b.WriteByte('{')
	for i := range 2500 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `"a%04d": "b"`, i)
	}
	b.WriteByte('}')
	for i := range 200 {
		var d [32]byte
		d[0], d[1], d[31] = byte(i>>8), byte(i), 1
		m := referrer(0, "t", subject, b.String())
		m.Digest = d
		st.putManifest(m)
	}
	resp := get(t, listHost(t, st), "app/referrers/"+digestText(subject))
	doc := referrersIndexOf(t, resp)
	link := resp.Header().Get("Link")
	if resp.Code != http.StatusOK || (len(doc.Manifests) < 200 && link == "") {
		t.Fatalf("status %d, %d of 200 referrers answered and Link %q", resp.Code, len(doc.Manifests), link)
	}
	if resp.Body.Len() > MaxReferrersBytes+1024 {
		t.Errorf("the answer is %d bytes", resp.Body.Len())
	}
	// Following the Links lists every referrer exactly once.
	seen := map[string]bool{}
	for {
		for _, d := range doc.Manifests {
			if seen[d.Digest] {
				t.Fatalf("%s listed twice", d.Digest)
			}
			seen[d.Digest] = true
		}
		if link == "" {
			break
		}
		next := strings.TrimSuffix(strings.TrimPrefix(link, "</v2/it/main/"), `>; rel="next"`)
		resp = get(t, listHost(t, st), next)
		doc, link = referrersIndexOf(t, resp), resp.Header().Get("Link")
	}
	if len(seen) != 200 {
		t.Fatalf("%d referrers listed over the pages, want 200", len(seen))
	}
}
