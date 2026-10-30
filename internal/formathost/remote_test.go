package formathost

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/proxy"
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
