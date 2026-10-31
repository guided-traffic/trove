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
)

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
// five seconds, and the heap stays within 512 MiB of its baseline.
func (r *run) inspect() {
	if len(r.fx.Inspect) == 0 {
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
		m := measure(r.f, in, lim)
		switch e, ok := errs.As(m.err); {
		case m.elapsed > inspectDeadline:
			r.failf("inspect case %q: Inspect took %v, the bound is %v", c.Name, m.elapsed.Round(time.Millisecond), inspectDeadline)
		case m.err == nil:
			r.failf("inspect case %q: Inspect accepted the input", c.Name)
		case !ok || (e.Kind != errs.TooLarge && e.Kind != errs.Unprocessable):
			r.failf("inspect case %q: Inspect failed with %v, want 413 or 422", c.Name, m.err)
		}
		if m.peak > heapBudget {
			r.failf("inspect case %q: the heap grew by %d MiB, the bound is %d MiB", c.Name, m.peak>>20, heapBudget>>20)
		}
	}
}

// measurement is what one run of Inspect cost.
type measurement struct {
	err     error
	elapsed time.Duration
	// peak is how far the heap rose above its baseline.
	peak uint64
}

// measure runs Inspect on the input. A run that is not done after the deadline is
// cancelled and reported with the time it took until it stopped.
func measure(f formats.Format, in formats.InspectInput, lim untrusted.Limits) measurement {
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
				done <- fmt.Errorf("inspect panicked: %v", p)
			}
		}()
		_, e := f.Inspect(ctx, in)
		done <- e
	}()
	var m measurement
	select {
	case m.err = <-done:
	case <-time.After(inspectDeadline):
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

// headers is check 7: the responses of Download, Metadata and Computed routes
// carry the content headers even when the handler deletes them, and no other
// route returns package content.
func (r *run) headers() {
	routes := r.f.Routes()
	if len(routes) == 0 {
		r.failf("%s: the format has no routes and the check is not skipped (Skip[%d] needs a reason)", CheckHeaders, CheckHeaders)
		return
	}
	served := map[string]bool{}
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
			if rq.Route.Kind.ServesContent() {
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
			for _, v := range h.Violations() {
				r.failf("content case %q: %s", c.Name, v)
			}
		}
	}
	for _, rt := range routes {
		switch rt.Kind {
		case formats.Download, formats.Metadata, formats.Computed, formats.Volatile:
			if !served[rt.Name] {
				r.failf("route %q (%s) has no content case", rt.Name, rt.Kind)
			}
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
	if !rq.Route.Kind.ServesContent() || resp.Code >= 400 {
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
		RegistryURL: DefaultBaseURL + "/api/" + string(r.d.ID) + "/main/", Repository: "main", Endpoint: "default", Scope: "@scope", Username: "alice",
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
		case t.Lang != "shell" && t.Lang != "ini" && t.Lang != "xml" && t.Lang != "yaml":
			r.failf("setup template %q has language %q", t.ID, t.Lang)
		}
		ids[t.ID] = true
		tp, err := template.New(t.ID).Option("missingkey=error").Parse(t.Template)
		if err != nil {
			r.failf("setup template %q does not parse: %v", t.ID, err)
			continue
		}
		if err := tp.Execute(&bytes.Buffer{}, data); err != nil {
			r.failf("setup template %q does not execute over SetupData: %v", t.ID, err)
		}
		text := t.Template
		for _, o := range r.fx.OptionNames {
			text = strings.ReplaceAll(text, o, "")
		}
		if m := tokenText.FindString(text); m != "" {
			r.failf("setup template %q spells a credential (%q): a template never receives a token, the user supplies it", t.ID, m)
		}
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
		h.routes = nil
		h.mu.Unlock()
		if err := c.Do(context.Background(), h); err != nil {
			r.failf("change %q fails: %v", c.Name, err)
			continue
		}
		for _, n := range h.RoutesServed() {
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
	for n := range r.fx.NoRevision {
		if !names[n] {
			r.failf("NoRevision names the route %q, which the format does not have", n)
		}
	}
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
