package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// collect is a sink that keeps what it is given.
func collect(into map[string][]byte) Sink {
	return func(name string, r io.Reader) (any, error) {
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		into[name] = b
		return len(b), nil
	}
}

func body(meta, attachments string) string {
	return `{"_id":"@acme/ui","name":"@acme/ui","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"@acme/ui","version":"1.0.0"}},` + meta + `"_attachments":{` + attachments + `}}`
}

func att(file string, data []byte, extra string) string {
	return fmt.Sprintf(`%q:{"content_type":"application/octet-stream",%s"data":%q,"length":%d}`, file, extra, base64.StdEncoding.EncodeToString(data), len(data))
}

func code(t *testing.T, err error) (string, int) {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("not a classified error: %v", err)
	}
	return e.Code, e.Status()
}

func TestScan(t *testing.T) {
	data := bytes.Repeat([]byte("tarball bytes "), 1000)
	t.Run("R-NPM-007 the data is decoded into the sink and the metadata is read", func(t *testing.T) {
		got := map[string][]byte{}
		env, err := Scan(context.Background(), strings.NewReader(body("", att("@acme/ui-1.0.0.tgz", data, ""))), Limits{}, collect(got))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got["@acme/ui-1.0.0.tgz"], data) {
			t.Fatal("the decoded data differs")
		}
		if env.Name != "@acme/ui" || env.ID != "@acme/ui" || len(env.Versions) != 1 || env.DistTags["latest"] != "1.0.0" {
			t.Fatalf("%+v", env)
		}
		a := env.Attachments[0]
		if a.Name != "@acme/ui-1.0.0.tgz" || a.Length != int64(len(data)) || a.Decoded != int64(len(data)) || a.ContentType != "application/octet-stream" {
			t.Fatalf("%+v", a)
		}
	})
	t.Run("R-NPM-007 the order of the members is not assumed", func(t *testing.T) {
		// data before length and content_type, and the metadata after the attachments
		doc := `{"_attachments":{"a.tgz":{"data":"` + base64.StdEncoding.EncodeToString(data) + `","length":14000,"content_type":"x"}},` +
			` "name" : "p", "versions":{"1.0.0":{}}, "dist-tags":{"latest":"1.0.0"} , "access":null, "unknown":[1,2,{"a":true}] }  `
		got := map[string][]byte{}
		env, err := Scan(context.Background(), strings.NewReader(doc), Limits{}, collect(got))
		if err != nil {
			t.Fatal(err)
		}
		if env.Name != "p" || len(env.Attachments) != 1 || env.Attachments[0].Length != 14000 || !bytes.Equal(got["a.tgz"], data) {
			t.Fatalf("%+v", env)
		}
	})
	t.Run("R-NPM-007 a JSON-escaped slash and plus in the base64 string decode", func(t *testing.T) {
		raw := []byte{0xfb, 0xff, 0xbf, 0xfe, 0x00, 0x01}
		enc := base64.StdEncoding.EncodeToString(raw) // "+/+//gAB"
		esc := strings.NewReplacer("/", `\/`, "+", `+`).Replace(enc)
		doc := `{"_attachments":{"a.tgz":{"data":"` + esc + `","length":6}}}`
		got := map[string][]byte{}
		if _, err := Scan(context.Background(), strings.NewReader(doc), Limits{}, collect(got)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got["a.tgz"], raw) {
			t.Fatalf("%x", got["a.tgz"])
		}
	})
	t.Run("R-NPM-007 a sink that reads nothing does not desynchronise the scan", func(t *testing.T) {
		lazy := func(name string, r io.Reader) (any, error) { return nil, nil }
		env, err := Scan(context.Background(), strings.NewReader(body("", att("a.tgz", data, ""))), Limits{}, lazy)
		if err != nil {
			t.Fatal(err)
		}
		if env.Attachments[0].Decoded != int64(len(data)) || env.Name != "@acme/ui" {
			t.Fatalf("%+v", env)
		}
	})
	t.Run("R-NPM-007 two attachments", func(t *testing.T) {
		got := map[string][]byte{}
		env, err := Scan(context.Background(), strings.NewReader(body("", att("a.tgz", data, "")+","+att("a.sigstore", []byte("bundle"), ""))), Limits{}, collect(got))
		if err != nil {
			t.Fatal(err)
		}
		if len(env.Attachments) != 2 || string(got["a.sigstore"]) != "bundle" {
			t.Fatalf("%+v", env)
		}
	})
	t.Run("R-NPM-007 a request without an attachment needs no sink", func(t *testing.T) {
		env, err := Scan(context.Background(), strings.NewReader(`{"name":"p","versions":{},"_attachments":{}}`), Limits{}, nil)
		if err != nil || len(env.Attachments) != 0 {
			t.Fatalf("%v %+v", err, env)
		}
	})
}

func TestScanRefusals(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte("abc"))
	cases := []struct {
		name   string
		doc    string
		lim    Limits
		code   string
		status int
	}{
		{"not an object", `[1]`, Limits{}, "npm_bad_publish", 400},
		{"empty", ``, Limits{}, "npm_bad_publish", 400},
		{"truncated", `{"name":"p","versions":{"1.0.0":`, Limits{}, "npm_bad_publish", 400},
		{"data after the document", `{"name":"p"} {"name":"q"}`, Limits{}, "npm_bad_publish", 400},
		{"duplicate member", `{"name":"p","name":"q"}`, Limits{}, "npm_bad_publish", 400},
		{"duplicate attachments member", `{"_attachments":{},"_attachments":{}}`, Limits{}, "npm_bad_publish", 400},
		{"duplicate attachment", `{"_attachments":{"a.tgz":{"data":"` + good + `"},"a.tgz":{"data":"` + good + `"}}}`, Limits{}, "npm_bad_publish", 400},
		{"attachment without data", `{"_attachments":{"a.tgz":{"length":3}}}`, Limits{}, "npm_bad_publish", 400},
		{"data is not a string", `{"_attachments":{"a.tgz":{"data":12}}}`, Limits{}, "npm_bad_publish", 400},
		{"length is not a number", `{"_attachments":{"a.tgz":{"data":"` + good + `","length":"3"}}}`, Limits{}, "npm_bad_publish", 400},
		{"negative length", `{"_attachments":{"a.tgz":{"data":"` + good + `","length":-3}}}`, Limits{}, "npm_bad_publish", 400},
		{"name is not a string", `{"name":3}`, Limits{}, "npm_bad_publish", 400},
		{"dist-tags with a non-string value", `{"dist-tags":{"latest":1}}`, Limits{}, "npm_bad_publish", 400},
		{"versions is an array", `{"versions":[]}`, Limits{}, "npm_bad_publish", 400},
		{"invalid base64 alphabet", `{"_attachments":{"a.tgz":{"data":"!!!!"}}}`, Limits{}, "npm_bad_publish", 400},
		{"base64 with data after the padding", `{"_attachments":{"a.tgz":{"data":"YQ==YQ=="}}}`, Limits{}, "npm_bad_publish", 400},
		{"base64 cut inside a group", `{"_attachments":{"a.tgz":{"data":"YWJjZ"}}}`, Limits{}, "npm_bad_publish", 400},
		{"non-ASCII in base64", `{"_attachments":{"a.tgz":{"data":"YWéj"}}}`, Limits{}, "npm_bad_publish", 400},
		{"an escape that base64 has no use for", `{"_attachments":{"a.tgz":{"data":"YW\nj"}}}`, Limits{}, "npm_bad_publish", 400},
		{"control character in a string", "{\"name\":\"a\x01b\"}", Limits{}, "npm_bad_publish", 400},
		{"bad escape", `{"name":"a\qb"}`, Limits{}, "npm_bad_publish", 400},
		{"bad unicode escape", `{"name":"a\u12g4"}`, Limits{}, "npm_bad_publish", 400},
		{"bad literal", `{"x":nul}`, Limits{}, "npm_bad_publish", 400},
		{"bad number", `{"x":1.2.3}`, Limits{}, "npm_bad_publish", 400},
		{"missing colon", `{"x" 1}`, Limits{}, "npm_bad_publish", 400},
		{"trailing comma", `{"x":1,}`, Limits{}, "npm_bad_publish", 400},
		{"too many attachments", `{"_attachments":{"a":{"data":""},"b":{"data":""},"c":{"data":""}}}`, Limits{MaxAttachments: 2}, "npm_bad_publish", 400},
		{"nesting beyond the depth", `{"x":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `}`, Limits{MaxDepth: 64}, "npm_too_large", 413},
		{"metadata beyond the budget", `{"x":"` + strings.Repeat("a", 5000) + `"}`, Limits{MaxMetadataBytes: 1024}, "npm_too_large", 413},
		{"a key beyond its bound", `{"` + strings.Repeat("k", 2000) + `":1}`, Limits{MaxKeyBytes: 1024}, "npm_too_large", 413},
		{"an attachment beyond the cap", `{"_attachments":{"a.tgz":{"data":"` + base64.StdEncoding.EncodeToString(make([]byte, 5000)) + `"}}}`, Limits{MaxDecoded: 4096}, "npm_too_large", 413},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.name, func(t *testing.T) {
			_, err := Scan(context.Background(), strings.NewReader(c.doc), c.lim, collect(map[string][]byte{}))
			if err == nil {
				t.Fatal("accepted")
			}
			if got, status := code(t, err); got != c.code || status != c.status {
				t.Fatalf("%d %s: %v", status, got, err)
			}
		})
	}
	t.Run("R-NPM-007 an attachment without a sink", func(t *testing.T) {
		_, err := Scan(context.Background(), strings.NewReader(`{"_attachments":{"a.tgz":{"data":"`+good+`"}}}`), Limits{}, nil)
		if c, _ := code(t, err); c != "npm_bad_publish" {
			t.Fatal(err)
		}
	})
	t.Run("R-NPM-007 the body cap of the server is a 413", func(t *testing.T) {
		rec := http.MaxBytesReader(nil, io.NopCloser(strings.NewReader(`{"name":"`+strings.Repeat("a", 4000)+`"}`)), 100)
		_, err := Scan(context.Background(), rec, Limits{}, nil)
		if c, status := code(t, err); c != "npm_too_large" || status != 413 {
			t.Fatal(err)
		}
	})
	t.Run("R-NPM-007 a failing source and a cancelled context are not the publisher's fault", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := Scan(context.Background(), io.MultiReader(strings.NewReader(`{"name":"`), &failing{boom}), Limits{}, nil)
		if !errors.Is(err, boom) {
			t.Fatalf("%v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Scan(ctx, strings.NewReader(`{}`), Limits{}, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-NPM-007 an error of the sink stops the scan", func(t *testing.T) {
		boom := errors.New("disk full")
		_, err := Scan(context.Background(), strings.NewReader(`{"_attachments":{"a.tgz":{"data":"`+good+`"}}}`), Limits{},
			func(string, io.Reader) (any, error) { return nil, boom })
		if !errors.Is(err, boom) {
			t.Fatalf("%v", err)
		}
	})
}

type failing struct{ err error }

func (f *failing) Read([]byte) (int, error) { return 0, f.err }

// genBody streams a publish body whose attachment is n zero bytes, without ever
// holding it.
type genBody struct {
	head, tail string
	left       int64 // base64 characters to go
	state      int
	buf        []byte
	chunk      []byte
}

func newGenBody(decoded int64) *genBody {
	return &genBody{head: `{"name":"p","_attachments":{"big.tgz":{"data":"`, tail: `","length":` + fmt.Sprint(decoded) + `}},"versions":{"1.0.0":{}}}`, left: (decoded / 3) * 4, chunk: bytes.Repeat([]byte("AAAA"), 16<<10)}
}

func (g *genBody) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		switch g.state {
		case 0:
			g.buf, g.state = []byte(g.head), 1
		case 1:
			if g.left == 0 {
				g.state = 2
				continue
			}
			n := min(g.left, 64<<10)
			g.buf, g.left = g.chunk[:n], g.left-n
		case 2:
			g.buf, g.state = []byte(g.tail), 3
		default:
			return 0, io.EOF
		}
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

func TestScanStreams(t *testing.T) {
	t.Run("R-NPM-007 a 96 MiB attachment is never held in memory", func(t *testing.T) {
		const size = 96 << 20
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		h := sha256.New()
		env, err := Scan(context.Background(), newGenBody(size), Limits{MaxDecoded: 128 << 20}, func(_ string, r io.Reader) (any, error) {
			_, err := io.Copy(h, r)
			return nil, err
		})
		if err != nil {
			t.Fatal(err)
		}
		runtime.ReadMemStats(&after)
		if env.Attachments[0].Decoded != size || env.Name != "p" || len(env.Versions) != 1 {
			t.Fatalf("%+v", env)
		}
		// Everything that streams allocates, but nothing in proportion to the data: the
		// base64 decoder and the copy buffers are reused.
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
			t.Fatalf("the scan allocated %d MiB for a %d MiB attachment", alloc>>20, size>>20)
		}
	})
	t.Run("R-NPM-007 413 at the cap without reading the rest", func(t *testing.T) {
		_, err := Scan(context.Background(), newGenBody(64<<20), Limits{MaxDecoded: 1 << 20}, func(_ string, r io.Reader) (any, error) {
			_, err := io.Copy(io.Discard, r)
			return nil, err
		})
		if c, status := code(t, err); c != "npm_too_large" || status != 413 {
			t.Fatal(err)
		}
	})
}

// FuzzPublishScanner feeds arbitrary bytes to the scanner (plan D-25). Whatever
// the input, it returns without a panic, every error is classified or is the
// reader's own, and an accepted body reports decoded lengths that match what the
// sink saw.
func FuzzPublishScanner(f *testing.F) {
	good := base64.StdEncoding.EncodeToString([]byte("tarball"))
	seeds := []string{
		``, `{}`, `[]`, `{"name":"p"}`,
		`{"_id":"p","name":"p","versions":{"1.0.0":{"name":"p"}},"dist-tags":{"latest":"1.0.0"},"_attachments":{"p-1.0.0.tgz":{"content_type":"application/octet-stream","data":"` + good + `","length":7}}}`,
		`{"_attachments":{"a.tgz":{"data":"YQ==","length":1}}}`,
		`{"_attachments":{"a.tgz":{"data":"Y\/Q=="}}}`,
		`{"_attachments":{"a":{"data":"` + good + `"},"a":{}}}`,
		`{"x":[[[[[[[[1]]]]]]]],"y":{"z":null,"w":true,"v":-1.5e+3}}`,
		`{"name":"\ud800","versions":{}}`,
		"{\"name\":\"a\x00\"}",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		lim := Limits{MaxMetadataBytes: 4096, MaxDepth: 8, MaxDecoded: 1 << 16, MaxAttachments: 3, MaxKeyBytes: 64}
		seen := map[string]int64{}
		env, err := Scan(context.Background(), bytes.NewReader(in), lim, func(name string, r io.Reader) (any, error) {
			n, err := io.Copy(io.Discard, r)
			seen[name] = n
			return n, err
		})
		if err != nil {
			if _, ok := errs.As(err); !ok {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		for _, a := range env.Attachments {
			if a.Decoded != seen[a.Name] || a.Decoded > lim.MaxDecoded {
				t.Fatalf("attachment %q: decoded %d, sink saw %d", a.Name, a.Decoded, seen[a.Name])
			}
		}
		if len(env.Attachments) > lim.MaxAttachments {
			t.Fatal("more attachments than the limit")
		}
	})
}
