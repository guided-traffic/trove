package rawfiles

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/files"
	"github.com/guided-traffic/trove/internal/httpserver"
)

const etag = `"5be0c1d2e8f3a4b5c6d7e8f90123456789abcdef0123456789abcdef01234567"`

var modified = time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC)

func request(method string, hdr ...string) *http.Request {
	r := httptest.NewRequest(method, "/files/r/a.bin", nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

func TestByteRange(t *testing.T) {
	const size = 1000
	for _, tc := range []struct {
		name          string
		hdr           []string
		method        string
		start, length int64
		want          rangeResult
	}{
		{"R-STOR-012 the first 100 bytes", []string{"Range", "bytes=0-99"}, "GET", 0, 100, partialContent},
		{"R-STOR-012 an open range", []string{"Range", "bytes=900-"}, "GET", 900, 100, partialContent},
		{"R-STOR-012 a suffix range", []string{"Range", "bytes=-100"}, "GET", 900, 100, partialContent},
		{"R-STOR-012 a suffix longer than the file is the file", []string{"Range", "bytes=-5000"}, "GET", 0, 1000, partialContent},
		{"R-STOR-012 an end beyond the file is cut to it", []string{"Range", "bytes=990-5000"}, "GET", 990, 10, partialContent},
		{"R-STOR-012 a start beyond the file is unsatisfiable", []string{"Range", "bytes=5000000000-"}, "GET", 0, 0, unsatisfiable},
		{"R-STOR-012 a suffix of zero is unsatisfiable", []string{"Range", "bytes=-0"}, "GET", 0, 0, unsatisfiable},
		{"R-STOR-012 several ranges are answered with the whole content", []string{"Range", "bytes=0-9,20-29"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 an unknown unit is ignored", []string{"Range", "items=0-9"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 a malformed range is ignored", []string{"Range", "bytes=9-0"}, "GET", 0, 0, fullContent},
		{"R-STOR-012 HEAD ignores Range", []string{"Range", "bytes=0-9"}, "HEAD", 0, 0, fullContent},
		{"R-STOR-012 If-Range with the ETag lets the range apply", []string{"Range", "bytes=100-", "If-Range", etag}, "GET", 100, 900, partialContent},
		{"R-STOR-012 If-Range with another ETag answers the whole content", []string{"Range", "bytes=100-", "If-Range", `"other"`}, "GET", 0, 0, fullContent},
		{"R-STOR-012 If-Range needs a strong tag", []string{"Range", "bytes=100-", "If-Range", "W/" + etag}, "GET", 0, 0, fullContent},
		{"R-STOR-012 If-Range with the date of the file lets the range apply", []string{"Range", "bytes=0-9", "If-Range", modified.Format(http.TimeFormat)}, "GET", 0, 10, partialContent},
		{"R-STOR-012 If-Range with another date answers the whole content", []string{"Range", "bytes=0-9", "If-Range", modified.Add(time.Hour).Format(http.TimeFormat)}, "GET", 0, 0, fullContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, length, got := byteRange(request(tc.method, tc.hdr...), etag, modified, size)
			if got != tc.want || (got == partialContent && (start != tc.start || length != tc.length)) {
				t.Fatalf("byteRange = %d, %d, %d; want %d, %d, %d", start, length, got, tc.start, tc.length, tc.want)
			}
		})
	}
	t.Run("R-STOR-012 any range of an empty file is unsatisfiable", func(t *testing.T) {
		if _, _, got := byteRange(request("GET", "Range", "bytes=0-0"), etag, modified, 0); got != unsatisfiable {
			t.Fatalf("got %d", got)
		}
	})
}

func TestEvaluate(t *testing.T) {
	later := modified.Add(time.Hour).Format(http.TimeFormat)
	earlier := modified.Add(-time.Hour).Format(http.TimeFormat)
	for _, tc := range []struct {
		name   string
		method string
		hdr    []string
		want   precondition
	}{
		{"R-STOR-012 no condition proceeds", "GET", nil, proceed},
		{"R-STOR-012 If-None-Match with the ETag is 304", "GET", []string{"If-None-Match", etag}, notModified},
		{"R-STOR-012 If-None-Match compares weakly", "GET", []string{"If-None-Match", "W/" + etag}, notModified},
		{"R-STOR-012 If-None-Match with a list", "GET", []string{"If-None-Match", `"x", ` + etag}, notModified},
		{"R-STOR-012 If-None-Match with another ETag proceeds", "GET", []string{"If-None-Match", `"x"`}, proceed},
		{"R-STOR-012 If-None-Match star is 304 for an existing file", "HEAD", []string{"If-None-Match", "*"}, notModified},
		{"R-API-012 If-None-Match star on a PUT fails the precondition", "PUT", []string{"If-None-Match", "*"}, preconditionFailed},
		{"R-STOR-012 If-Modified-Since at the modification time is 304", "GET", []string{"If-Modified-Since", modified.Format(http.TimeFormat)}, notModified},
		{"R-STOR-012 If-Modified-Since before it proceeds", "GET", []string{"If-Modified-Since", earlier}, proceed},
		{"R-STOR-012 If-None-Match wins over If-Modified-Since", "GET", []string{"If-None-Match", `"x"`, "If-Modified-Since", later}, proceed},
		{"RFC 9110 If-Match with another ETag is 412", "GET", []string{"If-Match", `"x"`}, preconditionFailed},
		{"RFC 9110 If-Match with the ETag proceeds", "GET", []string{"If-Match", etag}, proceed},
		{"RFC 9110 If-Match refuses a weak tag", "GET", []string{"If-Match", "W/" + etag}, preconditionFailed},
		{"RFC 9110 If-Unmodified-Since before the modification is 412", "GET", []string{"If-Unmodified-Since", earlier}, preconditionFailed},
		{"RFC 9110 If-Unmodified-Since after it proceeds", "GET", []string{"If-Unmodified-Since", later}, proceed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := evaluate(request(tc.method, tc.hdr...), etag, modified); got != tc.want {
				t.Fatalf("evaluate = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReprDigest(t *testing.T) {
	sha256 := "sha-256=:AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=:"
	sha512 := "sha-512=:" + strings.Repeat("AAAA", 21) + "AA==:"
	t.Run("R-STOR-007 sha-256 and sha-512 are read", func(t *testing.T) {
		h := http.Header{"Repr-Digest": {sha256 + ", " + sha512}}
		got, err := parseReprDigest(h)
		if err != nil || len(got.SHA256) != 32 || len(got.SHA512) != 64 {
			t.Fatalf("%+v, %v", got, err)
		}
	})
	t.Run("R-STOR-007 other algorithms are not verified", func(t *testing.T) {
		got, err := parseReprDigest(http.Header{"Repr-Digest": {"md5=:AAAAAAAAAAAAAAAAAAAAAA==:, " + sha256}})
		if err != nil || len(got.MD5) != 0 || len(got.SHA256) != 32 {
			t.Fatalf("%+v, %v", got, err)
		}
	})
	t.Run("R-STOR-007 a value that is not a dictionary of byte sequences is 400 invalid_header", func(t *testing.T) {
		for _, bad := range []string{"sha-256=abc", "sha-256", "sha-256=:!!!:", "sha-256=:AAAA:", "sha-512=:AAAA:"} {
			if _, err := parseReprDigest(http.Header{"Repr-Digest": {bad}}); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	})
	t.Run("RFC 9530 the response digest adds sha-512 on request", func(t *testing.T) {
		sum, sum512 := make([]byte, 32), make([]byte, 64)
		if got := reprDigest(http.Header{}, sum, sum512); strings.Contains(got, "sha-512") {
			t.Errorf("without Want-Repr-Digest: %q", got)
		}
		got := reprDigest(http.Header{"Want-Repr-Digest": {"sha-512=10"}}, sum, sum512)
		if !strings.HasPrefix(got, "sha-256=:") || !strings.Contains(got, ", sha-512=:") {
			t.Errorf("with Want-Repr-Digest: %q", got)
		}
		if got := reprDigest(http.Header{"Want-Repr-Digest": {"sha-512=0"}}, sum, sum512); strings.Contains(got, "sha-512") {
			t.Errorf("weight 0: %q", got)
		}
	})
}

func TestDirectoryIndex(t *testing.T) {
	items := []files.Child{
		{Path: "<script>.txt", Kind: files.KindFile},
		{Path: "javascript:alert(1)", Kind: files.KindFile},
		{Path: "a b&c", Kind: files.KindFile},
		{Path: "sub", Kind: files.KindFolder},
	}
	r := httptest.NewRequest("GET", "/files/r/tools/", nil)
	page := renderHTML("r", "tools", items, "", r)

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
