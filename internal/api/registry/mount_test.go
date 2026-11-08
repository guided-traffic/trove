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
		seen, formParsed = r.URL.RawQuery, r.Form != nil
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
		t.Errorf("a stripped mount keeps its parsed form (%v) or leaves no trace: %q", formParsed, logs.String())
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
