package rawfiles

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
)

type recordingObserver struct {
	matched []bastionmain.RouteMatch
	refuse  error
	handled bool
}

func (o *recordingObserver) Matched(w http.ResponseWriter, _ *http.Request, m bastionmain.RouteMatch) (bool, error) {
	o.matched = append(o.matched, m)
	if o.handled {
		w.WriteHeader(http.StatusNoContent)
	}
	return o.handled, o.refuse
}

func (o *recordingObserver) Decided(*http.Request, bastionmain.RouteDecision) {}

func observedRequest(t *testing.T, o *recordingObserver, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpserver.NewClientRouter()
	New(Options{}).Register(router)
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(bastionmain.WithObserver(req.Context(), o))
	rec := httptest.NewRecorder()
	httpserver.Chain{Router: router}.Handler().ServeHTTP(rec, req)
	return rec
}

// TestR_BAS_013_FilesRoutesTellTheObserverTheirKind: /files/ has no format prefix, so the raw file handler tells the
// observer the route itself; a folder listing is like a document (R-BAS-013).
func TestR_BAS_013_FilesRoutesTellTheObserverTheirKind(t *testing.T) {
	cases := []struct {
		method, path, name, kind string
		action                   authz.Action
	}{
		{"GET", "/files/files-local/a/b.zip", "files.get", bastionmain.KindDownload, authz.Read},
		{"HEAD", "/files/files-local/a/b.zip", "files.get", bastionmain.KindDownload, authz.Read},
		{"GET", "/files/files-local/a/", "files.list", bastionmain.KindMetadata, authz.Read},
		{"GET", "/files/files-local/", "files.list", bastionmain.KindMetadata, authz.Read},
		{"PUT", "/files/files-local/a/b.zip", "files.put", bastionmain.KindUpload, authz.Deploy},
		{"DELETE", "/files/files-local/a/b.zip", "files.delete", bastionmain.KindDelete, authz.Delete},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			o := &recordingObserver{}
			rec := observedRequest(t, o, tc.method, tc.path)
			if len(o.matched) != 1 {
				t.Fatalf("matched %d times (answer %d)", len(o.matched), rec.Code)
			}
			m := o.matched[0]
			if m.Format != bastionmain.FormatFiles || m.Repo != "files-local" || m.Name != tc.name || m.Kind != tc.kind || m.Action != tc.action || m.Method != tc.method {
				t.Fatalf("match %+v", m)
			}
		})
	}
	t.Run("a path that is no valid path never reaches the observer", func(t *testing.T) {
		o := &recordingObserver{}
		if rec := observedRequest(t, o, "GET", "/files/files-local/a/../b"); rec.Code != 400 || len(o.matched) != 0 {
			t.Fatalf("%d, matched %v", rec.Code, o.matched)
		}
	})
}

// TestR_BAS_014_FilesWritesAreRefusedByTheObserver: the refusal of the observer is the answer, before any decision.
func TestR_BAS_014_FilesWritesAreRefusedByTheObserver(t *testing.T) {
	o := &recordingObserver{refuse: errs.E(errs.Forbidden, errs.CodeBastionReadOnly, "read-only")}
	rec := observedRequest(t, o, "PUT", "/files/files-local/a/b.zip")
	if rec.Code != 403 || len(o.matched) != 1 {
		t.Fatalf("%d, matched %v", rec.Code, o.matched)
	}
	t.Run("an observer that answered the request ends it", func(t *testing.T) {
		o := &recordingObserver{handled: true}
		if rec := observedRequest(t, o, "DELETE", "/files/files-local/a/b.zip"); rec.Code != 204 {
			t.Fatalf("%d", rec.Code)
		}
	})
}
