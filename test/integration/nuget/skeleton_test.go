//go:build integration

package nuget

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_001_ARepositoryCanBeCreatedAndItsServiceIndexAnswers: the format is registered, a
// NuGet repository is created through the management API with its settings validated against
// the schema the format registers, and the service index of the repository answers at its three
// spellings (R-NUGET-001, R-NUGET-002, R-ARCH-008).
func TestR_NUGET_001_ARepositoryCanBeCreatedAndItsServiceIndexAnswers(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	post := func(body map[string]any) testenv.Answer {
		t.Helper()
		return e.st.Do(a, "POST", "/api/v1/repositories", e.tokAdmin, body)
	}

	t.Run("R-NUGET-001 the catalog lists nuget with its default upstream and periods", func(t *testing.T) {
		ans := e.st.Do(a, "GET", "/api/v1/formats", e.tokAdmin, nil)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		var list struct {
			Formats []struct {
				Name                      string          `json:"name"`
				Classes                   []string        `json:"classes"`
				ProtocolBasePath          string          `json:"protocolBasePath"`
				DefaultUpstreamURL        string          `json:"defaultUpstreamUrl"`
				DefaultMetadataTTLSeconds *int            `json:"defaultMetadataTtlSeconds"`
				DefaultNotFoundTTLSeconds *int            `json:"defaultNotFoundTtlSeconds"`
				SettingsSchema            json.RawMessage `json:"settingsSchema"`
			} `json:"formats"`
		}
		if err := json.Unmarshal(ans.Body, &list); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range list.Formats {
			if f.Name != "nuget" {
				continue
			}
			found = true
			if f.DefaultUpstreamURL != "https://api.nuget.org/v3/index.json" || f.ProtocolBasePath != "/api/nuget/{key}/" {
				t.Errorf("upstream %q, base path %q", f.DefaultUpstreamURL, f.ProtocolBasePath)
			}
			if f.DefaultMetadataTTLSeconds == nil || *f.DefaultMetadataTTLSeconds != 300 || f.DefaultNotFoundTTLSeconds == nil || *f.DefaultNotFoundTTLSeconds != 1800 {
				t.Errorf("periods %v and %v, want 300 and 1800 (Q-NUGET-2)", f.DefaultMetadataTTLSeconds, f.DefaultNotFoundTTLSeconds)
			}
			if !slices.Contains(f.Classes, "local") || !slices.Contains(f.Classes, "remote") || !slices.Contains(f.Classes, "virtual") {
				t.Errorf("classes = %v", f.Classes)
			}
			for _, want := range []string{`"deleteMode"`, `"forwardSearch"`, `"additionalProperties":false`} {
				if !strings.Contains(string(f.SettingsSchema), want) {
					t.Errorf("the schema lacks %s: %s", want, f.SettingsSchema)
				}
			}
		}
		if !found {
			t.Fatalf("GET /api/v1/formats does not list nuget: %s", ans.Body)
		}
	})

	t.Run("R-NUGET-007 a create with NuGet settings is stored and read back", func(t *testing.T) {
		ans := post(map[string]any{"key": "nuget-created", "class": "local", "format": "nuget", "nuget": map[string]any{"deleteMode": "delete"}})
		if ans.Status != 201 {
			t.Fatalf("create %d %s", ans.Status, ans.Body)
		}
		read := e.st.Do(a, "GET", "/api/v1/repositories/nuget-created", e.tokAdmin, nil)
		doc := read.JSON(t)
		if got, _ := json.Marshal(doc["nuget"]); string(got) != `{"deleteMode":"delete"}` {
			t.Fatalf("read back nuget = %s (%s)", got, read.Body)
		}
		if doc["layout"] != "nuget" {
			t.Errorf("layout = %v", doc["layout"])
		}
		if stored := e.st.Text(`SELECT config #>> '{nuget,deleteMode}' FROM repositories WHERE key = 'nuget-created'`); stored != "delete" {
			t.Fatalf("stored deleteMode = %q", stored)
		}
	})

	t.Run("R-ARCH-008 settings the schema refuses answer 422 with the pointer of the field", func(t *testing.T) {
		bad := post(map[string]any{"key": "nuget-bad", "class": "local", "format": "nuget", "nuget": map[string]any{"deleteMode": "purge"}})
		if bad.Status != 422 || problemFields(t, bad)["/nuget/deleteMode"] != "invalid_value" {
			t.Fatalf("%d %s", bad.Status, bad.Body)
		}
		other := post(map[string]any{"key": "nuget-bad", "class": "local", "format": "nuget", "nuget": map[string]any{"forwardSearch": true}})
		if other.Status != 422 || problemFields(t, other)["/nuget/forwardSearch"] != "not_applicable" {
			t.Fatalf("%d %s", other.Status, other.Body)
		}
		wrong := post(map[string]any{"key": "gen-bad", "class": "local", "format": "generic", "nuget": map[string]any{"deleteMode": "delete"}})
		if wrong.Status != 422 || problemFields(t, wrong)["/nuget"] != "not_applicable" {
			t.Fatalf("%d %s", wrong.Status, wrong.Body)
		}
	})

	t.Run("R-NUGET-001 the service index answers at its three spellings with one body", func(t *testing.T) {
		first := e.get(a, "nuget-created", "v3/index.json", e.tokAdmin)
		if first.Status != 200 {
			t.Fatalf("%d %s", first.Status, first.Body)
		}
		for _, p := range []string{"v3/", "v3"} {
			ans := e.get(a, "nuget-created", p, e.tokAdmin)
			if ans.Status != 200 || !bytes.Equal(ans.Body, first.Body) || ans.Header.Get("ETag") != first.Header.Get("ETag") {
				t.Errorf("GET %q: %d, same body %v", p, ans.Status, bytes.Equal(ans.Body, first.Body))
			}
		}
		if ct := first.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if !strings.HasPrefix(first.Header.Get("ETag"), `W/"`) {
			t.Errorf("ETag = %q", first.Header.Get("ETag"))
		}
		var doc struct {
			Version   string `json:"version"`
			Resources []struct {
				ID   string `json:"@id"`
				Type string `json:"@type"`
			} `json:"resources"`
		}
		if err := json.Unmarshal(first.Body, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Version != "3.0.0" || len(doc.Resources) != 12 {
			t.Errorf("version %q with %d resources", doc.Version, len(doc.Resources))
		}
		types := map[string]bool{}
		for _, r := range doc.Resources {
			types[r.Type] = true
			if !strings.HasPrefix(r.ID, feedBase+"/api/nuget/nuget-created/v3/") {
				t.Errorf("@id %q is not below the repository's own feed", r.ID)
			}
		}
		for _, want := range []string{"PackageBaseAddress/3.0.0", "PackagePublish/2.0.0", "SymbolPackagePublish/4.9.0", "RegistrationsBaseUrl/3.6.0", "SearchQueryService/3.5.0"} {
			if !types[want] {
				t.Errorf("the service index lacks %s", want)
			}
		}
	})

	t.Run("R-NUGET-002 a conditional request with the ETag answers 304", func(t *testing.T) {
		etag := e.get(a, "nuget-created", "v3/index.json", e.tokAdmin).Header.Get("ETag")
		ans := e.get(a, "nuget-created", "v3/index.json", e.tokAdmin, "If-None-Match", etag)
		if ans.Status != 304 || len(ans.Body) != 0 {
			t.Fatalf("%d with %d bytes", ans.Status, len(ans.Body))
		}
		head := e.st.Do(a, "HEAD", "/api/nuget/nuget-created/v3/index.json", e.tokAdmin, nil)
		if head.Status != 200 || len(head.Body) != 0 || head.Header.Get("ETag") != etag {
			t.Fatalf("HEAD %d, %d bytes, ETag %q", head.Status, len(head.Body), head.Header.Get("ETag"))
		}
	})
}

// TestR_NUGET_012_TheServiceIndexChallengesAndAuthenticates: an anonymous request is challenged
// with exactly Basic realm="trove" in the native error shape, a trove token works as the Basic
// password with any user name and as a Bearer token, and a revoked or unknown token is a 401.
func TestR_NUGET_012_TheServiceIndexChallengesAndAuthenticates(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	t.Run("R-NUGET-012 an anonymous request is challenged, never answered 404", func(t *testing.T) {
		ans := e.get(a, "nuget-local", "v3/index.json", "")
		if ans.Status != 401 || ans.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d, WWW-Authenticate %q", ans.Status, ans.Header.Get("WWW-Authenticate"))
		}
		line(t, ans)
	})
	t.Run("R-NUGET-012 a token as the Basic password with any user name", func(t *testing.T) {
		basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("anything:"+e.tokReader))
		ans := e.st.Do(a, "GET", "/api/nuget/nuget-local/v3/index.json", "", nil, "Authorization", basic)
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NUGET-012 a token as a Bearer token", func(t *testing.T) {
		if ans := e.get(a, "nuget-local", "v3/index.json", e.tokReader); ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
	})
	t.Run("R-NUGET-012 an unknown token is a 401 with the challenge", func(t *testing.T) {
		ans := e.get(a, "nuget-local", "v3/index.json", "trv_pat_"+strings.Repeat("A", 43))
		if ans.Status != 401 || ans.Header.Get("WWW-Authenticate") != `Basic realm="trove"` {
			t.Fatalf("%d, WWW-Authenticate %q", ans.Status, ans.Header.Get("WWW-Authenticate"))
		}
	})
	t.Run("R-NUGET-013 a person without a grant does not reach the index or learn what is behind it", func(t *testing.T) {
		for _, path := range []string{"v3/index.json", "v3/flatcontainer/acme.widgets/index.json"} {
			ans := e.get(a, "nuget-local", path, e.tokNobody)
			if ans.Status != 403 && ans.Status != 404 {
				t.Fatalf("%s: %d %s", path, ans.Status, ans.Body)
			}
			line(t, ans)
		}
	})
}

// TestR_NUGET_001_PathsThatAreNoV3ResourceAnswer404: every NuGet v2 request shape is a 404 with
// the code nuget_v2_unsupported and a message that names the absolute URL of the service index
// of the repository the client addressed; an unknown key and a repository of another format are
// 404 as well, in the native shape (R-NUGET-001).
func TestR_NUGET_001_PathsThatAreNoV3ResourceAnswer404(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	index := feedBase + "/api/nuget/nuget-local/v3/index.json"
	for _, path := range []string{
		"FindPackagesById()?id='Acme.Widgets'", "$metadata", "Packages()", "Search()?searchTerm='acme'", "api/v2/package", "", "v3/unknown",
	} {
		t.Run("R-NUGET-001 v2 "+path, func(t *testing.T) {
			ans := e.get(a, "nuget-local", path, e.tokReader)
			if ans.Status != 404 || line(t, ans) != "nuget_v2_unsupported" || !strings.Contains(string(ans.Body), index) {
				t.Fatalf("%d %s", ans.Status, ans.Body)
			}
		})
	}
	t.Run("R-NUGET-001 an unknown repository key is 404", func(t *testing.T) {
		ans := e.get(a, "does-not-exist", "v3/index.json", e.tokAdmin)
		if ans.Status != 404 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		line(t, ans)
	})
	t.Run("R-NUGET-001 a repository of another format is 404 and says so", func(t *testing.T) {
		ans := e.get(a, "gen-local", "v3/index.json", e.tokReader)
		if ans.Status != 404 || !strings.Contains(string(ans.Body), "not a NuGet repository") {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		line(t, ans)
	})
	t.Run("R-NUGET-001 a method the resource does not take answers 405 with Allow", func(t *testing.T) {
		ans := e.st.Do(a, "DELETE", "/api/nuget/nuget-local/v3/index.json", e.tokAdmin, nil)
		if ans.Status != 405 || ans.Header.Get("Allow") != "GET, HEAD" {
			t.Fatalf("%d, Allow %q", ans.Status, ans.Header.Get("Allow"))
		}
		line(t, ans)
	})
}

// TestR_NUGET_001_OperationsThatAreNotBuiltAnswer501: a route of the table whose handler is
// written by a later work package answers 501 nuget_not_implemented to a caller who may read,
// so the format registers whole and a client reads a failure of the server, not a missing
// package (WP3.1c).
func TestR_NUGET_001_OperationsThatAreNotBuiltAnswer501(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, path := range []string{
		"v3/flatcontainer/acme.widgets/1.2.3/readme",
	} {
		t.Run("R-NUGET-001 "+path, func(t *testing.T) {
			ans := e.get(a, "nuget-local", path, e.tokReader)
			if ans.Status != 501 || line(t, ans) != "nuget_not_implemented" {
				t.Fatalf("%d %s", ans.Status, ans.Body)
			}
		})
	}
}

// TestR_NUGET_013_RawDeployIntoANuGetRepositoryAnswers409: /files/ takes nothing into a
// repository whose format has a protocol of its own, so a raw .nupkg is refused before its body
// is read, whatever the path (R-NUGET-005, Q-NUGET-13).
func TestR_NUGET_013_RawDeployIntoANuGetRepositoryAnswers409(t *testing.T) {
	e := newEnv(t)
	a := e.replica("web-a")
	for _, path := range []string{"acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", "notes.txt", "acme.widgets/9.9.9/acme.widgets.9.9.9.nupkg"} {
		ans := e.st.Do(a, "PUT", "/files/nuget-local/"+path, e.tokAdmin, []byte("not a package"))
		if ans.Status != 409 || !strings.Contains(string(ans.Body), "/api/nuget/nuget-local/") {
			t.Errorf("PUT /files/nuget-local/%s: %d %s", path, ans.Status, ans.Body)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = 'nuget-local'`); n != 0 {
		t.Errorf("%d items were stored", n)
	}
}
