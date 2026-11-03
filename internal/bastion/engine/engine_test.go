package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/bastion/cache"
	"github.com/guided-traffic/trove/internal/bastion/downloads"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
	"github.com/guided-traffic/trove/internal/bastion/metrics"
	"github.com/guided-traffic/trove/internal/config"
)

func deps(t *testing.T) Deps {
	t.Helper()
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		Config: &config.BastionConfig{}, Metrics: m, Main: mainclient.Stub{}, Cache: cache.Stub{}, Downloads: downloads.Stub{},
	}
}

func TestR_BAS_001_ThePlaceholderRefusesEveryRequest(t *testing.T) {
	d := deps(t)
	e, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(method, "/api/npm/npm-all/lodash", nil))
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" ||
			(method != http.MethodHead && !strings.Contains(rec.Body.String(), "bastion_not_served")) {
			t.Errorf("%s: %d %q %q", method, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	if got := d.Stubbed(); len(got) != 3 {
		t.Errorf("Stubbed() = %v, want the three placeholders (the guard is built)", got)
	}
	if _, err := New(Deps{}); err == nil {
		t.Error("New accepted empty dependencies")
	}
}
