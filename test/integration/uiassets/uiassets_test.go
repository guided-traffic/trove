//go:build integration

// Package uiassets tests the UI's shared asset store (R-HA-020, Q-HA-120 to Q-HA-123) on the integration stack: replicas of
// one installation, each with a UI build of its own, stand for the releases of a rolling upgrade. They share PostgreSQL and
// the object store as the replicas of two releases do, so a page whose index.html came from one replica must load every file
// that index names from the other.
package uiassets

import (
	"bytes"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/webui"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

// build is the UI build of one release: per locale an index.html naming a stylesheet, a preloaded chunk, the polyfills and
// the main script, a font under media/ and a lazy chunk that only the main script would name; an unhashed favicon of its
// own; a chunk both releases have with the same bytes; and a chunk of the same name whose bytes differ, as a release that
// only changes a translation has (the name's hash covers the code before the translations are inlined, Q-UI-140).
type build struct {
	tag  string // A, B, C
	tree fstest.MapFS
}

func newBuild(tag string, extra map[string]string) build {
	h := strings.Repeat(tag, 8)                                                              // AAAAAAAA
	mixed := strings.ToLower(tag) + tag + "_" + strings.ToLower(tag) + "-" + tag + tag + "1" // aA_a-AA1: a chunk optimizer's hash
	tree := fstest.MapFS{}
	for _, l := range []string{"en", "de"} {
		tree[l+"/index.html"] = &fstest.MapFile{Data: []byte(`<!doctype html><html lang="` + l + `"><head><base href="/ui/` + l + `/">` +
			`<link rel="icon" href="favicon.ico"><link rel="stylesheet" href="styles-` + h + `.css" nonce="TROVE_CSP_NONCE"></head>` +
			`<body><trove-root ngcspnonce="TROVE_CSP_NONCE"></trove-root>` +
			`<link rel="modulepreload" href="chunk-` + mixed + `.js" nonce="TROVE_CSP_NONCE">` +
			`<link rel="modulepreload" href="chunk-SHARED00.js" nonce="TROVE_CSP_NONCE">` +
			`<script src="polyfills-` + h + `.js" type="module" nonce="TROVE_CSP_NONCE"></script>` +
			`<script src="main-` + h + `.js" type="module" nonce="TROVE_CSP_NONCE"></script></body></html>`)}
		for name, body := range map[string]string{
			"styles-" + h + ".css":        "body{--release:" + tag + "}",
			"chunk-" + mixed + ".js":      "export const release='" + tag + "'",
			"polyfills-" + h + ".js":      "/* polyfills of " + tag + " */",
			"main-" + h + ".js":           "import('./chunk-lazy" + tag + "000.js'); /* main of " + tag + " " + l + " */",
			"chunk-lazy" + tag + "000.js": "export default 'the lazy route of " + tag + " in " + l + "'",
			"media/font-" + h + ".woff2":  "font of " + tag,
			"chunk-SHARED00.js":           "export const shared=1",
			"chunk-TRANSLAT.js":           "export const hello='" + tag + " " + l + "'",
			"favicon.ico":                 "favicon of " + tag,
		} {
			tree[l+"/"+name] = &fstest.MapFile{Data: []byte(body)}
		}
		for name, body := range extra {
			tree[l+"/"+name] = &fstest.MapFile{Data: []byte(body)}
		}
	}
	return build{tag: tag, tree: tree}
}

func (b build) file(t *testing.T, locale, path string) []byte {
	t.Helper()
	f, ok := b.tree[locale+"/"+path]
	if !ok {
		t.Fatalf("build %s has no %s/%s", b.tag, locale, path)
	}
	return f.Data
}

// named are the files an index.html names, which a browser loads next.
var named = regexp.MustCompile(`(?:src|href)="([^"/][^"]*)"`)

func indexNames(t *testing.T, st *testenv.Stack, r *testenv.Replica, locale string) []string {
	t.Helper()
	a := st.Do(r, "GET", "/ui/"+locale+"/", "", nil)
	if a.Status != 200 {
		t.Fatalf("GET /ui/%s/ on %s: %d %s", locale, r.Name, a.Status, a.Body)
	}
	var out []string
	for _, m := range named.FindAllStringSubmatch(string(a.Body), -1) {
		out = append(out, m[1])
	}
	return out
}

func wantFile(t *testing.T, st *testenv.Stack, r *testenv.Replica, locale, path string, want []byte) {
	t.Helper()
	a := st.Do(r, "GET", "/ui/"+locale+"/"+path, "", nil)
	if a.Status != 200 || !bytes.Equal(a.Body, want) {
		t.Errorf("%s/%s on %s: %d %q, want 200 %q", locale, path, r.Name, a.Status, a.Body, want)
		return
	}
	if webui.IsHashed(path) && a.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("%s/%s on %s: Cache-Control %q", locale, path, r.Name, a.Header.Get("Cache-Control"))
	}
	if a.Header.Get("X-Content-Type-Options") != "nosniff" || a.Header.Get("Content-Type") == "" {
		t.Errorf("%s/%s on %s: headers %v", locale, path, r.Name, a.Header)
	}
}

func wantStatus(t *testing.T, st *testenv.Stack, r *testenv.Replica, path string, want int) testenv.Answer {
	t.Helper()
	a := st.Do(r, "GET", path, "", nil)
	if a.Status != want {
		t.Errorf("%s on %s: %d %s, want %d", path, r.Name, a.Status, a.Body, want)
	}
	return a
}

func TestR_HA_020_SharedUIAssets(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Valkey: true, Name: "uiassets"})
	a, b := newBuild("A", map[string]string{"notes.txt": "an unhashed file only A has"}), newBuild("B", nil)
	ra := st.Start(t, testenv.ReplicaOptions{Name: "ui-a", Role: "api", UI: a.tree})
	rb := st.Start(t, testenv.ReplicaOptions{Name: "ui-b", Role: "api", UI: b.tree})

	t.Run("R-HA-020 every file the index.html of one release names loads from a replica of the other, with its own bytes", func(t *testing.T) {
		for _, pair := range []struct {
			from, to *testenv.Replica
			b        build
		}{{ra, rb, a}, {rb, ra, b}} {
			for _, l := range []string{"en", "de"} {
				names := indexNames(t, st, pair.from, l)
				var hashed int
				for _, n := range names {
					if !webui.IsHashed(n) {
						continue
					}
					hashed++
					wantFile(t, st, pair.to, l, n, pair.b.file(t, l, n))
				}
				if hashed != 5 {
					t.Errorf("the index of %s/%s names %d hashed files, want 5: %v", pair.b.tag, l, hashed, names)
				}
				// the lazy chunk that only the main script names, as a route opened later in the page does, and the font
				// that only the stylesheet names
				h := strings.Repeat(pair.b.tag, 8)
				for _, n := range []string{"chunk-lazy" + pair.b.tag + "000.js", "media/font-" + h + ".woff2"} {
					wantFile(t, st, pair.to, l, n, pair.b.file(t, l, n))
				}
			}
		}
	})

	t.Run("R-HA-020 a file both releases have is served by each from its own build", func(t *testing.T) {
		for _, l := range []string{"en", "de"} {
			wantFile(t, st, ra, l, "chunk-TRANSLAT.js", a.file(t, l, "chunk-TRANSLAT.js"))
			wantFile(t, st, rb, l, "chunk-TRANSLAT.js", b.file(t, l, "chunk-TRANSLAT.js"))
			wantFile(t, st, rb, l, "chunk-SHARED00.js", b.file(t, l, "chunk-SHARED00.js"))
		}
		// the store keeps the first replica's row of a name (write-once): A started first
		var first string
		st.Query(`SELECT published_by FROM ui_assets WHERE locale = 'en' AND path = 'chunk-TRANSLAT.js'`, func(rows pgx.Rows) error {
			return rows.Scan(&first)
		})
		if first == "" {
			t.Error("chunk-TRANSLAT.js has no row")
		}
	})

	t.Run("R-HA-020 a hashed name no release published is a 404 on every replica", func(t *testing.T) {
		for _, r := range []*testenv.Replica{ra, rb} {
			for _, p := range []string{"/ui/en/main-ZZZZZZZZ.js", "/ui/de/chunk-zZ_z-ZZ1.js", "/ui/en/media/font-ZZZZZZZZ.woff2"} {
				if a := wantStatus(t, st, r, p, 404); a.Header.Get("Content-Type") != "application/problem+json" {
					t.Errorf("%s on %s: Content-Type %q", p, r.Name, a.Header.Get("Content-Type"))
				}
			}
		}
	})

	t.Run("R-HA-020 an unhashed path is never served from the store and never put there", func(t *testing.T) {
		// the favicon is each replica's own; A's unhashed notes.txt is not on B
		wantFile(t, st, rb, "en", "favicon.ico", []byte("favicon of B"))
		wantFile(t, st, ra, "en", "favicon.ico", []byte("favicon of A"))
		wantStatus(t, st, ra, "/ui/en/notes.txt", 200)
		wantStatus(t, st, rb, "/ui/en/notes.txt", 404)
		// index.html and a route are the replica's own build, never the store's
		if names := indexNames(t, st, rb, "en"); !slices.Contains(names, "main-BBBBBBBB.js") {
			t.Errorf("B's index names %v", names)
		}
		var paths []string
		st.Query(`SELECT locale || '/' || path FROM ui_assets ORDER BY 1`, func(rows pgx.Rows) error {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
			return nil
		})
		// per locale: the six hashed files of A's own, the six of B's, the shared chunk and the translated one
		if len(paths) != 2*(6+6+2) {
			t.Errorf("%d rows, want %d: %v", len(paths), 2*(6+6+2), paths)
		}
		for _, p := range paths {
			_, rest, _ := strings.Cut(p, "/")
			if !webui.IsHashed(rest) {
				t.Errorf("the store holds the unhashed %s", p)
			}
		}
		// every row names a live blob: the reference keeps the collector away (R-STOR-024)
		if n := st.Number(`SELECT count(*) FROM ui_assets u LEFT JOIN blobs b ON b.sha256 = u.blob_sha256 AND b.state = 'live' WHERE b.sha256 IS NULL`); n != 0 {
			t.Errorf("%d rows without a live blob", n)
		}
	})

	t.Run("R-HA-020 a replica that starts again renews its rows and puts back the ones that are gone", func(t *testing.T) {
		// a restore leaves the table empty (export class transient): the next start of a replica of B publishes B again
		st.Exec(`DELETE FROM ui_assets WHERE path LIKE '%BBBBBBBB%'`)
		st.Exec(`UPDATE ui_assets SET seen_at = now() - interval '2 hours' WHERE path LIKE '%bB_b-BB1%'`)
		before := st.Number(`SELECT count(*) FROM ui_assets`)
		rb2 := st.Start(t, testenv.ReplicaOptions{Name: "ui-b2", Role: "api", UI: b.tree})
		if after := st.Number(`SELECT count(*) FROM ui_assets`); after != before+2*4 {
			t.Errorf("rows %d after the start, %d before: want the 8 deleted files of B back", after, before)
		}
		if n := st.Number(`SELECT count(*) FROM ui_assets WHERE path LIKE '%bB_b-BB1%' AND seen_at > now() - interval '1 minute'`); n != 2 {
			t.Errorf("%d renewed rows of B's chunk, want 2", n)
		}
		wantFile(t, st, ra, "en", "main-BBBBBBBB.js", b.file(t, "en", "main-BBBBBBBB.js"))
		_ = rb2
	})

	t.Run("R-HA-020 a file no replica renewed within the retention is pruned and its blob queued for collection", func(t *testing.T) {
		st.Exec(`UPDATE ui_assets SET seen_at = now() - interval '8 days' WHERE path LIKE '%lazyA000%'`)
		digests := st.Number(`SELECT count(DISTINCT blob_sha256) FROM ui_assets WHERE path LIKE '%lazyA000%'`)
		// any replica's start-up cycle prunes; A's own replica renews at its start and then hourly, so a replica of a third
		// build is the one that prunes the row here
		rc := st.Start(t, testenv.ReplicaOptions{Name: "ui-c", Role: "api", UI: newBuild("C", nil).tree})
		if n := st.Number(`SELECT count(*) FROM ui_assets WHERE path LIKE '%lazyA000%'`); n != 0 {
			t.Errorf("%d rows of A's lazy chunk left after the retention", n)
		}
		if n := st.Number(`SELECT count(*) FROM blob_gc_queue q JOIN blobs b ON b.sha256 = q.sha256 WHERE NOT EXISTS (SELECT 1 FROM ui_assets u WHERE u.blob_sha256 = q.sha256)`); n < digests {
			t.Errorf("%d blobs queued for collection, want at least %d", n, digests)
		}
		// C never read it; B keeps the copy it read in its memory, which is the same immutable file
		wantStatus(t, st, rc, "/ui/en/chunk-lazyA000.js", 404)
		// A's own replica still serves its own file
		wantFile(t, st, ra, "en", "chunk-lazyA000.js", a.file(t, "en", "chunk-lazyA000.js"))
	})
}

// TestR_HA_020_SharedUIAssetsWithoutTheObjectStore is the store unavailable: a replica whose object store goes away keeps
// serving its own build and its index.html, answers a file of the other release 503 with Retry-After (the file may exist,
// so the answer is no 404 and is not cached), answers a name no release published 404 from PostgreSQL alone, and serves
// the other release's file again once the object store is back.
func TestR_HA_020_SharedUIAssetsWithoutTheObjectStore(t *testing.T) {
	st := testenv.NewStack(t, testenv.StackOptions{Valkey: true, Name: "uiassets_s3"})
	a, c := newBuild("A", nil), newBuild("C", nil)
	st.Start(t, testenv.ReplicaOptions{Name: "ui-a", Role: "api", UI: a.tree})

	s3 := testenv.S3Config(t)
	u, err := url.Parse(s3.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newTCPProxy(t, u.Host)
	rc := st.Start(t, testenv.ReplicaOptions{
		Name: "ui-c", Role: "web", UI: c.tree,
		Env: []string{"TROVE_STORAGE_S3_ENDPOINT=https://" + proxy.addr(), "TROVE_STORAGE_S3_CIRCUIT_BREAKER_OPEN_DURATION=1s"},
	})
	wantFile(t, st, rc, "en", "main-AAAAAAAA.js", a.file(t, "en", "main-AAAAAAAA.js"))

	proxy.setDown(true)
	t.Run("R-HA-020 the object store away: the own build serves, the other release's file is 503 with Retry-After, an unknown name 404", func(t *testing.T) {
		wantFile(t, st, rc, "en", "main-CCCCCCCC.js", c.file(t, "en", "main-CCCCCCCC.js"))
		if names := indexNames(t, st, rc, "en"); !slices.Contains(names, "main-CCCCCCCC.js") {
			t.Errorf("C's index names %v", names)
		}
		// main-AAAAAAAA.js is in C's memory since the first read; the polyfills were never read on C
		wantFile(t, st, rc, "en", "main-AAAAAAAA.js", a.file(t, "en", "main-AAAAAAAA.js"))
		for range 2 {
			res := wantStatus(t, st, rc, "/ui/en/polyfills-AAAAAAAA.js", 503)
			if res.Header.Get("Retry-After") == "" || res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Content-Type") != "application/problem+json" {
				t.Errorf("the 503: headers %v", res.Header)
			}
		}
		wantStatus(t, st, rc, "/ui/en/polyfills-ZZZZZZZZ.js", 404)
	})

	proxy.setDown(false)
	t.Run("R-HA-020 the object store back: the other release's file serves again", func(t *testing.T) {
		testenv.WaitFor(t, "the polyfills of A on C", 90*time.Second, func() bool {
			res := st.Do(rc, "GET", "/ui/en/polyfills-AAAAAAAA.js", "", nil)
			return res.Status == 200 && bytes.Equal(res.Body, a.file(t, "en", "polyfills-AAAAAAAA.js"))
		})
	})
}

// tcpProxy forwards TCP to the object store and can be taken away: while down it refuses new connections and cuts the open
// ones. TLS passes through it, so the replica checks the store's certificate as before (the address is the same host).
type tcpProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	down   bool
	conns  map[net.Conn]struct{}
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	t.Cleanup(func() { _ = ln.Close(); p.setDown(true) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) setDown(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if down {
		for c := range p.conns {
			_ = c.Close()
		}
		clear(p.conns)
	}
}

func (p *tcpProxy) serve(c net.Conn) {
	p.mu.Lock()
	if p.down {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", p.target, 2*time.Second)
	if err != nil {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c], p.conns[up] = struct{}{}, struct{}{}
	p.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); _ = up.Close(); _ = c.Close() }()
	_, _ = io.Copy(c, up)
	_ = c.Close()
	_ = up.Close()
}
