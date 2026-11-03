package formattest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"runtime"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/httpserver/content"
	"github.com/guided-traffic/trove/internal/repository"
)

// Bounds of check 6 (R-ARCH-028 acceptance): a crafted input is rejected within
// inspectDeadline, and the heap stays within heapBudget of its baseline.
const (
	inspectDeadline = 5 * time.Second
	heapBudget      = 512 << 20
	// raceSlowdown stretches inspectDeadline in a build under the race detector, which with the atomic coverage of
	// make test makes the kit's inspections about ten times slower (TestR_ARCH_010_Defects: 3.8 s plain, 39 s
	// instrumented). The instrumented bound still catches an inspection that runs unbounded; the bound of R-ARCH-028
	// itself is held by the plain run of the kit that make test adds.
	raceSlowdown = 10
)

// timeBound is the time an inspection may take in this build: inspectDeadline, stretched under the race detector.
func timeBound() time.Duration {
	if raceDetector {
		return inspectDeadline * raceSlowdown
	}
	return inspectDeadline
}

// ambiguity is check 1: every route matches its examples and no other route of
// its method does. formats.Validate is the check Register runs; the kit adds the
// routing through the format's own Match, so a Match that does not use the
// format's routes fails too.
func (r *run) ambiguity() {
	if err := formats.Validate(r.f); err != nil {
		r.failf("%v", err)
		return
	}
	if err := registers(r.f); err != nil {
		r.failf("%v", err)
		return
	}
	routes := r.f.Routes()
	if len(routes) == 0 {
		r.failf("%s: the format has no routes and the check is not skipped (Skip[%d] needs a reason)", CheckAmbiguity, CheckAmbiguity)
		return
	}
	for _, rt := range routes {
		for _, ex := range rt.Examples {
			got, _, err := r.f.Match(rt.Method, ex)
			switch {
			case err != nil:
				r.failf("route %q: Match(%s, %q) fails: %v", rt.Name, rt.Method, ex, err)
			case got.Name != rt.Name:
				r.failf("route %q: its example %q is matched by route %q (ambiguous routes)", rt.Name, ex, got.Name)
			}
		}
	}
}

// registers runs what Register does on a private catalog: Validate and the
// catalog's own rules.
func registers(f formats.Format) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("register would panic: %v", p)
		}
	}()
	catalog.NewRegistry().Register(formats.CatalogEntry(f))
	return nil
}

// traversalInputs are the inputs Match refuses whatever the format's routes.
func traversalInputs() []string {
	return []string{
		"..", "../x", "x/..", "a/../b", "%2e%2e", "%2E%2e%2f", "%2e%2e/x", "a/%2e%2e/b", "a/%2E%2e%2fb", "..%2f", "..%2F..%2F",
		".", "a/./b", "a//b", `a\b`, "a%5Cb", "a%5cb", "a\x00b", "a%00b", "a\x01b", "a%01b", "a%1fb", "a%7fb", "a%zz", "a%", "%ff",
		strings.Repeat("a", 4097), strings.Repeat("a/", 2100) + "b", strings.Repeat("a", 256),
	}
}

// mutate returns the traversal variants of an example.
func mutate(ex string) []string {
	return []string{
		ex + "/..", "../" + ex, ex + "/%2e%2e", ex + "/%2E%2e%2f", "%2e%2e/" + ex, ex + "\x00", ex + "%00", ex + "\x01",
		ex + "/./" + ex, strings.Repeat("a/", 2100) + ex, ex + "/" + strings.Repeat("a", 4097),
	}
}

// traversal is check 2: Match refuses traversal, NUL, control characters and
// overlong paths. It runs on a format without routes as well: its Match must
// still refuse them, and generic's Match is the same one call.
func (r *run) traversal() {
	methods := map[string]bool{http.MethodGet: true}
	var inputs []string
	inputs = append(inputs, traversalInputs()...)
	inputs = append(inputs, r.fx.Rejects...)
	for _, rt := range r.f.Routes() {
		methods[rt.Method] = true
		for _, ex := range rt.Examples {
			inputs = append(inputs, mutate(ex)...)
		}
	}
	for m := range methods {
		for _, in := range inputs {
			rt, _, err := safeMatch(r.f, m, in)
			if err == nil {
				name := "<nil>"
				if rt != nil {
					name = rt.Name
				}
				r.failf("Match(%s, %s) resolves to route %s", m, show(in), name)
			}
		}
	}
}

// safeMatch calls Match and names the input of a panic: a format that panics on
// hostile input is as broken as one that resolves it.
func safeMatch(f formats.Format, method, p string) (rt *formats.Route, params formats.Params, err error) {
	defer func() {
		if x := recover(); x != nil {
			panic(fmt.Sprintf("Match(%s, %s) panics: %v", method, show(p), x))
		}
	}()
	return f.Match(method, p)
}

func show(s string) string {
	if len(s) > 60 {
		return fmt.Sprintf("%q... (%d bytes)", s[:40], len(s))
	}
	return fmt.Sprintf("%q", s)
}

// classify is check 3: Classify says a kind for every example and for the fixture's
// paths, and a metadata route is never content.
func (r *run) classify() {
	n := 0
	for _, rt := range r.f.Routes() {
		for _, ex := range rt.Examples {
			n++
			clean, err := repository.CleanPath(ex)
			if err != nil {
				r.failf("route %q: example %q is no clean path: %v", rt.Name, ex, err)
				continue
			}
			if r.fx.Canonical != nil {
				// The example is a protocol path; Classify is asked about the canonical path
				// it stands for. A route that addresses none (a service index, a search, a
				// write) has no path to classify, and checks 7 and 9 hold it to its kind; a
				// download serves a stored item, so its example must address one.
				canon, ok := r.fx.Canonical(ex)
				if !ok {
					if rt.Kind == formats.Download {
						r.failf("route %q is a download and its example %q addresses no canonical path", rt.Name, ex)
					}
					continue
				}
				if clean, err = repository.CleanPath(canon); err != nil {
					r.failf("route %q: the canonical path %q of example %q is no clean path: %v", rt.Name, canon, ex, err)
					continue
				}
			}
			info := r.f.Classify(clean)
			switch {
			case !validPathKind(info.Kind):
				r.failf("route %q: Classify(%q) has no kind", rt.Name, clean.Path)
			case rt.Kind == formats.Download && info.Kind != formats.Content:
				r.failf("route %q is a download and Classify(%q) says %s, not content", rt.Name, clean.Path, info.Kind)
			case (rt.Kind == formats.Metadata || rt.Kind == formats.Computed) && info.Kind == formats.Content:
				r.failf("route %q serves documents and Classify(%q) says content", rt.Name, clean.Path)
			case rt.Kind == formats.Volatile && info.Kind != formats.PathVolatile:
				r.failf("route %q is volatile and Classify(%q) says %s", rt.Name, clean.Path, info.Kind)
			}
		}
	}
	for _, pc := range r.fx.Paths {
		n++
		clean, err := repository.CleanPath(pc.Path)
		if err != nil {
			r.failf("path case %q is no clean path: %v", pc.Path, err)
			continue
		}
		if got := r.f.Classify(clean); got != pc.Want {
			r.failf("Classify(%q) = %+v, want %+v", clean.Path, got, pc.Want)
		}
	}
	if n == 0 {
		r.failf("%s: the format has no route example and the fixture no Paths, and the check is not skipped", CheckClassify)
	}
}

func validPathKind(k formats.PathKind) bool { return k >= formats.Content && k <= formats.PathVolatile }

// rewrite is check 4: Rewrite is deterministic and idempotent, and no upstream
// host remains in a download link.
func (r *run) rewrite() {
	rm := r.f.Remote()
	fx := r.fx.Remote
	switch {
	case rm == nil:
		r.failf("the format has no remote adapter and the check is not skipped")
		return
	case fx == nil || len(fx.Docs) == 0 || fx.DownloadLinks == nil:
		r.failf("the format has a remote adapter and the fixture has no Remote documents with DownloadLinks")
		return
	}
	up, err := url.Parse(fx.Upstream)
	if err != nil || up.Host == "" {
		r.failf("Remote.Upstream %q is no origin", fx.Upstream)
		return
	}
	h := r.host(formats.Remote)
	rc := formats.RemoteContext{Repo: h.Repo(), Upstream: up, Settings: fx.Settings}
	ln := Links("", r.d.ID, h.Repo().Key)
	ctx := context.Background()
	meaningful := false
	for _, d := range fx.Docs {
		clean, err := repository.CleanPath(d.Path)
		if err != nil {
			r.failf("document %q: %q is no clean path: %v", d.Name, d.Path, err)
			continue
		}
		in := append([]byte(nil), d.Body...)
		for _, l := range fx.DownloadLinks(in) {
			if sameHost(l, up) {
				meaningful = true
			}
		}
		out1, err1 := rm.Rewrite(ctx, rc, clean, in, ln)
		out2, err2 := rm.Rewrite(ctx, rc, clean, in, ln)
		switch {
		case err1 != nil || err2 != nil:
			r.failf("document %q: Rewrite fails: %v %v", d.Name, err1, err2)
			continue
		case !bytes.Equal(in, d.Body):
			r.failf("document %q: Rewrite changed its input", d.Name)
		case !bytes.Equal(out1, out2):
			r.failf("document %q: Rewrite is not deterministic: two runs on the same input differ", d.Name)
		}
		out3, err3 := rm.Rewrite(ctx, rc, clean, out1, ln)
		if err3 != nil {
			r.failf("document %q: Rewrite of a rewritten document fails: %v", d.Name, err3)
		} else if !bytes.Equal(out1, out3) {
			r.failf("document %q: Rewrite is not idempotent: rewriting its own output changes it", d.Name)
		}
		for _, l := range fx.DownloadLinks(out1) {
			if sameHost(l, up) {
				r.failf("document %q: the rewritten document still links to the upstream: %s", d.Name, l)
			}
		}
	}
	if !meaningful {
		r.failf("no document of the Remote fixture has a download link to %s, so the check proves nothing", fx.Upstream)
	}
}

func sameHost(link string, up *url.URL) bool {
	u, err := url.Parse(link)
	return err == nil && strings.EqualFold(u.Host, up.Host)
}

// merge is check 5: Merge is deterministic and a version entry comes from the
// lowest (tier, position) member that holds it.
func (r *run) merge() {
	va := r.f.Virtual()
	fx := r.fx.Merge
	switch {
	case va == nil:
		r.failf("the format has no virtual adapter and the check is not skipped")
		return
	case fx == nil || len(fx.Cases) == 0 || fx.Versions == nil || fx.VersionSource == nil:
		r.failf("the format has a virtual adapter and the fixture has no Merge cases with Versions and VersionSource")
		return
	}
	h := r.host(formats.Virtual)
	for _, c := range fx.Cases {
		if len(c.Sources) != len(c.Parts) || len(c.Parts) < 2 {
			r.failf("merge case %q needs at least two parts and one source marker per part", c.Name)
			continue
		}
		clean, err := repository.CleanPath(c.Path)
		if err != nil {
			r.failf("merge case %q: %q is no clean path: %v", c.Name, c.Path, err)
			continue
		}
		rq := h.bareRequest(context.Background(), http.MethodGet, c.Path)
		out1, err1 := va.Merge(rq.Context(), rq, clean, c.Parts)
		out2, err2 := va.Merge(rq.Context(), rq, clean, c.Parts)
		switch {
		case err1 != nil || err2 != nil:
			r.failf("merge case %q: Merge fails: %v %v", c.Name, err1, err2)
			continue
		case !bytes.Equal(out1, out2):
			r.failf("merge case %q: Merge is not deterministic", c.Name)
		}
		held := map[string]int{} // version -> index of the lowest part that holds it
		for i, p := range c.Parts {
			for _, v := range fx.Versions(p.Doc) {
				if j, ok := held[v]; !ok || less(p, c.Parts[j]) {
					held[v] = i
				}
			}
		}
		if len(held) == 0 {
			r.failf("merge case %q: no member document holds a version", c.Name)
		}
		for v, i := range held {
			if got := fx.VersionSource(out1, v); got != c.Sources[i] {
				r.failf("merge case %q: version %s came from %q, the per-version source rule picks %q (tier %d, position %d)",
					c.Name, v, got, c.Sources[i], c.Parts[i].Tier, c.Parts[i].Position)
			}
		}
	}
}

func less(a, b formats.MemberDocument) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	return a.Position < b.Position
}

// inspect is check 6: every crafted input is rejected with 413 or 422 within
// five seconds, and the heap stays within 512 MiB of its baseline. A format whose
// remote adapter inspects fills runs the inputs its hook takes through it as well,
// and one whose remote adapter follows a discovery document runs crafted discovery
// documents through its parser (discovery).
func (r *run) inspect() {
	disc := formats.CapabilitiesOf(r.f).Remote.Discovery
	if len(r.fx.Inspect) == 0 && (disc == nil || r.hasRoute(formats.Upload)) {
		r.failf("the fixture has no crafted Inspect case and the check is not skipped")
		return
	}
	lim := untrusted.DefaultLimits().Tighten(r.d.Limits)
	if r.d.MaxDocumentBytes > 0 {
		lim = lim.Tighten(untrusted.Limits{DocumentBytes: r.d.MaxDocumentBytes})
	}
	for _, c := range r.fx.Inspect {
		clean, err := repository.CleanPath(c.Path)
		if err != nil {
			r.failf("inspect case %q: %q is no clean path: %v", c.Name, c.Path, err)
			continue
		}
		in := formats.InspectInput{Path: clean, MediaType: c.MediaType, Body: inputSpool{c.Input}, Limits: lim}
		m := measure(func(ctx context.Context) error {
			_, e := r.f.Inspect(ctx, in)
			return e
		}, lim)
		switch e, ok := errs.As(m.err); {
		case m.elapsed > timeBound():
			r.failf("inspect case %q: Inspect took %v, the bound is %v", c.Name, m.elapsed.Round(time.Millisecond), timeBound())
		case m.err == nil:
			r.failf("inspect case %q: Inspect accepted the input", c.Name)
		case !ok || (e.Kind != errs.TooLarge && e.Kind != errs.Unprocessable):
			r.failf("inspect case %q: Inspect failed with %v, want 413 or 422", c.Name, m.err)
		}
		if m.peak > heapBudget {
			r.failf("inspect case %q: the heap grew by %d MiB, the bound is %d MiB", c.Name, m.peak>>20, heapBudget>>20)
		}
		if fill := formats.CapabilitiesOf(r.f).Remote.Fill; fill != nil && fill.InspectsFill(clean) {
			r.inspectFill(fill, c.Name, in, lim)
		}
	}
	if disc != nil {
		r.discovery(disc, lim)
	}
}

// inspectFill holds the fill hook of a remote adapter (formats.FillInspector) to the bounds of
// R-ARCH-028 on the same crafted input: it reads what an upstream sent, which is as untrusted as an
// upload (Q-ARCH-267). What it answers is the format's - NuGet refuses most archives it cannot
// read and serves content that is no package as it came (Q-NUGET-84) - so the check asks that it
// returns in time, within the heap budget and without a panic (Q-ARCH-281).
func (r *run) inspectFill(fill formats.FillInspector, name string, in formats.InspectInput, lim untrusted.Limits) {
	rc := formats.RemoteContext{Repo: r.host(formats.Remote).Repo()}
	m := measure(func(ctx context.Context) error {
		_, e := fill.InspectFill(ctx, rc, in.Path, in.Body)
		return e
	}, lim)
	switch {
	case m.elapsed > timeBound():
		r.failf("inspect case %q: InspectFill took %v, the bound is %v", name, m.elapsed.Round(time.Millisecond), timeBound())
	case errors.Is(m.err, errPanicked):
		r.failf("inspect case %q: InspectFill %s", name, strings.TrimPrefix(m.err.Error(), "inspect "))
	}
	if m.peak > heapBudget {
		r.failf("inspect case %q: InspectFill grew the heap by %d MiB, the bound is %d MiB", name, m.peak>>20, heapBudget>>20)
	}
}

// measurement is what one run of Inspect cost.
type measurement struct {
	err     error
	elapsed time.Duration
	// peak is how far the heap rose above its baseline.
	peak uint64
}

// errPanicked marks the error of a measured call that panicked.
var errPanicked = errors.New("panicked")

// measure runs one reading of a crafted input - Inspect, or the fill hook of a remote
// adapter - under the input's limits. A run that is not done after the deadline is
// cancelled and reported with the time it took until it stopped.
func measure(call func(ctx context.Context) error, lim untrusted.Limits) measurement {
	runtime.GC()
	base := heapBytes()
	ctx, cancel := lim.Context(context.Background())
	defer cancel()
	stop := make(chan struct{})
	var top uint64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			if h := heapBytes(); h > top {
				top = h
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("inspect %w: %v", errPanicked, p)
			}
		}()
		done <- call(ctx)
	}()
	var m measurement
	select {
	case m.err = <-done:
	case <-time.After(timeBound()):
		cancel()
		select {
		case m.err = <-done:
		case <-time.After(time.Second):
			m.err = errors.New("inspect did not return after its context was cancelled")
		}
		if m.err == nil {
			m.err = context.Canceled
		}
	}
	m.elapsed = time.Since(start)
	close(stop)
	wg.Wait()
	if top > base {
		m.peak = top - base
	}
	return m
}

func heapBytes() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// contentHeaders are the header names the wrapper owns; the kit's handler wrapper
// deletes them to prove the core puts them back.
func contentHeaders() []string {
	return []string{"X-Content-Type-Options", "Content-Security-Policy", "Cross-Origin-Resource-Policy", "Content-Disposition"}
}

// stripper deletes the content headers right before the status line and the
// first byte, the way a handler that tries to weaken them would.
type stripper struct{ http.ResponseWriter }

func (s stripper) strip() {
	for _, h := range contentHeaders() {
		s.Header().Del(h)
	}
}

func (s stripper) WriteHeader(code int) { s.strip(); s.ResponseWriter.WriteHeader(code) }
func (s stripper) Write(p []byte) (int, error) {
	s.strip()
	return s.ResponseWriter.Write(p)
}
func (s stripper) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// headers is check 7: the responses of Download, Metadata, Computed and Volatile
// routes carry the content headers even when the handler deletes them, and no other
// route returns package content. A Volatile route (a search) may return
// package-derived bytes under the headers of Metadata (Q-ARCH-258); it serves no
// stored item.
func (r *run) headers() {
	routes := r.f.Routes()
	if len(routes) == 0 {
		r.failf("%s: the format has no routes and the check is not skipped (Skip[%d] needs a reason)", CheckHeaders, CheckHeaders)
		return
	}
	// served are the routes a content case reached, checked those whose headers a case
	// checked: a case that expects a refusal checks none (an error is no content).
	served, checked := map[string]bool{}, map[string]bool{}
	for _, c := range r.fx.Content {
		for _, sameOrigin := range []bool{false, true} {
			h := r.host("")
			if c.Seed != nil {
				if err := c.Seed(h); err != nil {
					r.failf("content case %q: the seed fails: %v", c.Name, err)
					break
				}
			}
			method := c.Method
			if method == "" {
				method = http.MethodGet
			}
			var body *bytes.Reader
			if c.Body != nil {
				body = bytes.NewReader(c.Body)
			} else {
				body = bytes.NewReader(nil)
			}
			rq, err := h.NewRequest(context.Background(), method, c.Path, body)
			if err != nil {
				r.failf("content case %q: %v", c.Name, err)
				break
			}
			served[rq.Route.Name] = true
			if rq.Route.Kind.WrapsContent() {
				rt := *rq.Route
				orig := rt.Handler
				rt.Handler = func(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
					s := stripper{w}
					s.strip()
					return orig(ctx, rq, s)
				}
				rq.Route = &rt
			}
			resp := h.ServeRequest(rq, sameOrigin)
			r.checkContent(c, rq, resp, sameOrigin)
			if c.Status < http.StatusBadRequest {
				checked[rq.Route.Name] = true
			}
			for _, v := range h.Violations() {
				r.failf("content case %q: %s", c.Name, v)
			}
		}
	}
	for _, rt := range routes {
		if !rt.Kind.WrapsContent() {
			continue
		}
		reason, listed := r.fx.NoContent[rt.Name]
		switch {
		case !served[rt.Name]:
			r.failf("route %q (%s) has no content case", rt.Name, rt.Kind)
		case listed && reason == "":
			r.failf("route %q is listed in NoContent without a reason", rt.Name)
		case listed && checked[rt.Name]:
			r.failf("route %q is listed in NoContent and a content case serves it: its headers are checked, the entry goes", rt.Name)
		case !listed && !checked[rt.Name]:
			r.failf("route %q (%s) has only content cases that refuse, so its content headers are never checked: "+
				"give it a case it serves, or list it in NoContent with the reason", rt.Name, rt.Kind)
		}
	}
	for n := range r.fx.NoContent {
		if !slices.ContainsFunc(routes, func(rt formats.Route) bool { return rt.Name == n && rt.Kind.WrapsContent() }) {
			r.failf("NoContent names %q, which is no Download, Metadata, Computed or Volatile route of the format", n)
		}
	}
}

func (r *run) checkContent(c ContentCase, rq *formats.Request, resp *Response, sameOrigin bool) {
	want := c.Status
	if want == 0 {
		want = http.StatusOK
	}
	if resp.Code != want {
		r.failf("content case %q: status %d, want %d (%s)", c.Name, resp.Code, want, snippet(resp.Body.String()))
		return
	}
	if !rq.Route.Kind.WrapsContent() || resp.Code >= 400 {
		return
	}
	kind := content.Document
	if rq.Route.Kind == formats.Download {
		kind = content.File
	}
	// what was sent is the header map at the status line, not the map a handler can
	// still edit afterwards
	sent := resp.Result().Header
	wantH := content.Headers(kind, path.Base(rq.Path.Path), sameOrigin)
	for k, v := range wantH {
		if !reflect.DeepEqual(sent[k], v) {
			r.failf("content case %q (route %q, same-origin %v): header %s is %q, want %q", c.Name, rq.Route.Name, sameOrigin, k, sent[k], v)
		}
	}
	if kind == content.Document && sent.Get("Content-Disposition") != "" {
		r.failf("content case %q: route %q serves a document and sets Content-Disposition", c.Name, rq.Route.Name)
	}
}

func snippet(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

var tokenText = regexp.MustCompile(`(?i)token|password|_auth`)

// setup is check 8: every client has a configure and an install template; every
// template parses and executes over SetupData, which has no credential field, and
// none spells a credential; and the format's guide covers every class and client
// under the same credential rule (guide).
func (r *run) setup() {
	tpls := r.f.SetupTemplates()
	if len(r.d.Clients) == 0 && len(tpls) == 0 && len(r.f.Guide()) == 0 {
		r.failf("the format has no clients and the check is not skipped")
		return
	}
	defer r.guide(tpls)
	known := map[string]bool{}
	for _, c := range r.d.Clients {
		known[c.ID] = true
		var configure, install bool
		for _, t := range tpls {
			if t.Client == c.ID {
				configure = configure || t.Section == "configure"
				install = install || t.Section == "install"
			}
		}
		if !configure {
			r.failf("client %q has no configure template", c.ID)
		}
		if !install {
			r.failf("client %q has no install template", c.ID)
		}
	}
	ids := map[string]bool{}
	data := formats.SetupData{
		RegistryURL: DefaultBaseURL + "/api/" + string(r.d.ID) + "/main/", Repository: "main", Endpoint: "default", Variant: "scope", Username: "alice",
	}
	for _, t := range tpls {
		switch {
		case t.ID == "":
			r.failf("a setup template of client %q has no ID", t.Client)
			continue
		case ids[t.ID]:
			r.failf("setup template ID %q is used twice", t.ID)
		case !known[t.Client]:
			r.failf("setup template %q belongs to client %q, which the descriptor does not list", t.ID, t.Client)
		case t.Section != "configure" && t.Section != "publish" && t.Section != "install":
			r.failf("setup template %q has section %q", t.ID, t.Section)
		case !slices.Contains([]string{"shell", "powershell", "cmd", "ini", "xml", "yaml"}, t.Lang):
			r.failf("setup template %q has language %q", t.ID, t.Lang)
		}
		r.versionSpec(t)
		ids[t.ID] = true
		// The API gives a template the quoting functions shell, powershell, cmd and xml
		// (R-API-030), and schemeless; the check only needs them to exist, the quoting is the API's and
		// tested there.
		quote := func(v string) string { return v }
		tp, err := template.New(t.ID).Option("missingkey=error").Funcs(template.FuncMap{"shell": quote, "powershell": quote, "cmd": quote, "xml": quote, "schemeless": quote}).Parse(t.Template)
		if err != nil {
			r.failf("setup template %q does not parse: %v", t.ID, err)
			continue
		}
		if err := tp.Execute(&bytes.Buffer{}, data); err != nil {
			r.failf("setup template %q does not execute over SetupData: %v", t.ID, err)
		}
		text := t.Template
		// The references to the token are not credentials, in any of their spellings.
		for _, ref := range formats.SetupTokenRefs() {
			text = strings.ReplaceAll(text, ref, "")
		}
		for _, o := range r.fx.OptionNames {
			text = strings.ReplaceAll(text, o, "")
		}
		if m := tokenText.FindString(text); m != "" {
			r.failf("setup template %q spells a credential (%q): a template never receives a token, the user supplies it", t.ID, m)
		}
	}
}

// versionSpec is the rule of SetupTemplate.VersionSpec: only an install template that
// places the package has one, and it places the version.
func (r *run) versionSpec(t formats.SetupTemplate) {
	switch {
	case t.VersionSpec == "":
	case t.Section != "install":
		r.failf("setup template %q has a VersionSpec and is not an install template", t.ID)
	case !strings.Contains(t.Template, formats.SetupPackageRef):
		r.failf("setup template %q has a VersionSpec and does not place %s", t.ID, formats.SetupPackageRef)
	case !strings.Contains(t.VersionSpec, formats.SetupVersionRef):
		r.failf("setup template %q has a VersionSpec that does not place %s", t.ID, formats.SetupVersionRef)
	}
}

// revision is check 9: a change leaves the affected package's rev_seq higher.
func (r *run) revision() {
	if len(r.fx.Changes) == 0 && !r.hasWriteRoutes() {
		r.failf("the fixture has no Change and the check is not skipped")
		return
	}
	covered := map[string]bool{}
	for _, c := range r.fx.Changes {
		if c.Package == "" || c.Do == nil {
			r.failf("change %q needs a Package and a Do", c.Name)
			continue
		}
		h := r.host("")
		if c.Seed != nil {
			if err := c.Seed(h); err != nil {
				r.failf("change %q: the seed fails: %v", c.Name, err)
				continue
			}
		}
		before := h.RevSeq(c.Package)
		h.mu.Lock()
		h.routes, h.hooks = nil, nil
		h.mu.Unlock()
		if err := c.Do(context.Background(), h); err != nil {
			r.failf("change %q fails: %v", c.Name, err)
			continue
		}
		for _, n := range h.RoutesServed() {
			covered[n] = true
		}
		for _, n := range h.HooksRun() {
			covered[n] = true
		}
		if after := h.RevSeq(c.Package); after <= before {
			r.failf("change %q: rev_seq of %q is %d before and %d after; a change of package rows must raise it (R-ARCH-009)", c.Name, c.Package, before, after)
		}
		for _, v := range h.Violations() {
			r.failf("change %q: %s", c.Name, v)
		}
	}
	names := map[string]bool{}
	for _, rt := range r.f.Routes() {
		names[rt.Name] = true
		switch rt.Kind {
		case formats.Upload, formats.Delete, formats.Admin:
			if reason, ok := r.fx.NoRevision[rt.Name]; ok {
				if reason == "" {
					r.failf("route %q is listed in NoRevision without a reason", rt.Name)
				}
				continue
			}
			if !covered[rt.Name] {
				r.failf("route %q (%s) has no change case and is not listed in NoRevision", rt.Name, rt.Kind)
			}
		}
	}
	hooks := r.hooks()
	for _, hook := range hooks {
		if reason, ok := r.fx.NoRevision[hook]; ok {
			if reason == "" {
				r.failf("the storage hook %s is listed in NoRevision without a reason", hook)
			}
			continue
		}
		if !covered[hook] {
			r.failf("the storage hook %s ran in no change case: check 9 needs a change that drives it, or a NoRevision entry with the reason", hook)
		}
	}
	for n := range r.fx.NoRevision {
		if !names[n] && !slices.Contains(hooks, n) {
			r.failf("NoRevision names the route or hook %q, which the format does not have", n)
		}
	}
}

// hooks are the storage hooks check 9 drives: the commit and the removal hook of a format
// with package rows, and the restore hook of a format that has one (formats.ItemsRestorer).
func (r *run) hooks() []string {
	var out []string
	if r.d.PackageNames {
		out = append(out, "OnCommit", "OnItemsRemoved")
	}
	if formats.CapabilitiesOf(r.f).ItemsRestorer != nil {
		out = append(out, "OnItemsRestored")
	}
	return out
}

// hasRoute reports whether the format has a route of the kind.
func (r *run) hasRoute(kind formats.RouteKind) bool {
	for _, rt := range r.f.Routes() {
		if rt.Kind == kind {
			return true
		}
	}
	return false
}

func (r *run) hasWriteRoutes() bool {
	for _, rt := range r.f.Routes() {
		switch rt.Kind {
		case formats.Upload, formats.Delete, formats.Admin:
			return true
		}
	}
	return false
}

// names is check 10: the document path of every sample name is a GET route, and
// the spellings of one name lead to one path.
func (r *run) names() {
	if !r.d.PackageNames {
		r.failf("the format has no package names and the check is not skipped")
		return
	}
	if len(r.fx.Names) == 0 {
		r.failf("the fixture has no sample package names")
		return
	}
	for _, nc := range r.fx.Names {
		var norm, esc string
		for i, sp := range nc.Spellings {
			n, ok := r.d.Names.Normalize(sp)
			if !ok {
				r.failf("the sample name %q is no package name of the format", sp)
				continue
			}
			p, ok := r.f.PackageDocumentPath(n)
			if !ok {
				r.failf("PackageDocumentPath(%q) answers no path for a format with package names", n)
				continue
			}
			e := EscapePath(p)
			if r.fx.Protocol != nil {
				e = r.fx.Protocol(e)
			}
			rt, _, err := r.f.Match(http.MethodGet, e)
			switch {
			case err != nil:
				r.failf("the document path %q of %q does not match a route: %v", e, n, err)
			case rt.Method != http.MethodGet:
				r.failf("the document path %q of %q matches route %q, which is not a GET route", e, n, rt.Name)
			}
			if i == 0 {
				norm, esc = n, e
				continue
			}
			if n != norm {
				r.failf("the spellings %q and %q are given as one name and normalize to %q and %q", nc.Spellings[0], sp, norm, n)
			} else if e != esc {
				r.failf("the spellings %q and %q of one name lead to two paths: %q and %q", nc.Spellings[0], sp, esc, e)
			}
		}
		if len(nc.Spellings) == 0 {
			r.failf("a name case has no spellings")
		}
	}
}
