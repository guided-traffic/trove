package document

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func inputs() Inputs {
	return Inputs{
		RenderVersion: 1, Repo: 7, Package: 11, Rev: 3, Variant: "registration-gz/index", Serving: "nuget-local",
		BaseURL: "https://trove.example.com", Endpoint: "default", Settings: "4/64/128",
	}
}

// TestR_NUGET_002_ETagIsWeakAndFollowsItsInputs: the value is weak, the same inputs give
// the same value, and each input moves it (Q-NUGET-9). The content coding is not an
// input.
func TestR_NUGET_002_ETagIsWeakAndFollowsItsInputs(t *testing.T) {
	base := inputs().ETag()
	if !strings.HasPrefix(base, `W/"`) || !strings.HasSuffix(base, `"`) {
		t.Fatalf("ETag %q is not weak", base)
	}
	if inputs().ETag() != base {
		t.Error("the same inputs give another value")
	}
	for name, mod := range map[string]func(*Inputs){
		"render version": func(i *Inputs) { i.RenderVersion++ },
		"repository":     func(i *Inputs) { i.Repo++ },
		"package":        func(i *Inputs) { i.Package++ },
		"revision":       func(i *Inputs) { i.Rev++ },
		"variant":        func(i *Inputs) { i.Variant = "registration/index" },
		"serving key":    func(i *Inputs) { i.Serving = "nuget-local~cache" },
		"base URL":       func(i *Inputs) { i.BaseURL = "https://other.example.com" },
		"endpoint":       func(i *Inputs) { i.Endpoint = "customers" },
		"exposure":       func(i *Inputs) { i.Exposure = "abc" },
		"settings":       func(i *Inputs) { i.Settings = "4/32/128" },
		"content":        func(i *Inputs) { i.Content = ContentDigest([]byte(`{"versions":["2.0.0"]}`)) },
	} {
		in := inputs()
		mod(&in)
		if in.ETag() == base {
			t.Errorf("a different %s gives the same value", name)
		}
	}
	// Two fields cannot trade characters: the fields are length-prefixed.
	a, b := inputs(), inputs()
	a.Variant, a.Serving = "ab", "c"
	b.Variant, b.Serving = "a", "bc"
	if a.ETag() == b.ETag() {
		t.Error("two inputs that differ only in where a field ends give the same value")
	}
}

// TestR_NUGET_002_AnEmptyContentKeepsTheValueOfEarlierReleases: Content enters the value only
// when it is set, so the validator of a local document outside an endpoint with rules is the one
// the releases before Q-NUGET-87 computed, and a rolling upgrade does not turn its 304s into 200s.
// The digest is the SHA-256 of the bytes in hex.
func TestR_NUGET_002_AnEmptyContentKeepsTheValueOfEarlierReleases(t *testing.T) {
	if got := inputs().ETag(); got != `W/"5e5fe61503a3a4ea2c96923aafe9e1cb"` {
		t.Errorf("ETag = %s, want the value of the earlier releases", got)
	}
	if got := ContentDigest([]byte("a")); got != "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" {
		t.Errorf("digest of a = %s, want the SHA-256 of a", got)
	}
	if got := ContentDigest(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("digest of nothing = %s, want the SHA-256 of nothing", got)
	}
}

// TestR_NUGET_002_IfNoneMatchUsesTheWeakComparison: a strong or weak copy of the value
// matches, any member of a list matches, another value and the wildcard do not.
func TestR_NUGET_002_IfNoneMatchUsesTheWeakComparison(t *testing.T) {
	const etag = `W/"abc"`
	for header, want := range map[string]bool{
		`W/"abc"`:           true,
		`"abc"`:             true,
		`"x", W/"abc"`:      true,
		` W/"abc" ,"y"`:     true,
		`W/"abd"`:           false,
		`*`:                 false,
		``:                  false,
		`W/"abc`:            false,
		`"abcd", W/"ab"`:    false,
		`W/"abc"x`:          false,
		`"x" W/"abc" "abc"`: false,
	} {
		if got := IfNoneMatch(header, etag); got != want {
			t.Errorf("IfNoneMatch(%q) = %v, want %v", header, got, want)
		}
	}
}

// TestR_NUGET_002_AcceptsGzip reads Accept-Encoding the way RFC 9110 says.
func TestR_NUGET_002_AcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip":                  true,
		"GZIP":                  true,
		"x-gzip":                true,
		"deflate, gzip;q=0.5":   true,
		"gzip;q=0":              false,
		"gzip; q=0.0, deflate":  false,
		"identity":              false,
		"*":                     false,
		"":                      false,
		"br, gzip ;q=1.0":       true,
		"gzipped":               false,
		"gzip;q=notanumber":     true,
		"deflate;q=1, gzip;q=0": false,
	} {
		if got := AcceptsGzip(header); got != want {
			t.Errorf("AcceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}

// TestR_NUGET_030_MarshalIsDeterministic: struct members keep their order, map keys are
// sorted, HTML characters stay as they are, and there is no trailing newline.
func TestR_NUGET_030_MarshalIsDeterministic(t *testing.T) {
	type doc struct {
		Z string         `json:"z"`
		A string         `json:"a"`
		M map[string]int `json:"m"`
	}
	v := doc{Z: "<b>&", A: "x", M: map[string]int{"b": 2, "a": 1, "c": 3, "d": 4, "e": 5}}
	want := `{"z":"<b>&","a":"x","m":{"a":1,"b":2,"c":3,"d":4,"e":5}}`
	for range 50 {
		got, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("Marshal = %s, want %s", got, want)
		}
	}
	if _, err := Marshal(make(chan int)); err == nil {
		t.Error("an unmarshalable value was rendered")
	}
}

// TestR_NUGET_030_TimestampIsFixedPrecision: whole seconds, UTC, truncated; the zone of
// the instant does not matter.
func TestR_NUGET_030_TimestampIsFixedPrecision(t *testing.T) {
	zone := time.FixedZone("x", 2*3600)
	for _, tc := range []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC), "2026-10-28T09:14:03Z"},
		{time.Date(2026, 10, 28, 9, 14, 3, 999999999, time.UTC), "2026-10-28T09:14:03Z"},
		{time.Date(2026, 10, 28, 11, 14, 3, 500, zone), "2026-10-28T09:14:03Z"},
		{time.Time{}, "0001-01-01T00:00:00Z"},
	} {
		if got := Timestamp(tc.in); got != tc.want {
			t.Errorf("Timestamp(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestR_NUGET_008_VersionListBytes: the list is {"versions":[...]} in the order given, and
// an empty list is an empty array, never null.
func TestR_NUGET_008_VersionListBytes(t *testing.T) {
	b, err := VersionList([]string{"1.2.3", "1.3.0-beta.1"})
	if err != nil || string(b) != `{"versions":["1.2.3","1.3.0-beta.1"]}` {
		t.Errorf("VersionList = %s, %v", b, err)
	}
	if b, _ := VersionList(nil); string(b) != `{"versions":[]}` {
		t.Errorf("VersionList(nil) = %s", b)
	}
}

// TestR_NUGET_030_GzipIsDeterministicAndRoundTrips: the same input compresses to the same
// bytes, with no name and no modification time in the header, and expands to the input.
func TestR_NUGET_030_GzipIsDeterministicAndRoundTrips(t *testing.T) {
	in := bytes.Repeat([]byte(`{"count":1}`), 500)
	a, b := Gzip(in), Gzip(in)
	if !bytes.Equal(a, b) {
		t.Fatal("two compressions differ")
	}
	zr, err := gzip.NewReader(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if !zr.ModTime.IsZero() || zr.Name != "" {
		t.Errorf("header carries mtime %v and name %q", zr.ModTime, zr.Name)
	}
	out, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(out, in) {
		t.Errorf("round trip: %v", err)
	}
	if len(a) >= len(in) {
		t.Errorf("gzip is %d bytes for %d", len(a), len(in))
	}
}

// TestR_NUGET_002_ReplyWriteHeadEqualsGetMinusBody: HEAD carries the headers of the GET,
// the Content-Length of the variant that GET sends, and no body - for identity and gzip.
func TestR_NUGET_002_ReplyWriteHeadEqualsGetMinusBody(t *testing.T) {
	plain := []byte(strings.Repeat(`{"a":1}`, 100))
	for name, r := range map[string]Reply{
		"identity":       {ETag: `W/"e"`, Body: plain},
		"gzip":           {ETag: `W/"e"`, Body: Gzip(plain), Gzip: true, Vary: true},
		"identity, vary": {ETag: `W/"e"`, Body: plain, Vary: true},
	} {
		t.Run(name, func(t *testing.T) {
			get, head := httptest.NewRecorder(), httptest.NewRecorder()
			if err := r.Write(get, http.MethodGet); err != nil {
				t.Fatal(err)
			}
			if err := r.Write(head, http.MethodHead); err != nil {
				t.Fatal(err)
			}
			if get.Code != 200 || head.Code != 200 {
				t.Fatalf("status %d / %d", get.Code, head.Code)
			}
			if got := get.Header(); got.Get("Content-Type") != ContentType || got.Get("ETag") != `W/"e"` ||
				got.Get("Content-Length") != strconv.Itoa(len(r.Body)) || (got.Get("Content-Encoding") == "gzip") != r.Gzip ||
				(got.Get("Vary") == "Accept-Encoding") != r.Vary {
				t.Errorf("GET headers = %v", got)
			}
			if get.Header().Get("Content-Length") != head.Header().Get("Content-Length") ||
				get.Header().Get("ETag") != head.Header().Get("ETag") ||
				get.Header().Get("Content-Encoding") != head.Header().Get("Content-Encoding") ||
				get.Header().Get("Content-Type") != head.Header().Get("Content-Type") ||
				get.Header().Get("Vary") != head.Header().Get("Vary") {
				t.Errorf("HEAD headers %v differ from GET %v", head.Header(), get.Header())
			}
			if !bytes.Equal(get.Body.Bytes(), r.Body) || head.Body.Len() != 0 {
				t.Errorf("bodies: GET %d bytes, HEAD %d bytes", get.Body.Len(), head.Body.Len())
			}
		})
	}
}

// TestR_NUGET_002_NotModified: a 304 names the validator and, for a gzip hive, the Vary.
func TestR_NUGET_002_NotModified(t *testing.T) {
	w := httptest.NewRecorder()
	NotModified(w, `W/"e"`, true)
	if w.Code != 304 || w.Header().Get("ETag") != `W/"e"` || w.Header().Get("Vary") != "Accept-Encoding" || w.Body.Len() != 0 {
		t.Errorf("304 = %d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	NotModified(w, `W/"e"`, false)
	if w.Header().Get("Vary") != "" {
		t.Errorf("Vary on a document with no gzip variant: %v", w.Header())
	}
}
