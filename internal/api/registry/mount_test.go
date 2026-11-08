package registry_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/api/dispatch"
	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// R-OCI-006 (the source of a mount needs pull): a registry token without pull on the source gets the answer of an unreadable
// source - the handler sees a request without mount and from and opens a session - and no 401 that would tell the source exists.
func TestAMountWithoutPullOnTheSourceFallsBackToASession(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	src := func(from string, bound qname.Repo) (qname.Repo, string, bool, bool) {
		if !bound.IsZero() {
			return bound, from, false, true
		}
		p := strings.SplitN(from, "/", 3)
		if len(p) != 3 {
			return qname.Repo{}, "", false, false
		}
		key, cache := strings.CutSuffix(p[1], qname.CacheSuffix)
		return qname.Repo{Project: p[0], Key: key}, p[2], cache, true
	}
	var logs bytes.Buffer
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), MountRoute: "oci.upload.start", MountSource: src})
	if err != nil {
		t.Fatal(err)
	}
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	var seen string
	var formParsed bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		// A form marked as parsed holds the stripped values, and the body is never read for it.
		formParsed = r.Form == nil || r.PostForm == nil || r.Form.Has("mount") || r.Form.Has("from")
		w.WriteHeader(http.StatusAccepted)
	})
	router := httpserver.NewClientRouter()
	if err := e.Mount(router, []string{"/v2/{rest...}"}, h, registry.RepositoryRoutes(oci.Routes(), "/v2/web/docker-local/")...); err != nil {
		t.Fatal(err)
	}
	grant := func(entries ...httpserver.RegistryEntry) httpserver.Principal {
		return httpserver.Principal{Kind: httpserver.KindUser, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{Entries: entries}}}
	}
	target := httpserver.RegistryEntry{Repo: local, Name: "app", Push: true}
	sourcePull := httpserver.RegistryEntry{Repo: hub, Name: "library/alpine", Pull: true}
	digest := "sha256:" + strings.Repeat("a", 64)
	run := func(p httpserver.Principal, query string) string {
		seen = "<not served>"
		r := withPrincipal(httptest.NewRequestWithContext(context.Background(), "POST", "/v2/web/docker-local/app/blobs/uploads/?"+query, nil), p)
		_ = r.ParseForm() // a form parsed from the old query must not survive a stripped mount
		router.ServeHTTP(httptest.NewRecorder(), r)
		return seen
	}
	q := "mount=" + digest + "&from=web/docker-hub/library/alpine"
	if got := run(grant(target, sourcePull), q); got != q {
		t.Errorf("pull on the source: the mount was dropped: %q", got)
	}
	if got := run(grant(target), q); got != "" {
		t.Errorf("no pull on the source: %q, want the request without mount and from", got)
	}
	if formParsed || !strings.Contains(logs.String(), "mount stripped") || !strings.Contains(logs.String(), "source=web/docker-hub/library/alpine") {
		t.Errorf("a stripped mount keeps the old form or leaves the form unparsed (%v) or leaves no trace: %q", formParsed, logs.String())
	}
	if got := run(grant(target, httpserver.RegistryEntry{Repo: hub, Name: "other", Pull: true}), q); got != "" {
		t.Errorf("pull on another image: %q", got)
	}
	// A cache view as the source is its base repository.
	if got := run(grant(target, sourcePull), "mount="+digest+"&from=web/docker-hub~cache/library/alpine"); got == "" {
		t.Errorf("pull on the base repository of a cache view: the mount was dropped")
	}
	// Other requests are untouched, and a request without a registry token is the decision point's.
	if got := run(grant(target), "digest="+digest); got != "digest="+digest {
		t.Errorf("an upload without a mount: %q", got)
	}
	if got := run(httpserver.Principal{Kind: httpserver.KindUser}, q); got != q {
		t.Errorf("a trove token presented directly: %q", got)
	}
}

// The challenge of a mount asks for push on the target and pull on the source (the distribution specification's token flow): a
// client that follows the challenge gets a token that carries both, so the mount is served as a mount and not dropped to a session.
// Found by the conformance suite, whose client takes the scope from the challenge: its mount answered 202 for lack of the source's pull.
func TestTheChallengeOfAMountNamesTheSourcesPullToo(t *testing.T) {
	disp, err := dispatch.New(dispatch.Options{Host: stubHost{}, Decider: stubDecider{}, Repos: stubRepos{}})
	if err != nil {
		t.Fatal(err)
	}
	src := func(from string, bound qname.Repo) (qname.Repo, string, bool, bool) {
		if !bound.IsZero() {
			return bound, from, false, true
		}
		p := strings.SplitN(from, "/", 3)
		if len(p) != 3 {
			return qname.Repo{}, "", false, false
		}
		return qname.Repo{Project: p[0], Key: p[1]}, p[2], false, true
	}
	e, err := registry.NewEnforcer(registry.EnforcerOptions{Matcher: disp, WriteError: writeError, MountRoute: "oci.upload.start", MountSource: src})
	if err != nil {
		t.Fatal(err)
	}
	var oci formats.Format
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			oci = f
		}
	}
	router := httpserver.NewClientRouter()
	if err := e.Mount(router, []string{"/v2/{rest...}"}, http.NotFoundHandler(), registry.RepositoryRoutes(oci.Routes(), "/v2/web/docker-local/")...); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	req := func(method, query string) *http.Request {
		return httptest.NewRequestWithContext(context.Background(), method, "/v2/web/docker-local/app/blobs/uploads/?"+query, nil)
	}
	const target, source = "repository:web/docker-local/app:push", "repository:web/docker-hub/library/alpine:pull"
	if got := e.Scope(req("POST", "mount="+digest+"&from=web/docker-hub/library/alpine")); got != target+" "+source {
		t.Errorf("scope of a mount = %q, want the target's push and the source's pull", got)
	}
	for name, q := range map[string]string{"an upload without a mount": "digest=" + digest, "a mount without from": "mount=" + digest, "a from that names no image": "mount=" + digest + "&from=x"} {
		if got := e.Scope(req("POST", q)); got != target {
			t.Errorf("%s: scope = %q, want the target's push alone", name, got)
		}
	}
	// A registry token without push on the target is told both entries as well.
	p := httpserver.Principal{Kind: httpserver.KindUser, Credential: httpserver.Credential{Registry: &httpserver.RegistryGrant{}}}
	err = e.Check(withPrincipal(req("POST", "mount="+digest+"&from=web/docker-hub/library/alpine"), p))
	if ee, ok := errs.As(err); !ok || !strings.Contains(ee.Challenge, `scope="`+target+" "+source+`"`) {
		t.Errorf("the challenge of a token without push on a mount's target: %v", err)
	}
}
