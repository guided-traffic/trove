//go:build integration

package files

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/oci"
)

func r3Referrer(cfg, layer, subject string, subjectSize int) []byte {
	return []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"artifactType":"application/vnd.example.sig",`+
		`"config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":%q,"size":2},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":5}],`+
		`"subject":{"mediaType":%q,"digest":%q,"size":%d}}`, ociCT, cfg, layer, ociCT, subject, subjectSize))
}

// The tags list decides each tag with the audited decision: a reader whose grant excludes some tags makes one authz.denied event per hidden
// tag per request, and the Link of a page names the store's last tag, hidden or not.
func TestR_OCI_015_AListOfHiddenTagsWritesAtMostOneAuditEventAndNamesNoHiddenTag(t *testing.T) {
	f := newOCIFixture(t)
	f.e.user("peeker")
	f.e.grant(f.r, "oci-peek", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"**/_tags/x*"}}, map[string][]string{"user:peeker": {"read"}})
	cfg, layer := f.blob("app", []byte("{}")), f.blob("app", []byte("layer"))
	m := ociManifest(cfg, layer)
	for _, tag := range []string{"v1", "v2"} {
		wantOCI(t, f.push("writer", "app", tag, m, ociCT), 201, "")
	}
	for i := 1; i <= 30; i++ {
		wantOCI(t, f.push("writer", "app", fmt.Sprintf("x%02d", i), m, ociCT), 201, "")
	}
	wantOCI(t, f.push("writer", "app", "z1", m, ociCT), 201, "")
	time.Sleep(2 * time.Second)
	before := len(f.r.rec.ofType("authz.denied"))
	page := f.r.get("peeker", "/api/oci/it/oci-local/app/tags/list?n=10")
	var doc struct{ Tags []string }
	_ = json.Unmarshal(page.body, &doc)
	time.Sleep(500 * time.Millisecond)
	afterPage := len(f.r.rec.ofType("authz.denied"))
	full := f.r.get("peeker", "/api/oci/it/oci-local/app/tags/list")
	time.Sleep(500 * time.Millisecond)
	afterFull := len(f.r.rec.ofType("authz.denied"))
	t.Logf("n=10: %d %s, Link %q, authz.denied +%d", page.status, page.body, page.header.Get("Link"), afterPage-before)
	t.Logf("full list: %d %s, authz.denied +%d", full.status, full.body, afterFull-afterPage)
	if afterFull-afterPage > 1 || afterPage-before > 1 {
		t.Errorf("FLOOD: a tags list wrote %d and %d authz.denied events", afterPage-before, afterFull-afterPage)
	}
	if link := page.header.Get("Link"); strings.Contains(link, "last=x") {
		t.Errorf("the Link names a tag the caller may not read: %s", link)
	}
	for _, tag := range doc.Tags {
		if strings.HasPrefix(tag, "x") {
			t.Errorf("a hidden tag is listed: %s", tag)
		}
	}
}

// A delete by digest decides delete for the manifest's tags, referrers and children (Q-OCI-76), but the hook's cascade goes further: the
// tags of a referrer go with the referrer.
func TestR_OCI_018_TheCascadeOfADeleteByDigestIsHeldToTheGrantsAtEveryDepth(t *testing.T) {
	f := newOCIFixture(t)
	f.e.user("deleter")
	f.e.grant(f.r, "oci-del", []string{"oci-local"}, authz.PatternLists{Exclude: []string{"**/_tags/release-*"}}, map[string][]string{"user:deleter": {"read", "delete"}})
	cfg, layer := f.blob("app", []byte("{}")), f.blob("app", []byte("layer"))
	m := ociManifest(cfg, layer)
	wantOCI(t, f.push("writer", "app", "1", m, ociCT), 201, "")
	sig := r3Referrer(cfg, layer, shaOf(m), len(m))
	wantOCI(t, f.push("writer", "app", "release-sig", sig, ociCT), 201, "")
	time.Sleep(2 * time.Second)
	direct := f.r.do("deleter", http.MethodDelete, "/api/oci/it/oci-local/app/manifests/release-sig", nil)
	t.Logf("direct DELETE of the excluded tag: %d %s", direct.status, direct.body)
	res := f.r.do("deleter", http.MethodDelete, "/api/oci/it/oci-local/app/manifests/"+shaOf(m), nil)
	left := f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/release-sig'`)
	t.Logf("DELETE of the subject by digest: %d %s; excluded tag of the referrer left: %d", res.status, res.body, left)
	if direct.status != http.StatusForbidden || res.status != http.StatusForbidden || left != 1 {
		t.Errorf("direct %d, by digest %d, excluded tag of the referrer left %d: the cascade took a tag the deleter may not delete", direct.status, res.status, left)
	}
	if f.count(`SELECT count(*) FROM items WHERE path = $1`, oci.ManifestPath("app", shaOf(m))) != 1 {
		t.Error("the refused delete removed the subject")
	}
	// The same removal through /files/ is held to the grants the same way: the cascade of the hook is guarded by the request.
	viaFiles := f.r.do("deleter", http.MethodDelete, "/files/it/oci-local/"+oci.ManifestPath("app", shaOf(m)), nil)
	if viaFiles.status != http.StatusForbidden || f.count(`SELECT count(*) FROM items WHERE path = 'app/_tags/release-sig'`) != 1 {
		t.Errorf("/files/ answered %d %s: the cascade took a tag the deleter may not delete", viaFiles.status, viaFiles.body)
	}
}
