package galaxydoc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
)

var update = flag.Bool("update", false, "rewrite the golden files of testdata")

var addr = galaxydoc.Addr{Repo: "https://trove.example.com/api/ansible/web/ansible-local/", Namespace: "acme", Name: "tools"}

var (
	t0 = time.Date(2026, 11, 10, 12, 30, 45, 123456000, time.UTC)
	t1 = time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
)

func sha(b byte) (out [32]byte) {
	for i := range out {
		out[i] = b
	}
	return out
}

func version() galaxydoc.Version {
	return galaxydoc.Version{
		Namespace: "acme", Name: "tools", Version: "1.2.0", RequiresAnsible: ">=2.14.0", SHA256: sha(0xab), Size: 4096,
		Dependencies: map[string]string{"community.general": ">=1.0.0", "ansible.posix": "*"},
		Info:         json.RawMessage(`{"authors":["A <a@example.com>"],"description":"Tools & more","license":["MIT"],"readme":"README.md","tags":["tools"],"x_extra":{"k":1}}`),
		CreatedAt:    t0, UpdatedAt: t1, CollectionID: "8f1c2a4e-0000-4000-8000-000000000001",
	}
}

// golden compares got with testdata/<name>, or rewrites it with -update. The files are the shapes the public Galaxy writes for the
// fields a client reads (R-ANS-004; the recorded documents of the counting stub's corpus pin the same shapes in its own tests).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, got, "", "  "); err != nil {
		t.Fatalf("%s is not JSON: %v\n%s", name, err, got)
	}
	pretty.WriteByte('\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, pretty.Bytes()) {
		t.Errorf("%s differs:\n%s", name, pretty.String())
	}
}

func TestR_ANS_003_RootListsV1OnlyWhenAsked(t *testing.T) {
	golden(t, "root_v3.json", galaxydoc.RenderRoot(false))
	golden(t, "root_v1.json", galaxydoc.RenderRoot(true))
}

func TestR_ANS_004_CollectionDocument(t *testing.T) {
	golden(t, "collection.json", galaxydoc.RenderCollection(galaxydoc.CollectionSummary{
		Namespace: "acme", Name: "tools", CollectionID: "8f1c2a4e-0000-4000-8000-000000000001", HighestVersion: "1.2.0", CreatedAt: t0, UpdatedAt: t1,
	}, addr))
}

func TestR_ANS_004_VersionDocumentHasWhatTheClientReadsAndAddressesOnTheRepository(t *testing.T) {
	doc := galaxydoc.RenderVersion(version(), addr)
	golden(t, "version.json", doc)
	var got struct {
		DownloadURL string `json:"download_url"`
		Href        string `json:"href"`
		Artifact    struct {
			SHA256, Filename string
			Size             int64
		}
		Metadata map[string]json.RawMessage
	}
	if err := json.Unmarshal(doc, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.DownloadURL, addr.Repo+"v3/artifacts/collections/") || got.Artifact.Filename != "acme-tools-1.2.0.tar.gz" ||
		got.Href != addr.Repo+"v3/collections/acme/tools/versions/1.2.0/" || got.Artifact.SHA256 != strings.Repeat("ab", 32) {
		t.Errorf("document = %+v", got)
	}
	for _, dropped := range []string{"contents", "docs_blob", "manifest", "files"} {
		if _, ok := got.Metadata[dropped]; ok {
			t.Errorf("metadata holds %s", dropped)
		}
	}
	if !bytes.Contains(doc, []byte("Tools & more")) {
		t.Error("an ampersand is escaped; a plain JSON writer does not")
	}
}

func TestR_ANS_004_VersionDocumentOfAVersionWithoutMetadataHasTheDefaults(t *testing.T) {
	v := galaxydoc.Version{Namespace: "acme", Name: "bare", Version: "0.1.0", SHA256: sha(1), CreatedAt: t0, UpdatedAt: t0, CollectionID: "id"}
	golden(t, "version_bare.json", galaxydoc.RenderVersion(v, addr))
}

func TestR_ANS_013_SignaturesPassThroughOpaque(t *testing.T) {
	v := version()
	v.Signatures = []json.RawMessage{json.RawMessage(`{"signature":"-----BEGIN PGP SIGNATURE-----\nAAAA","pubkey_fingerprint":"ABCD"}`)}
	doc := galaxydoc.RenderVersion(v, addr)
	var got struct {
		Signatures []json.RawMessage `json:"signatures"`
	}
	if err := json.Unmarshal(doc, &got); err != nil || len(got.Signatures) != 1 || !bytes.Contains(got.Signatures[0], []byte(`"pubkey_fingerprint":"ABCD"`)) {
		t.Fatalf("signatures = %s, %v", got.Signatures, err)
	}
}

func entries(n int) []galaxydoc.VersionEntry {
	var out []galaxydoc.VersionEntry
	for i := range n {
		out = append(out, galaxydoc.VersionEntry{Version: "1." + string(rune('0'+i)) + ".0", CreatedAt: t0, UpdatedAt: t1})
	}
	out[0].RequiresAnsible = ">=2.14"
	return out
}

func TestR_ANS_004_VersionListPagesWithLinksOnTheRepository(t *testing.T) {
	golden(t, "versions_first.json", galaxydoc.RenderVersionList(entries(2), 5, 2, 0, addr))
	golden(t, "versions_middle.json", galaxydoc.RenderVersionList(entries(2), 5, 2, 2, addr))
	golden(t, "versions_last.json", galaxydoc.RenderVersionList(entries(1), 5, 2, 4, addr))
	golden(t, "versions_empty.json", galaxydoc.RenderVersionList(nil, 0, 100, 0, addr))
	type page struct {
		Meta  struct{ Count int }
		Links map[string]*string
	}
	var p page
	if err := json.Unmarshal(galaxydoc.RenderVersionList(nil, 3, 100, 500, addr), &p); err != nil || p.Meta.Count != 3 || p.Links["next"] != nil {
		t.Errorf("an offset beyond the count: %+v, %v", p, err)
	}
	if err := json.Unmarshal(galaxydoc.RenderVersionList(entries(2), 5, 0, 0, addr), &p); err != nil || !strings.Contains(*p.Links["first"], "limit=100&offset=0") {
		t.Errorf("a limit of 0 is the default: %+v, %v", p.Links, err)
	}
}

func TestR_ANS_004_PageLimitIsClamped(t *testing.T) {
	for in, want := range map[int]int{-5: 100, 0: 100, 1: 1, 100: 100, 101: 100, 5000: 100} {
		if got := galaxydoc.PageLimit(in); got != want {
			t.Errorf("PageLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestR_ANS_009_TaskDocuments(t *testing.T) {
	ok := galaxydoc.Task{
		ID: "6f1c0000-0000-4000-8000-000000000002", State: galaxydoc.TaskCompleted, Namespace: "acme", Name: "tools", Version: "1.0.0",
		CreatedAt: t0, StartedAt: t0, UpdatedAt: t0, FinishedAt: t0,
		Messages: []galaxydoc.TaskMessage{{Level: "INFO", Message: "Collection acme.tools 1.0.0 stored (sha256 d3499c)", Time: t0}},
	}
	golden(t, "task_completed.json", galaxydoc.RenderTask(ok))
	bad := ok
	bad.State, bad.ErrorCode, bad.ErrorDescription = galaxydoc.TaskFailed, "invalid_archive", "The archive holds a link."
	bad.Messages = nil
	golden(t, "task_failed.json", galaxydoc.RenderTask(bad))
}

var digest = strings.Repeat("ab", 32)

// upstreamVersionDoc is a version document in the shape of the public Galaxy, with the plugin listing that makes it large.
var upstreamVersionDoc = `{
 "version":"10.0.0","href":"/api/v3/plugin/ansible/content/published/collections/index/community/general/versions/10.0.0/",
 "created_at":"2024-11-04T10:00:00.123456Z","updated_at":"2024-11-04T10:00:01.000000Z","requires_ansible":">=2.15.0","marks":[],
 "artifact":{"filename":"community-general-10.0.0.tar.gz","sha256":"` + digest + `","size":1234},
 "collection":{"id":"11111111-2222-3333-4444-555555555555","name":"general","href":"/x/"},
 "download_url":"https://galaxy.ansible.com/api/v3/plugin/ansible/content/published/collections/artifacts/community-general-10.0.0.tar.gz",
 "name":"general","namespace":{"name":"community"},
 "signatures":[{"signature":"AAAA","pubkey_fingerprint":"ABCD","signing_service":null}],
 "metadata":{"authors":["Ansible (https://github.com/ansible)"],"dependencies":{"ansible.posix":">=1.0.0"},"description":"d",
  "documentation":"","homepage":"","issues":"","license":["GPL-3.0-or-later"],"license_file":null,"readme":"README.md",
  "repository":"https://github.com/ansible-collections/community.general","tags":["tools"],
  "contents":[{"name":"x","content_type":"module","description":"` + "{{LONG}}" + `"}],"docs_blob":{"collection_readme":{"html":"<p>x</p>"}},
  "manifest":{"a":1},"files":[{"name":"x"}]}
}`

func TestR_ANS_017_AnUpstreamVersionDocumentIsParsedAndServedWithoutThePluginListing(t *testing.T) {
	doc := strings.Replace(upstreamVersionDoc, "{{LONG}}", strings.Repeat("x", 100_000), 1)
	v, dl, err := galaxydoc.ParseUpstreamVersion([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if v.Namespace != "community" || v.Name != "general" || v.Version != "10.0.0" || v.RequiresAnsible != ">=2.15.0" || v.Size != 1234 ||
		dl != "https://galaxy.ansible.com/api/v3/plugin/ansible/content/published/collections/artifacts/community-general-10.0.0.tar.gz" ||
		v.Dependencies["ansible.posix"] != ">=1.0.0" || len(v.Signatures) != 1 || v.CollectionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("version = %+v, download %s", v, dl)
	}
	if len(v.Info) > 1000 {
		t.Errorf("Info is %d bytes: the plugin listing was kept", len(v.Info))
	}
	out := galaxydoc.RenderVersion(v, galaxydoc.Addr{Repo: "https://t.example/api/ansible/p/r/"})
	golden(t, "version_rewritten.json", out)
	for _, leak := range []string{"galaxy.ansible.com/api/v3", "contents", "docs_blob", "html"} {
		if bytes.Contains(out, []byte(leak)) {
			t.Errorf("the rewritten document holds %q", leak)
		}
	}
	// The rewrite is a fixed point: parsing the rendered document and rendering it again gives the same bytes.
	again, _, err := galaxydoc.ParseUpstreamVersion(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := galaxydoc.RenderVersion(again, galaxydoc.Addr{Repo: "https://t.example/api/ansible/p/r/"}); !bytes.Equal(got, out) {
		t.Errorf("rendering twice differs:\n%s\n%s", got, out)
	}
}

func TestR_ANS_017_AnUpstreamVersionDocumentThatIsNotAVersionIsRefused(t *testing.T) {
	good := strings.Replace(upstreamVersionDoc, "{{LONG}}", "x", 1)
	for name, doc := range map[string]string{
		"not JSON":          "{",
		"an array":          "[]",
		"no namespace":      strings.Replace(good, `"namespace":{"name":"community"}`, `"namespace":null`, 1),
		"no version":        strings.Replace(good, `"version":"10.0.0",`, "", 1),
		"names that differ": strings.Replace(good, `"name":"general","href":"/x/"`, `"name":"other","href":"/x/"`, 1),
		"no download_url":   strings.Replace(good, `"download_url"`, `"download_urlx"`, 1),
		"a short digest":    strings.Replace(good, `"sha256":"abab`, `"sha256":"ab`, 1),
		"an upper digest":   strings.Replace(good, `"sha256":"abab`, `"sha256":"ABAB`, 1),
		"a negative size":   strings.Replace(good, `"size":1234`, `"size":-1`, 1),
		"bad dependencies":  strings.Replace(good, `{"ansible.posix":">=1.0.0"}`, `["x"]`, 1),
	} {
		if _, _, err := galaxydoc.ParseUpstreamVersion([]byte(doc)); !errors.Is(err, galaxydoc.ErrInvalid) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, _, err := galaxydoc.ParseUpstreamVersion(make([]byte, galaxydoc.MaxDocumentBytes+1)); !errors.Is(err, galaxydoc.ErrInvalid) {
		t.Errorf("an oversized document: %v", err)
	}
}

func TestR_ANS_017_AnUpstreamListPageIsParsed(t *testing.T) {
	const page = `{"meta":{"count":250},"links":{"first":"/a","previous":null,"next":"/api/v3/x/versions/?limit=100&offset=100","last":"/z"},
	"data":[{"version":"2.0.0","href":"/h","created_at":"2024-01-01T00:00:00.000000Z","updated_at":"2024-01-02T00:00:00Z","requires_ansible":">=2.14","marks":[]},
	{"version":"1.0.0","requires_ansible":null}]}`
	es, count, next, err := galaxydoc.ParseUpstreamList([]byte(page))
	if err != nil || count != 250 || next != "/api/v3/x/versions/?limit=100&offset=100" || len(es) != 2 {
		t.Fatalf("%+v %d %q %v", es, count, next, err)
	}
	if es[0].Version != "2.0.0" || es[0].RequiresAnsible != ">=2.14" || es[0].CreatedAt.IsZero() || es[1].RequiresAnsible != "" {
		t.Errorf("entries = %+v", es)
	}
	_, _, next, err = galaxydoc.ParseUpstreamList([]byte(`{"meta":{"count":1},"links":{"next":null},"data":[{"version":"1.0.0"}]}`))
	if err != nil || next != "" {
		t.Errorf("last page: %q %v", next, err)
	}
	for name, doc := range map[string]string{
		"not JSON": "{", "a count below the page": `{"meta":{"count":0},"data":[{"version":"1.0.0"}]}`,
		"an entry without a version": `{"meta":{"count":1},"data":[{}]}`, "a negative count": `{"meta":{"count":-1},"data":[]}`,
	} {
		if _, _, _, err := galaxydoc.ParseUpstreamList([]byte(doc)); !errors.Is(err, galaxydoc.ErrInvalid) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

func FuzzR_ANS_031_ParseUpstreamDocuments(f *testing.F) {
	f.Add([]byte(strings.Replace(upstreamVersionDoc, "{{LONG}}", "x", 1)))
	f.Add([]byte(`{"meta":{"count":1},"links":{"next":null},"data":[{"version":"1.0.0"}]}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, doc []byte) {
		if v, _, err := galaxydoc.ParseUpstreamVersion(doc); err == nil {
			out := galaxydoc.RenderVersion(v, addr)
			if !json.Valid(out) {
				t.Fatalf("rendered a document that is not JSON: %s", out)
			}
		}
		if es, count, _, err := galaxydoc.ParseUpstreamList(doc); err == nil {
			if !json.Valid(galaxydoc.RenderVersionList(es, count, 100, 0, addr)) {
				t.Fatal("rendered a list that is not JSON")
			}
		}
	})
}
