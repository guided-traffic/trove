package formathost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
)

func TestRemoteRequestMode(t *testing.T) {
	req := func(h ...string) *formats.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for i := 0; i+1 < len(h); i += 2 {
			r.Header.Add(h[i], h[i+1])
		}
		return &formats.Request{Request: r}
	}
	t.Run("R-REPO-032 the cache view and Cache-Control: only-if-cached never contact the upstream", func(t *testing.T) {
		for name, tc := range map[string]struct {
			rq   *formats.Request
			want proxy.Mode
		}{
			"plain":        {req(), proxy.Normal},
			"only":         {req("Cache-Control", "only-if-cached"), proxy.OnlyIfCached},
			"among others": {req("Cache-Control", "max-age=0, Only-If-Cached"), proxy.OnlyIfCached},
			"no-cache":     {req("Cache-Control", "no-cache"), proxy.Normal},
		} {
			if got := modeOf(tc.rq); got != tc.want {
				t.Errorf("%s: %v, want %v", name, got, tc.want)
			}
		}
		view := req()
		view.CacheView = true
		if modeOf(view) != proxy.CacheView {
			t.Error("the cache view is not CacheView")
		}
	})
	t.Run("R-ARCH-009 the format's member of the repository document is handed to its adapter", func(t *testing.T) {
		got := formatSettings([]byte(`{"npm":{"a":1},"remote":{}}`), "npm")
		if string(got) != `{"a":1}` {
			t.Fatalf("%s", got)
		}
		if formatSettings([]byte(`not json`), "npm") != nil || formatSettings([]byte(`{}`), "npm") != nil {
			t.Fatal("a missing member must be nil")
		}
	})
	t.Run("Q-NPM-13 the engine's currencies map to the contract's", func(t *testing.T) {
		if currency(proxy.Current) != formats.Current || currency(proxy.Revalidated) != formats.Revalidated || currency(proxy.Unknown) != formats.Unknown {
			t.Fatal("currency mapping")
		}
	})
}

func TestVirtualRemotesPopulate(t *testing.T) {
	t.Run("R-AUTHZ-008 the populate check of a call answers the MayFill the virtual engine decided", func(t *testing.T) {
		p := &callPopulate{rc: virtual.RemoteCall{MayFill: true, Subject: authz.Subject{Endpoint: "customers", CacheFill: false}}}
		if !p.May(t.Context()) {
			t.Fatal("MayFill true was refused")
		}
		if name, fills := p.Endpoint(); name != "customers" || fills {
			t.Fatalf("%q %v", name, fills)
		}
		p.rc.MayFill = false
		if p.May(t.Context()) {
			t.Fatal("MayFill false was allowed")
		}
		if err := p.Deny(t.Context()); errs.KindOf(err) != errs.Forbidden {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-ARCH-009 the binding needs the engine, the reader and the decision point", func(t *testing.T) {
		if _, err := NewVirtualRemotes(VirtualRemotesOptions{}); err == nil {
			t.Fatal("an empty binding was accepted")
		}
	})
}

type refuser struct {
	names   map[string]bool
	asked   []string
	viaNil  bool
	failure error
}

func (r *refuser) RemoteRefused(_ context.Context, _ repository.Repository, name string, via *repository.Repository) (bool, error) {
	r.asked = append(r.asked, name)
	r.viaNil = via == nil
	return r.names[name], r.failure
}

func TestR_REPO_023_ARemoteRepositoryRefusesANameAGlobalClaimCoversEvenWhenAddressedDirectly(t *testing.T) {
	d := &fakeDecider{}
	h := newTestHost(t, d)
	ref := &refuser{names: map[string]bool{"@acme/ui": true}}
	h.o.Refusal = ref
	remoteRq := func() *formats.Request {
		rq := testRequest(formats.Metadata)
		rq.Repo.Class = repository.Remote
		return rq
	}
	read := []formats.Action{formats.ActionRead}
	if err := h.Authorize(t.Context(), remoteRq(), formats.Target{Package: "@acme/ui", Actions: read}); errs.KindOf(err) != errs.NotFound {
		t.Fatalf("a claimed name: %v", err)
	}
	if !ref.viaNil {
		t.Error("a request addressed to the remote repository has no virtual repository in front of it")
	}
	if err := h.Authorize(t.Context(), remoteRq(), formats.Target{Package: "lodash", Actions: read}); err != nil {
		t.Fatalf("an unclaimed name: %v", err)
	}
	// A repository of another class, a request without a package and a write are not subject to it.
	local := testRequest(formats.Metadata)
	ref.asked = nil
	if err := h.Authorize(t.Context(), local, formats.Target{Package: "@acme/ui", Actions: read}); err != nil {
		t.Fatalf("a local repository: %v", err)
	}
	if err := h.Authorize(t.Context(), remoteRq(), formats.Target{Actions: read}); err != nil {
		t.Fatalf("no package: %v", err)
	}
	if len(ref.asked) != 0 {
		t.Errorf("asked %v", ref.asked)
	}
	// A failure to decide is a failure to serve, not an admission.
	ref.failure = errs.E(errs.Unavailable, errs.CodeUnavailable, "claims not loaded")
	if err := h.Authorize(t.Context(), remoteRq(), formats.Target{Package: "lodash", Actions: read}); errs.KindOf(err) != errs.Unavailable {
		t.Fatalf("err = %v", err)
	}
}

type typedRemote struct{ formats.RemoteAdapter }

func (typedRemote) ExpectsType(p formats.Path, mediaType string) bool {
	return p.Path == "doc" && mediaType == "text/html"
}

func TestR_REPO_042_TheExpectedTypesOfTheFormatReachTheEngine(t *testing.T) {
	a := &adapter{ra: typedRemote{}}
	if !a.ExpectsType("doc", "text/html") || a.ExpectsType("other", "text/html") {
		t.Fatal("the adapter does not forward the format's declaration")
	}
	if (&adapter{ra: struct{ formats.RemoteAdapter }{}}).ExpectsType("doc", "text/html") {
		t.Fatal("a format that declares nothing expects nothing")
	}
}
