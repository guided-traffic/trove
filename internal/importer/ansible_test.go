package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

// galaxyVersion is one version of a collection in the fake source.
type galaxyVersion struct {
	body      []byte
	announced string // the sha256 the version document announces; the digest of body when empty and not noDigest
	noDigest  bool
	created   string
}

type galaxyCollection struct {
	versions   map[string]galaxyVersion
	deprecated bool
}

// fakeGalaxy is a source: the v3 documents of a Galaxy with a list that pages through relative links.
type fakeGalaxy struct {
	srv         *httptest.Server
	mu          sync.Mutex
	collections map[string]*galaxyCollection // "ns.name"
	roles       int
	pageSize    int
	auth        []string
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func newFakeGalaxy(t *testing.T) *fakeGalaxy {
	t.Helper()
	g := &fakeGalaxy{collections: map[string]*galaxyCollection{}, pageSize: 2}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGalaxy) names() []string {
	var out []string
	for n := range g.collections {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// page cuts items by limit and offset the way the Galaxy does and gives the next link relative to the host.
func (g *fakeGalaxy) page(w http.ResponseWriter, r *http.Request, items []any) {
	off := 0
	_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &off)
	end := min(off+g.pageSize, len(items))
	var next any
	if end < len(items) {
		q := url.Values{"limit": {fmt.Sprint(g.pageSize)}, "offset": {fmt.Sprint(end)}}
		next = r.URL.Path + "?" + q.Encode()
	}
	writeJSON(w, map[string]any{"meta": map[string]any{"count": len(items)}, "links": map[string]any{"next": next}, "data": items[min(off, len(items)):end]})
}

func (g *fakeGalaxy) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	g.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch {
	case path == "v1/roles/":
		writeJSON(w, map[string]any{"count": g.roles})
	case path == "v3/collections/":
		var items []any
		for _, n := range g.names() {
			ns, name, _ := strings.Cut(n, ".")
			items = append(items, map[string]any{"namespace": ns, "name": name})
		}
		g.page(w, r, items)
	case strings.HasPrefix(path, "v3/artifacts/"):
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(path, "v3/artifacts/collections/"), ".tar.gz"), "-")
		if c := g.collections[parts[0]+"."+parts[1]]; c != nil {
			if v, ok := c.versions[parts[2]]; ok {
				_, _ = w.Write(v.body)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(path, "v3/collections/"):
		g.collection(w, r, strings.Split(strings.Trim(strings.TrimPrefix(path, "v3/collections/"), "/"), "/"))
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGalaxy) collection(w http.ResponseWriter, r *http.Request, seg []string) {
	if len(seg) < 2 {
		http.NotFound(w, r)
		return
	}
	c := g.collections[seg[0]+"."+seg[1]]
	if c == nil {
		http.NotFound(w, r)
		return
	}
	switch len(seg) {
	case 2:
		writeJSON(w, map[string]any{"namespace": seg[0], "name": seg[1], "deprecated": c.deprecated})
	case 3:
		var vs []string
		for v := range c.versions {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		var items []any
		for _, v := range vs {
			items = append(items, map[string]any{"version": v, "created_at": c.versions[v].created})
		}
		g.page(w, r, items)
	case 4:
		v, ok := c.versions[seg[3]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		art := map[string]any{"filename": strings.Join([]string{seg[0], seg[1], seg[3]}, "-") + ".tar.gz", "size": len(v.body)}
		if !v.noDigest {
			art["sha256"] = v.announced
			if v.announced == "" {
				art["sha256"] = sum(v.body)
			}
		}
		writeJSON(w, map[string]any{
			"version": seg[3], "created_at": v.created, "artifact": art,
			"download_url": g.srv.URL + "/api/v3/artifacts/collections/" + seg[0] + "-" + seg[1] + "-" + seg[3] + ".tar.gz",
		})
	default:
		http.NotFound(w, r)
	}
}

type published struct {
	name, version string
	declared      string // the sha256 part
	body          []byte
	auth          string
}

// fakeTarget is a trove Ansible repository: the discovery document, the version documents, the publish and the task.
type fakeTarget struct {
	srv         *httptest.Server
	mu          sync.Mutex
	description string
	stored      map[string][]byte // "ns.name/version"
	published   []published
	failTask    map[string]string // collection -> error code of the task
	refuse      map[string]int    // collection -> status of the refusal of the publish
	taskPolls   int
	tasks       map[string]string // id -> failure code, "" for completed
}

func newFakeTarget(t *testing.T) *fakeTarget {
	t.Helper()
	f := &fakeTarget{description: "trove Ansible Galaxy API", stored: map[string][]byte{}, failTask: map[string]string{}, refuse: map[string]int{}, tasks: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

const targetRoot = "/api/ansible/web/local/"

func (f *fakeTarget) apiError(w http.ResponseWriter, status int, code, detail string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"errors": []map[string]any{{"status": fmt.Sprint(status), "code": code, "detail": detail}}})
}

func (f *fakeTarget) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rel := strings.TrimPrefix(r.URL.Path, targetRoot)
	switch {
	case r.URL.Path == strings.TrimSuffix(targetRoot, "/") || rel == "":
		writeJSON(w, map[string]any{"available_versions": map[string]string{"v3": "v3/"}, "current_version": "v3", "description": f.description})
	case r.Method == http.MethodPost && rel == "v3/artifacts/collections/":
		f.publish(w, r)
	case strings.HasPrefix(rel, "v3/imports/collections/"):
		f.taskPolls++
		id := strings.Trim(strings.TrimPrefix(rel, "v3/imports/collections/"), "/")
		code, ok := f.tasks[id]
		if !ok {
			f.apiError(w, http.StatusNotFound, "not_found", "no task")
			return
		}
		doc := map[string]any{"id": id, "state": "completed", "finished_at": "2026-01-01T00:00:00Z", "error": nil}
		if code != "" {
			doc["state"], doc["error"] = "failed", map[string]any{"code": code, "description": "the archive was refused: " + code}
		}
		writeJSON(w, doc)
	case strings.HasPrefix(rel, "v3/collections/"):
		f.read(w, r, strings.Split(strings.Trim(strings.TrimPrefix(rel, "v3/collections/"), "/"), "/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTarget) read(w http.ResponseWriter, r *http.Request, seg []string) {
	if len(seg) < 3 {
		http.NotFound(w, r)
		return
	}
	prefix := seg[0] + "." + seg[1] + "/"
	var vs []string
	for k := range f.stored {
		if strings.HasPrefix(k, prefix) {
			vs = append(vs, strings.TrimPrefix(k, prefix))
		}
	}
	if len(vs) == 0 {
		f.apiError(w, http.StatusNotFound, "not_found", "no such collection")
		return
	}
	sort.Strings(vs)
	if len(seg) == 3 {
		var items []any
		for _, v := range vs {
			items = append(items, map[string]any{"version": v})
		}
		writeJSON(w, map[string]any{"meta": map[string]any{"count": len(items)}, "links": map[string]any{"next": nil}, "data": items})
		return
	}
	body, ok := f.stored[prefix+seg[3]]
	if !ok {
		f.apiError(w, http.StatusNotFound, "not_found", "no such version")
		return
	}
	writeJSON(w, map[string]any{"version": seg[3], "artifact": map[string]any{"sha256": sum(body)}})
}

func (f *fakeTarget) publish(w http.ResponseWriter, r *http.Request) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		f.apiError(w, http.StatusBadRequest, "invalid_request", "not multipart")
		return
	}
	var p published
	p.auth = r.Header.Get("Authorization")
	var fileName string
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		switch part.FormName() {
		case "sha256":
			p.declared = string(b)
		case "file":
			p.body, fileName = b, part.FileName()
		}
	}
	base := strings.TrimSuffix(fileName, ".tar.gz")
	parts := strings.Split(base, "-")
	if len(parts) != 3 {
		f.apiError(w, http.StatusUnprocessableEntity, "invalid_archive", "bad file name "+fileName)
		return
	}
	p.name, p.version = parts[0]+"."+parts[1], parts[2]
	if p.declared != sum(p.body) {
		f.apiError(w, http.StatusBadRequest, "sha256_mismatch", "the sha256 part does not match")
		return
	}
	if st := f.refuse[p.name]; st != 0 {
		f.apiError(w, st, map[int]string{403: "permission_denied", 409: "version_exists"}[st], "refused")
		return
	}
	id := fmt.Sprintf("task-%d", len(f.published)+1)
	f.tasks[id] = f.failTask[p.name]
	if f.failTask[p.name] == "" {
		f.stored[p.name+"/"+p.version] = p.body
	}
	f.published = append(f.published, p)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"task": f.srv.URL + targetRoot + "v3/imports/collections/" + id + "/"})
}

func ansibleRun(t *testing.T, g *fakeGalaxy, f *fakeTarget, mutate func(*Options)) (*Report, error) {
	t.Helper()
	format, src, dst, err := Build(factory(), Params{Format: "ansible", From: g.srv.URL + "/api/", To: f.srv.URL + targetRoot, Token: "trv_secret"})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Format: format, Source: src, Target: dst, Names: []string{"acme.tools"}, Dir: t.TempDir(), Concurrency: 1}
	if mutate != nil {
		mutate(&o)
	}
	return Run(context.Background(), o)
}

func archive(s string) []byte { return []byte("archive:" + s) }

func versionOutcomes(p PackageResult) map[string]string {
	out := map[string]string{}
	for _, v := range p.Versions {
		out[v.Version] = v.Outcome
		if v.Code != "" {
			out[v.Version] += ":" + v.Code
		}
	}
	return out
}

func TestR_ANS_027_ValidNameIsTheGrammarOfACollectionNameAndFoldsUpperCase(t *testing.T) {
	a := &Ansible{}
	if a.ID() != "ansible" {
		t.Fatalf("id %q", a.ID())
	}
	for name, want := range map[string]bool{
		"acme.tools": true, "Community.General": true, "a1.b_2": true,
		"a.b.c": false, "_a.b": false, "1.2": false, "acme": false, "acme.": false, ".tools": false, "ac-me.tools": false,
		strings.Repeat("a", 65) + ".b": false, strings.Repeat("a", 64) + ".b": true, "acme.tools/../x": false, "": false,
	} {
		if got := a.ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
	if f, err := FormatByID("ansible"); err != nil || f.ID() != "ansible" {
		t.Errorf("FormatByID: %v", err)
	}
}

func TestR_ANS_027_ASourceIsImportedInTheOrderOfPublicationAndASecondRunSkipsEveryVersion(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{
		"1.0.0": {body: archive("1.0.0"), created: "2025-01-02T00:00:00Z"},
		"1.1.0": {body: archive("1.1.0"), created: "2025-01-01T00:00:00Z"}, // published before 1.0.0
		"2.0.0": {body: archive("2.0.0"), created: "2025-03-01T00:00:00Z"},
	}}
	f := newFakeTarget(t)
	rep, err := ansibleRun(t, g, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := pkgOf(t, rep, "acme.tools")
	want := map[string]string{"1.0.0": OutcomeImported, "1.1.0": OutcomeImported, "2.0.0": OutcomeImported}
	if got := versionOutcomes(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	var order []string
	for _, pub := range f.published {
		order = append(order, pub.version)
		if pub.auth != "Bearer trv_secret" {
			t.Errorf("publish carried Authorization %q", pub.auth)
		}
		if pub.declared != sum(pub.body) || !strings.HasPrefix(string(pub.body), "archive:") {
			t.Errorf("publish of %s: declared %s, body %q", pub.version, pub.declared, pub.body)
		}
	}
	if want := []string{"1.1.0", "1.0.0", "2.0.0"}; !reflect.DeepEqual(order, want) {
		t.Errorf("published in the order %v, want the order of the source's publication times %v", order, want)
	}
	for v, body := range map[string][]byte{"1.0.0": archive("1.0.0"), "2.0.0": archive("2.0.0")} {
		if string(f.stored["acme.tools/"+v]) != string(body) {
			t.Errorf("the target stores other bytes for %s", v)
		}
	}
	if !rep.OK() {
		t.Error("the report of a clean run is not OK")
	}
	// The second run compares the digest of the target's version document with the source's: nothing is read or sent.
	before := len(f.published)
	rep, err = ansibleRun(t, g, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := versionOutcomes(pkgOf(t, rep, "acme.tools")); !reflect.DeepEqual(got, map[string]string{"1.0.0": OutcomeSkipped, "1.1.0": OutcomeSkipped, "2.0.0": OutcomeSkipped}) {
		t.Errorf("second run: %v", got)
	}
	if len(f.published) != before {
		t.Errorf("the second run published %d more", len(f.published)-before)
	}
}

func TestR_ANS_027_AVersionWithOtherContentInTheTargetIsAConflictAndNothingIsWritten(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{"1.0.0": {body: archive("source"), created: "2025-01-01T00:00:00Z"}}}
	f := newFakeTarget(t)
	f.stored["acme.tools/1.0.0"] = archive("target")
	rep, err := ansibleRun(t, g, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := versionOutcomes(pkgOf(t, rep, "acme.tools")); got["1.0.0"] != OutcomeConflict+":version_exists" {
		t.Errorf("outcomes %v", got)
	}
	if len(f.published) != 0 || string(f.stored["acme.tools/1.0.0"]) != string(archive("target")) {
		t.Error("the target's version was touched")
	}
}

func TestR_ANS_027_ABodyThatIsNotTheAnnouncedDigestIsNeverSentAndAMissingDigestIsRefused(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{
		"1.0.0": {body: archive("real"), announced: sum([]byte("something else")), created: "2025-01-01T00:00:00Z"},
		"1.1.0": {body: archive("nodigest"), noDigest: true, created: "2025-01-02T00:00:00Z"},
		"1.2.0": {body: archive("ok"), created: "2025-01-03T00:00:00Z"},
	}}
	f := newFakeTarget(t)
	rep, err := ansibleRun(t, g, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"1.0.0": OutcomeFailed + ":" + CodeDigestMismatch, "1.1.0": OutcomeFailed + ":" + CodeNoDigest, "1.2.0": OutcomeImported}
	if got := versionOutcomes(pkgOf(t, rep, "acme.tools")); !reflect.DeepEqual(got, want) {
		t.Errorf("outcomes %v, want %v", got, want)
	}
	if len(f.published) != 1 || f.published[0].version != "1.2.0" {
		t.Errorf("published %+v", f.published)
	}
	if rep.OK() {
		t.Error("a report with failed versions is OK")
	}
}

func TestR_ANS_027_AFailedImportTaskIsATargetRefusalWithTheTasksCodeAndAForbiddenPublishIsForbidden(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{"1.0.0": {body: archive("a"), created: "2025-01-01T00:00:00Z"}}}
	g.collections["acme.other"] = &galaxyCollection{versions: map[string]galaxyVersion{"1.0.0": {body: archive("b"), created: "2025-01-01T00:00:00Z"}}}
	f := newFakeTarget(t)
	f.failTask["acme.tools"] = "invalid_archive"
	f.refuse["acme.other"] = http.StatusForbidden
	rep, err := ansibleRun(t, g, f, func(o *Options) { o.Names = []string{"acme.tools", "acme.other"} })
	if rep == nil {
		t.Fatalf("no report: %v", err)
	}
	vr := pkgOf(t, rep, "acme.tools").Versions[0]
	if vr.Outcome != OutcomeFailed || vr.Code != CodeTargetRefused || !strings.Contains(vr.Detail, "invalid_archive") {
		t.Errorf("failed task: %+v", vr)
	}
	if other := pkgOf(t, rep, "acme.other"); len(other.Versions) != 1 || other.Versions[0].Code != CodeForbidden {
		t.Errorf("forbidden publish: %+v", other)
	}
	if f.taskPolls == 0 {
		t.Error("the task was not polled")
	}
}

func TestR_ANS_027_ASearchPagesThroughTheIndexAndFiltersOnTheFullName(t *testing.T) {
	g := newFakeGalaxy(t)
	for _, n := range []string{"acme.tools", "acme.web", "other.pkg", "acme.net", "Zed.Thing"} {
		g.collections[n] = &galaxyCollection{}
	}
	_, src, _, err := Build(factory(), Params{Format: "ansible", From: g.srv.URL + "/api", To: "https://t.example/api/ansible/p/r/", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	a := &Ansible{}
	got, err := a.Search(context.Background(), src, "acme.")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"acme.net", "acme.tools", "acme.web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("search %v, want %v", got, want)
	}
	all, err := a.Search(context.Background(), src, "")
	if err != nil || len(all) != 5 {
		t.Errorf("an empty search lists everything over three pages: %v %v", all, err)
	}
	if _, err := a.Search(context.Background(), &SourceClient{HTTP: src.HTTP, Origin: mustURL(t, g.srv.URL+"/nowhere/")}, ""); err == nil {
		t.Error("a source without a collection index was searched")
	}
}

func TestR_ANS_027_APlanFoldsTheNameReportsWhatIsNotKeptAndKnowsAnUnknownCollection(t *testing.T) {
	g := newFakeGalaxy(t)
	g.roles = 7
	g.collections["acme.tools"] = &galaxyCollection{deprecated: true, versions: map[string]galaxyVersion{
		"1.0.0": {body: archive("1"), created: "2025-01-01T00:00:00Z"},
		"1.1.0": {body: archive("2"), created: "2025-01-02T00:00:00Z"},
		"1.2.0": {body: archive("3"), created: "2025-01-03T00:00:00Z"},
	}}
	_, src, _, err := Build(factory(), Params{Format: "ansible", From: g.srv.URL + "/api/", To: "https://t.example/api/ansible/p/r/", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	a := &Ansible{}
	p, err := a.Plan(context.Background(), src, "ACME.Tools")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "acme.tools" || len(p.Versions) != 3 {
		t.Fatalf("plan %+v", p)
	}
	v := p.Versions[0]
	if v.Want.Alg != "sha256" || hex.EncodeToString(v.Want.Sum) != sum(archive("1")) || !strings.HasSuffix(v.URL, "/v3/artifacts/collections/acme-tools-1.0.0.tar.gz") || v.Published.IsZero() {
		t.Errorf("version %+v", v)
	}
	notKept := strings.Join(p.NotKept, "|")
	if !strings.Contains(notKept, "deprecation") || !strings.Contains(notKept, "7 roles") {
		t.Errorf("notKept %q", notKept)
	}
	g.collections["acme.second"] = &galaxyCollection{versions: map[string]galaxyVersion{"1.0.0": {body: archive("s"), created: "2025-01-01T00:00:00Z"}}}
	p2, err := a.Plan(context.Background(), src, "acme.second")
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.NotKept) != 0 {
		t.Errorf("the roles are reported once per run, got %v", p2.NotKept)
	}
	none, err := a.Plan(context.Background(), src, "acme.absent")
	if err != nil || len(none.Versions) != 0 {
		t.Errorf("an unknown collection has no versions: %+v %v", none, err)
	}
	if _, err := a.Plan(context.Background(), src, "bad name"); err == nil {
		t.Error("a name outside the grammar was planned")
	}
}

func TestR_ANS_027_ASourceWhoseDownloadAddressIsForeignIsRefusedAsForeignHost(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{"1.0.0": {body: archive("a"), created: "2025-01-01T00:00:00Z"}}}
	f := newFakeTarget(t)
	rep, err := ansibleRun(t, g, f, func(o *Options) {
		inner := o.Source.HTTP.Transport
		o.Source.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err == nil && strings.Contains(r.URL.Path, "/versions/1.0.0/") {
				b, _ := io.ReadAll(resp.Body)
				resp.Body = io.NopCloser(strings.NewReader(strings.ReplaceAll(string(b), g.srv.URL, "http://elsewhere.example")))
				resp.ContentLength = -1
				resp.Header.Del("Content-Length")
			}
			return resp, err
		})
		_ = inner
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := versionOutcomes(pkgOf(t, rep, "acme.tools")); got["1.0.0"] != OutcomeFailed+":"+CodeForeignHost {
		t.Errorf("outcomes %v", got)
	}
	if len(f.published) != 0 {
		t.Error("something was published")
	}
}

func TestR_ANS_027_PreflightWantsATroveRepositoryThatListsV3(t *testing.T) {
	a := &Ansible{}
	for name, tc := range map[string]struct {
		desc string
		ok   bool
	}{"trove": {"trove Ansible Galaxy API", true}, "another galaxy": {"Galaxy API", false}} {
		f := newFakeTarget(t)
		f.description = tc.desc
		_, _, dst, err := Build(factory(), Params{Format: "ansible", From: "https://s.example/api/", To: f.srv.URL + targetRoot, Token: "t"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Preflight(context.Background(), dst); (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	f := newFakeTarget(t)
	_, _, dst, err := Build(factory(), Params{Format: "ansible", From: "https://s.example/api/", To: f.srv.URL + "/api/ansible/web/nowhere/", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Preflight(context.Background(), dst); err == nil {
		t.Error("a target that answers 404 passed the preflight")
	}
}

func TestR_ANS_027_ADryRunReadsBothSidesAndSendsNothing(t *testing.T) {
	g := newFakeGalaxy(t)
	g.collections["acme.tools"] = &galaxyCollection{versions: map[string]galaxyVersion{
		"1.0.0": {body: archive("a"), created: "2025-01-01T00:00:00Z"},
		"1.1.0": {body: archive("b"), created: "2025-01-02T00:00:00Z"},
	}}
	f := newFakeTarget(t)
	f.stored["acme.tools/1.0.0"] = archive("a")
	rep, err := ansibleRun(t, g, f, func(o *Options) { o.DryRun = true })
	if err != nil {
		t.Fatal(err)
	}
	if got := versionOutcomes(pkgOf(t, rep, "acme.tools")); !reflect.DeepEqual(got, map[string]string{"1.0.0": OutcomeSkipped, "1.1.0": OutcomeWouldAdd}) {
		t.Errorf("outcomes %v", got)
	}
	if len(f.published) != 0 {
		t.Error("a dry run published")
	}
}
