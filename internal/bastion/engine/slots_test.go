package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/link"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/maintest"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/bastion/shared"
)

// handler serves the site over HTTP, for the real client and the in-process main installation: the site's
// answers (the marking is in their headers) become the response.
func (s *site) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.doFn(mainclient.Forward{Request: r, IfNoneMatch: r.Header.Get("If-None-Match"), Probe: r.Header.Get(iface.HeaderProbe) != ""})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(resp.Status)
		_, _ = io.Copy(w, resp.Body)
	}
}

// TestR_BAS_025_EveryPathClosesTheResponseBodyAndSoReleasesItsSlot: a slot of the budget is held until the
// response body ends or is closed (mainclient.SlotBudget), so the engine has to close every body on every path.
// The engine runs over the real client and a budget of one slot against the in-process main installation, and
// every kind of answer is served in turn: a miss that streams, a hit, a 304 that serves the cached bytes, a 304
// whose bytes are gone, a Range request, a shared document, a private copy, an absent name, a pass-through, a
// refusal, a write probe, an invalid token and a fill whose digest does not match (the transfer is aborted).
// After each request no slot is in use; at the end, a request still gets the one slot at once, and the main
// installation never saw two requests at a time.
func TestR_BAS_025_EveryPathClosesTheResponseBodyAndSoReleasesItsSlot(t *testing.T) {
	dir := t.TempDir()
	m := maintest.New(t)
	m.SetAnonymous(true)
	st := newSite("web/npm-all")
	m.SetForward(st.handler())
	m.SetCheck(func(req iface.CheckRequest) iface.CheckAnswer { a, _ := st.checkFn(req); return a })

	full, part, bad := newToken(t, "pat"), newToken(t, "pat"), newToken(t, "pat")
	st.addReader(full, &reader{id: "full", read: iface.ReadFull})
	st.addReader(part, &reader{id: "part", read: iface.ReadPartial, allowed: func(p string) bool { return strings.Contains(p, "/@acme/") }})
	art := func(name string, n int) string {
		p := "/api/npm/web/npm-all/@acme/" + name + ".tgz"
		st.artifacts[p] = payload(n)
		return p
	}
	cold, gone, rng, corrupt := art("cold", 200<<10), art("gone", 5000), art("range", 100<<10), art("corrupt", 200<<10)
	st.corrupt[corrupt] = true
	st.docs["/api/npm/web/npm-all/shared"] = "all packages"
	st.docs["/api/npm/web/npm-all/@acme/private"] = "mine"

	cfg := testConfig(t, func(c *configT) {
		c.Bastion.Main.MaxConcurrent, c.Bastion.Replicas, c.Bastion.Main.QueueTimeout = 1, 1, 300*time.Millisecond
		c.Bastion.Cache.ArtifactRevalidateAfter = time.Second
		c.Bastion.Cache.MetadataRevalidateAfter = time.Second
	})
	clk := &testClock{t: time.Now().UTC().Truncate(time.Second)}
	bm, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	tc := newTestCache(t, cfg, nil, bm, clk.now)
	holder := mainclient.NewHolder(mainclient.Token{Value: m.NewToken(), Hint: "trv_bst_test"})
	client, err := mainclient.New(mainclient.Options{
		URL: m.URL(), BaseURL: "https://packages.example.com", Replica: "bastion-0", Release: "test", Tokens: holder,
		Budget: mainclient.NewBudget(mainclient.BudgetOptions{
			MaxConcurrent: 1, Replicas: 1, AnonymousShare: 0.5, QueueTimeout: 300 * time.Millisecond, Metrics: bm,
		}),
		ConnectTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, CAFile: m.CAFile(dir), Metrics: bm,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	lnk := link.New(link.Options{Client: client, Config: link.ConfigFrom(cfg), Metrics: bm, Now: clk.now, Tokens: holder})
	lnk.Tick(context.Background())
	if !lnk.Reachable() {
		t.Fatal("the link did not become reachable against the in-process main installation")
	}
	dl := &fakeDownloads{}
	e, err := New(Deps{
		Config: cfg, Metrics: bm, Replica: "bastion-0", Release: "test", Now: clk.now, Main: lnk.Guard(client), Link: lnk,
		Cache: tc.Cache, Shared: shared.NewLocal(1, clk.now), Guard: newGuard(), Downloads: dl,
	})
	if err != nil {
		t.Fatal(err)
	}

	inFlight := func() float64 {
		return readCounter(bm.MainInFlight.WithLabelValues("authenticated")) + readCounter(bm.MainInFlight.WithLabelValues("anonymous"))
	}
	call := func(name, method, path string, hdr ...string) (code int) {
		t.Helper()
		r := httptest.NewRequest(method, "https://packages.example.com"+path, strings.NewReader(map[bool]string{true: "x"}[method == http.MethodPut]))
		r.RemoteAddr = "203.0.113.7:4711"
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		func() {
			defer func() {
				if p := recover(); p != nil && !errors.Is(asError(p), http.ErrAbortHandler) {
					panic(p)
				}
			}()
			e.ServeHTTP(w, r)
		}()
		if n := inFlight(); n != 0 {
			t.Fatalf("%s: %v slots are still in use after the request", name, n)
		}
		return w.Code
	}
	auth := func(tok string) []string { return []string{"Authorization", bearer(tok)} }
	expect := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Fatalf("%s: %d, want %d", name, got, want)
		}
	}

	expect("a miss that streams", call("miss", http.MethodGet, cold, auth(full)...), 200)
	expect("a hit", call("hit", http.MethodGet, cold, auth(full)...), 200)
	expect("a partial reader of a cached artifact (a check and a 304)", call("partial reader", http.MethodGet, cold, auth(part)...), 200)
	clk.advance(2 * time.Second) // past artifact_revalidate_after
	expect("a conditional request answered 304", call("304", http.MethodGet, cold, auth(full)...), 200)
	expect("a miss for the bytes that vanish", call("gone miss", http.MethodGet, gone, auth(full)...), 200)
	for _, p := range tc.files("sha256") {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	clk.advance(2 * time.Second)
	expect("a 304 whose bytes are gone", call("304 gone", http.MethodGet, gone, auth(full)...), 200)
	expect("a Range request on a miss", call("range", http.MethodGet, rng, append(auth(full), "Range", "bytes=10-99")...), 206)
	expect("a shared document", call("shared", http.MethodGet, "/api/npm/web/npm-all/shared", auth(full)...), 200)
	clk.advance(2 * time.Second)
	expect("a shared document revalidated (304)", call("shared 304", http.MethodGet, "/api/npm/web/npm-all/shared", auth(full)...), 200)
	expect("a private copy", call("private", http.MethodGet, "/api/npm/web/npm-all/@acme/private", auth(part)...), 200)
	expect("a private copy revalidated (304)", call("private 304", http.MethodGet, "/api/npm/web/npm-all/@acme/private", auth(part)...), 200)
	expect("an absent name", call("absent", http.MethodGet, "/api/npm/web/npm-all/nope", auth(full)...), 404)
	expect("a pass-through", call("search", http.MethodGet, "/api/npm/web/npm-all/-/v1/search?text=a", auth(full)...), 200)
	expect("a refusal", call("refusal", http.MethodGet, "/api/npm/web/npm-all/@acme/hidden.tgz", auth(none(t, st))...), 404)
	expect("a write probe", call("probe", http.MethodPut, "/api/npm/web/npm-all/x", auth(full)...), 403)
	expect("an invalid token", call("invalid", http.MethodGet, cold, auth(bad)...), 401)
	call("a fill whose digest does not match", http.MethodGet, corrupt, auth(full)...)

	for i := 0; i < 10; i++ {
		expect(fmt.Sprintf("sequential request %d on one slot", i), call("tail", http.MethodGet, cold, auth(full)...), 200)
	}
	if peak := m.MaxInFlight(); peak > 1 {
		t.Fatalf("the main installation saw %d requests at a time with a budget of one slot", peak)
	}
	if readCounter(bm.BudgetExhausted.WithLabelValues("authenticated"))+readCounter(bm.BudgetExhausted.WithLabelValues("anonymous")) != 0 {
		t.Fatal("a request waited for a slot out: a body stayed open")
	}
}

// none registers a reader that reads nothing and returns its token.
func none(t *testing.T, st *site) string {
	t.Helper()
	tok := newToken(t, "pat")
	st.addReader(tok, &reader{id: "none", read: iface.ReadNone})
	return tok
}

// asError returns the value of a recovered panic as an error, nil when it is none.
func asError(p any) error {
	err, _ := p.(error)
	return err
}
