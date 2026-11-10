package ansible

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/ansibletest"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

func newHost(t *testing.T) *formattest.Host {
	t.Helper()
	return formattest.NewHost(New(), hostOptions())
}

func get(t *testing.T, h *formattest.Host, path string) *formattest.Response {
	t.Helper()
	path, query, _ := strings.Cut(path, "?")
	r, err := h.Serve(context.Background(), http.MethodGet, path, nil, false, func(r *http.Request) { r.URL.RawQuery = query })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// taskOf reads the task a publish answered (202) and fetches it through the route.
func taskOf(t *testing.T, h *formattest.Host, r *formattest.Response) map[string]any {
	t.Helper()
	if r.Code != http.StatusAccepted {
		t.Fatalf("publish: status %d: %s", r.Code, r.Body.String())
	}
	var body struct{ Task string }
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil || !strings.Contains(body.Task, "/v3/imports/collections/") || !strings.HasSuffix(body.Task, "/") {
		t.Fatalf("answer = %s: %v", r.Body.String(), err)
	}
	tr := get(t, h, "v3/"+body.Task[strings.Index(body.Task, "imports/"):])
	if tr.Code != http.StatusOK {
		t.Fatalf("task: status %d: %s", tr.Code, tr.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(tr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func errorCode(t *testing.T, r *formattest.Response) string {
	t.Helper()
	var b struct{ Errors []struct{ Code string } }
	if err := json.Unmarshal(r.Body.Bytes(), &b); err != nil || len(b.Errors) != 1 {
		t.Fatalf("status %d, body %s", r.Code, r.Body.String())
	}
	return b.Errors[0].Code
}

func TestR_ANS_008_APublishIsAcceptedInEitherOrderOfParts(t *testing.T) {
	for name, shaFirst := range map[string]bool{"sha256 first, as the client sends it": true, "file first, as the documentation shows it": false} {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			r, err := publish(h, collectiontest.Build(collectionSpec("1.0.0")), shaFirst, "")
			if err != nil {
				t.Fatal(err)
			}
			task := taskOf(t, h, r)
			if task["state"] != "completed" || task["namespace"] != "acme" || task["name"] != "tools" || task["version"] != "1.0.0" || task["error"] != nil {
				t.Errorf("task = %v", task)
			}
			if v := get(t, h, "v3/collections/acme/tools/versions/1.0.0/"); v.Code != http.StatusOK {
				t.Errorf("version: status %d", v.Code)
			}
		})
	}
}

// TestR_ANS_008_AFilePartInBase64IsDecodedAsAnsibleCoreSendsIt: ansible-core encodes the archive in the file part as base64 in lines of 76
// characters and says so in Content-Transfer-Encoding (prepare_multipart); the stored archive and its digest are the decoded bytes.
func TestR_ANS_008_AFilePartInBase64IsDecodedAsAnsibleCoreSendsIt(t *testing.T) {
	h := newHost(t)
	archive := collectiontest.Build(collectionSpec("1.0.0"))
	sum := sha256.Sum256(archive)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Type", "application/octet-stream")
	hdr.Set("Content-Transfer-Encoding", "base64")
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="acme-tools-1.0.0.tar.gz"`)
	fw, _ := mw.CreatePart(hdr)
	enc := base64.StdEncoding.EncodeToString(archive)
	for len(enc) > 76 {
		_, _ = io.WriteString(fw, enc[:76]+"\n")
		enc = enc[76:]
	}
	_, _ = io.WriteString(fw, enc+"\n")
	_ = mw.Close()
	r, err := h.Serve(context.Background(), http.MethodPost, "v3/artifacts/collections/", &body, false,
		func(r *http.Request) { r.Header.Set("Content-Type", mw.FormDataContentType()) })
	if err != nil {
		t.Fatal(err)
	}
	if task := taskOf(t, h, r); task["state"] != "completed" {
		t.Errorf("task = %v", task)
	}
	if v := get(t, h, "v3/collections/acme/tools/versions/1.0.0/"); !strings.Contains(v.Body.String(), hex.EncodeToString(sum[:])) {
		t.Errorf("the stored version does not carry the digest of the decoded archive: %s", v.Body.String())
	}
}

func TestR_ANS_008_ARawBodyIsTheArchive(t *testing.T) {
	h := newHost(t)
	archive := collectiontest.Build(collectionSpec("1.0.0"))
	r, err := h.Serve(context.Background(), http.MethodPost, "v3/artifacts/collections/", bytes.NewReader(archive), false,
		func(r *http.Request) { r.Header.Set("Content-Type", "application/octet-stream") })
	if err != nil {
		t.Fatal(err)
	}
	if task := taskOf(t, h, r); task["state"] != "completed" {
		t.Errorf("task = %v", task)
	}
}

func TestR_ANS_008_AWrongDigestStoresNothing(t *testing.T) {
	h := newHost(t)
	r, err := publish(h, collectiontest.Build(collectionSpec("1.0.0")), true, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusBadRequest || errorCode(t, r) != "sha256_mismatch" {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	if c := get(t, h, "v3/collections/acme/tools/"); c.Code != http.StatusNotFound {
		t.Errorf("the collection exists: status %d", c.Code)
	}
}

func TestR_ANS_008_ABodyWithoutTheFilePartOrWithTheFileTwiceIsMalformed(t *testing.T) {
	h := newHost(t)
	for name, body := range map[string]string{
		"no file":    "--b\r\nContent-Disposition: form-data; name=\"sha256\"\r\n\r\n" + strings.Repeat("a", 64) + "\r\n--b--\r\n",
		"two files":  "--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nx\r\n--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\ny\r\n--b--\r\n",
		"a bad hash": "--b\r\nContent-Disposition: form-data; name=\"sha256\"\r\n\r\nnothex\r\n--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\ny\r\n--b--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r, err := h.Serve(context.Background(), http.MethodPost, "v3/artifacts/collections/", strings.NewReader(body), false,
				func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=b") })
			if err != nil {
				t.Fatal(err)
			}
			if r.Code != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
		})
	}
}

func TestR_ANS_008_ARepeatIsIdempotentAndOtherBytesAreRefused(t *testing.T) {
	h := newHost(t)
	archive := collectiontest.Build(collectionSpec("1.0.0"))
	first, err := publish(h, archive, true, "")
	if err != nil {
		t.Fatal(err)
	}
	t1 := taskOf(t, h, first)
	again, err := publish(h, archive, true, "")
	if err != nil {
		t.Fatal(err)
	}
	t2 := taskOf(t, h, again)
	if t2["state"] != "completed" || t2["id"] == t1["id"] || t2["version"] != "1.0.0" {
		t.Errorf("the repeat's task = %v, the first's %v", t2, t1)
	}
	if v := get(t, h, "v3/collections/acme/tools/versions/"); !strings.Contains(v.Body.String(), `"count":1`) {
		t.Errorf("the repeat stored a second version: %s", v.Body.String())
	}
	other := collectionSpec("1.0.0")
	other.Files["README.md"] = "other bytes"
	r, err := publish(h, collectiontest.Build(other), true, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusConflict || errorCode(t, r) != "version_exists" {
		t.Fatalf("other bytes: status %d: %s", r.Code, r.Body.String())
	}
	// A version that differs only in build metadata is the same version.
	meta := collectionSpec("1.0.0+build.5")
	r, err = publish(h, collectiontest.Build(meta), true, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusConflict || errorCode(t, r) != "version_exists" {
		t.Fatalf("build metadata: status %d: %s", r.Code, r.Body.String())
	}
}

func TestR_ANS_009_AnArchiveThatIsNoCollectionIsAFailedTask(t *testing.T) {
	for name, tc := range map[string]struct {
		archive []byte
		code    string
	}{
		"not a gzip tarball": {[]byte("this is not an archive"), "invalid_archive"},
		"an upper-case name": {collectiontest.Build(func() collectiontest.Spec { s := collectionSpec("1.0.0"); s.Name = "Tools"; return s }()), "invalid_name"},
		"a version 1.0":      {collectiontest.Build(collectionSpec("1.0")), "invalid_version"},
		"an unlisted member": {collectiontest.Pack(append(collectiontest.Entries(collectionSpec("1.0.0")), collectiontest.Entry{Name: "extra", Body: []byte("x")})), "invalid_archive"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			r, err := publish(h, tc.archive, true, "")
			if err != nil {
				t.Fatal(err)
			}
			task := taskOf(t, h, r)
			e, _ := task["error"].(map[string]any)
			if task["state"] != "failed" || e == nil || e["code"] != tc.code || e["description"] == "" {
				t.Errorf("task = %v", task)
			}
			if c := get(t, h, "v3/collections/acme/tools/"); c.Code != http.StatusNotFound {
				t.Errorf("a failed publish stored a collection: status %d", c.Code)
			}
			st := h.FormatData(ID).(*ansibletest.Store)
			if len(st.Published) != 0 {
				t.Errorf("a failed publish wrote package.published")
			}
		})
	}
}

// TestR_ANS_012_ADeletedVersionIsTombstonedUntilTheTombstoneIsCleared: the removal of the archive removes the version from the documents,
// leaves a tombstone keyed by the number, and a publish of that number is 409 version_tombstoned.
func TestR_ANS_012_ADeletedVersionIsTombstonedUntilTheTombstoneIsCleared(t *testing.T) {
	h := newHost(t)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if err := publishVersion(v)(h); err != nil {
			t.Fatal(err)
		}
	}
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/collections/acme/tools/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Remove(context.Background(), rq, "delete", ArtifactPath("acme", "tools", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	list := get(t, h, "v3/collections/acme/tools/versions/")
	if strings.Contains(list.Body.String(), `"version":"1.0.0"`) || !strings.Contains(list.Body.String(), `"version":"1.1.0"`) {
		t.Errorf("list after the deletion: %s", list.Body.String())
	}
	for _, v := range []string{"1.0.0", "1.0.0+rebuilt"} {
		r, err := publish(h, collectiontest.Build(collectionSpec(v)), true, "")
		if err != nil {
			t.Fatal(err)
		}
		if r.Code != http.StatusConflict || errorCode(t, r) != "version_tombstoned" {
			t.Fatalf("publish %s: status %d: %s", v, r.Code, r.Body.String())
		}
	}
	st := h.FormatData(ID).(*ansibletest.Store)
	if found, err := st.ClearTombstone(context.Background(), h.Ref().ID, "acme.tools", "1.0.0", rowsAudit()); err != nil || !found {
		t.Fatalf("ClearTombstone = %v, %v", found, err)
	}
	other := collectionSpec("1.0.0")
	other.Files["README.md"] = "other bytes after the clearing"
	if r, err := publish(h, collectiontest.Build(other), true, ""); err != nil || r.Code != http.StatusAccepted {
		t.Fatalf("publish after the clearing: %v %v", r, err)
	}
}

func TestR_ANS_004_TheDocumentsOfAPublishedCollection(t *testing.T) {
	h := newHost(t)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0-rc.1", "2.0.0"} {
		if err := publishVersion(v)(h); err != nil {
			t.Fatal(err)
		}
	}
	var list struct {
		Meta  struct{ Count int }
		Links map[string]*string
		Data  []struct{ Version, Href string }
	}
	r := get(t, h, "v3/collections/acme/tools/versions/?limit=3")
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Meta.Count != 4 || len(list.Data) != 3 || list.Data[0].Version != "2.0.0" || list.Data[1].Version != "2.0.0-rc.1" || list.Data[2].Version != "1.1.0" {
		t.Fatalf("list = %+v", list)
	}
	next := list.Links["next"]
	if next == nil || !strings.HasPrefix(*next, formattest.DefaultBaseURL+"/api/ansible/") || !strings.Contains(*next, "limit=3&offset=3") {
		t.Fatalf("links = %+v", list.Links)
	}
	r = get(t, h, "v3/collections/acme/tools/versions/?limit=3&offset=3")
	list.Data = nil
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].Version != "1.0.0" || list.Links["next"] != nil {
		t.Fatalf("second page = %s (%v)", r.Body.String(), err)
	}
	if r = get(t, h, "v3/collections/acme/tools/versions/?limit=x"); r.Code != http.StatusBadRequest {
		t.Errorf("limit=x: status %d", r.Code)
	}
	if r = get(t, h, "v3/collections/acme/tools/versions/?offset=500"); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"data":[]`) {
		t.Errorf("offset beyond the count: %d %s", r.Code, r.Body.String())
	}
	var col struct {
		Highest struct{ Version string } `json:"highest_version"`
	}
	r = get(t, h, "v3/collections/Acme/Tools/")
	if err := json.Unmarshal(r.Body.Bytes(), &col); err != nil || col.Highest.Version != "2.0.0" {
		t.Errorf("collection = %s (%v): the newest by semver, not by publish time", r.Body.String(), err)
	}
	var ver struct {
		DownloadURL string `json:"download_url"`
		Artifact    struct{ Sha256 string }
		Metadata    struct {
			Dependencies map[string]string
			Readme       string
		}
		RequiresAnsible string `json:"requires_ansible"`
		Signatures      []any
	}
	r = get(t, h, "v3/collections/acme/tools/versions/1.0.0/")
	if err := json.Unmarshal(r.Body.Bytes(), &ver); err != nil {
		t.Fatal(err)
	}
	if ver.DownloadURL != formattest.DefaultBaseURL+"/api/ansible/it/main/v3/artifacts/collections/acme-tools-1.0.0.tar.gz" || ver.RequiresAnsible != ">=2.14.0" ||
		ver.Metadata.Dependencies["community.general"] != ">=1.0.0" || ver.Metadata.Readme != "README.md" || len(ver.Artifact.Sha256) != 64 || len(ver.Signatures) != 0 {
		t.Errorf("version = %+v", ver)
	}
	// The archive is the stored one, with the digest of the document.
	dl := get(t, h, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz")
	if dl.Code != http.StatusOK {
		t.Fatalf("download: status %d", dl.Code)
	}
	if !bytes.Equal(dl.Body.Bytes(), collectiontest.Build(collectionSpec("1.0.0"))) {
		t.Errorf("the download differs from the published archive")
	}
	if got := dl.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("Content-Type = %q", got)
	}
	for _, p := range []string{"v3/collections/acme/tools/versions/9.9.9/", "v3/collections/acme/tools/versions/not-a-version/", "v3/artifacts/collections/acme-tools-9.9.9.tar.gz", "v3/artifacts/collections/nope.tar.gz", "v3/collections/_x/y/"} {
		if r = get(t, h, p); r.Code != http.StatusNotFound || errorCode(t, r) != "not_found" {
			t.Errorf("GET %s = %d %s", p, r.Code, r.Body.String())
		}
	}
}

func TestR_ANS_004_ADocumentCarriesAWeakValidatorAndAnswers304(t *testing.T) {
	h := newHost(t)
	if err := publishVersion("1.0.0")(h); err != nil {
		t.Fatal(err)
	}
	r := get(t, h, "v3/collections/acme/tools/")
	etag := r.Header().Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("ETag = %q", etag)
	}
	cond, err := h.Serve(context.Background(), http.MethodGet, "v3/collections/acme/tools/", nil, false, func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
	if err != nil || cond.Code != http.StatusNotModified {
		t.Fatalf("conditional: %v %v", cond, err)
	}
	if err := publishVersion("1.1.0")(h); err != nil {
		t.Fatal(err)
	}
	if r2 := get(t, h, "v3/collections/acme/tools/"); r2.Header().Get("ETag") == etag {
		t.Error("a publish left the validator of the collection document unchanged")
	}
}

func TestR_ANS_003_TheRootListsV3Only(t *testing.T) {
	h := newHost(t)
	r := get(t, h, "")
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"v3":"v3/"`) || strings.Contains(r.Body.String(), "v1") {
		t.Errorf("root = %d %s", r.Code, r.Body.String())
	}
}
