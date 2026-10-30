package rawfiles

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
)

func TestDirectoryIndex(t *testing.T) {
	entries := []items.Child{
		{Path: "<script>.txt", Kind: items.KindFile},
		{Path: "javascript:alert(1)", Kind: items.KindFile},
		{Path: "a b&c", Kind: items.KindFile},
		{Path: "sub", Kind: items.KindFolder},
	}
	r := httptest.NewRequest("GET", "/files/r/tools/", nil)
	page := renderHTML("r", "tools", entries, "", r)

	t.Run("R-STOR-017 every name is HTML-escaped", func(t *testing.T) {
		if strings.Contains(page, "<script>") || !strings.Contains(page, "&lt;script&gt;.txt") {
			t.Fatalf("the name is not escaped:\n%s", page)
		}
	})
	t.Run("R-STOR-017 every link is ./ followed by the percent-encoded name", func(t *testing.T) {
		for _, want := range []string{
			`href="./%3Cscript%3E.txt"`, `href="./javascript%3Aalert(1)"`, `href="./a%20b&amp;c"`, `href="./sub/"`, `href="../"`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("missing %s in\n%s", want, page)
			}
		}
		if strings.Contains(page, `href="javascript:`) {
			t.Error("a link has a scheme")
		}
	})
	t.Run("R-STOR-017 the next page is linked with the cursor", func(t *testing.T) {
		if got := renderHTML("r", "", nil, "abc", r); !strings.Contains(got, `rel="next" href="?cursor=abc"`) {
			t.Errorf("%s", got)
		}
	})
	t.Run("R-STOR-017 text/html is chosen by the Accept header, JSON otherwise", func(t *testing.T) {
		for accept, want := range map[string]bool{
			"": false, "*/*": false, "application/json": false, "text/html": true,
			"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": true,
			"application/json, text/html;q=0.5":                               false,
			"text/html;q=0.5, application/json":                               false,
			"text/html;q=0":                                                   false,
		} {
			if got := wantsHTML(accept); got != want {
				t.Errorf("wantsHTML(%q) = %v, want %v", accept, got, want)
			}
		}
	})
}

func TestSplitPath(t *testing.T) {
	for _, tc := range []struct {
		in        string
		key, rest string
		ok        bool
	}{
		{"/files/r/a/b.bin", "r", "a/b.bin", true},
		{"/files/r/", "r", "", true},
		{"/files/r/a%2Fb/", "r", "a%2Fb/", true},
		{"/files/my%2Drepo/x", "my-repo", "x", true},
		{"/files//x", "", "", false},
		{"/files/", "", "", false},
		{"/other/r/x", "", "", false},
	} {
		key, rest, ok := splitPath(tc.in)
		if key != tc.key || rest != tc.rest || ok != tc.ok {
			t.Errorf("splitPath(%q) = %q, %q, %v", tc.in, key, rest, ok)
		}
	}
}

func router() *httpserver.Router {
	r := httpserver.NewClientRouter()
	New(Options{}).Register(r)
	return r
}

func TestInvalidPathsAreRefusedBeforeAnything_else(t *testing.T) {
	// The handler has no service: the answer 400 shows that the path is checked
	// before authorization and before any lookup, and that the paths ServeMux would
	// redirect reach the handler.
	for _, path := range []string{
		"/files/r/a/%2e%2e/x", "/files/r/a//b", "/files/r/a%00b", "/files/r/a%5Cb", "/files/r/a/../x", "/files/r/a/./x",
		// An empty first segment right after the key (security finding: it was served as /files/r/x).
		"/files/r//x", "/files/r//",
	} {
		t.Run("R-GENERIC-002 "+path, func(t *testing.T) {
			for _, method := range []string{"GET", "PUT", "DELETE"} {
				req := httptest.NewRequest(method, "/", nil)
				req.URL.Path, req.URL.RawPath = path, ""
				if strings.Contains(path, "%") {
					req.URL.Path, req.URL.RawPath = unescapeForTest(path), path
				}
				rec := httptest.NewRecorder()
				router().ServeHTTP(rec, req)
				if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_path"`) {
					t.Errorf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
				}
			}
		})
	}
	t.Run("R-GENERIC-002 a clean path reaches the service, which is not there", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router().ServeHTTP(rec, httptest.NewRequest("GET", "/files/r/a/b.bin", nil))
		if rec.Code != 503 {
			t.Errorf("status %d", rec.Code)
		}
	})
}

func unescapeForTest(p string) string {
	r := strings.NewReplacer("%2e", ".", "%00", "\x00", "%5C", `\`)
	return r.Replace(p)
}

func TestRoutesAgreeWithTheDocument(t *testing.T) {
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-API-002 the routes of /files/ and the operations of the document agree in both directions", func(t *testing.T) {
		patterns := router().Patterns()
		for p := range doc.Operations() { // the management API is registered elsewhere
			patterns = append(patterns, p)
		}
		if err := doc.CheckRoutes(patterns); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-API-002 a files route that the document lacks is named", func(t *testing.T) {
		r := router()
		r.HandleFunc("POST /files/{repoKey}/{path...}", func(http.ResponseWriter, *http.Request) {})
		patterns := r.Patterns()
		for p := range doc.Operations() {
			patterns = append(patterns, p)
		}
		if err := doc.CheckRoutes(patterns); err == nil || !strings.Contains(err.Error(), "POST /files/{repoKey}/{path...}") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-API-025 the operations of the files family declare the repository action on the path", func(t *testing.T) {
		want := map[string]string{"GET": "read", "HEAD": "read", "PUT": "deploy", "DELETE": "delete"}
		got := doc.FileOperations()
		if len(got) != len(want) {
			t.Fatalf("operations = %v", got)
		}
		for pattern, op := range got {
			method, _, _ := strings.Cut(pattern, " ")
			if string(op.Requirement.Action) != want[method] || op.Requirement.Repository != "path.repoKey" || op.Requirement.Path != "path.path" {
				t.Errorf("%s: %+v", pattern, op.Requirement)
			}
		}
	})
}
