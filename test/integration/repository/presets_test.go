//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_REPO_062_TheOCIPresetsBringTheirHosts: on a real database and with the registered OCI format, a remote whose url is a
// preset's and that omits extraHosts stores that preset's hosts, another URL stores none, a merge patch that omits extraHosts keeps
// them as stored, and a mirror of any preset needs a bound (R-REPO-062, R-REPO-052).
func TestR_REPO_062_TheOCIPresetsBringTheirHosts(t *testing.T) {
	e := newStackEnv(t)
	rep := e.newReplica("presets", stackOpts{})
	ctx := context.Background()
	hosts := func(t *testing.T, st repository.Stored) []repository.ExtraHost {
		t.Helper()
		var remote struct {
			ExtraHosts []repository.ExtraHost `json:"extraHosts"`
		}
		if err := json.Unmarshal(st.Document.Remote, &remote); err != nil {
			t.Fatal(err)
		}
		return remote.ExtraHosts
	}
	gitlab := []repository.ExtraHost{{Pattern: "gitlab.com", SendCredentials: true}}

	t.Run("R-REPO-062 the GitLab preset's URL stores gitlab.com receiving the credential", func(t *testing.T) {
		st, err := rep.svc.Create(ctx, caller, "it", []byte(`{"key":"oci-gitlab","class":"remote","format":"oci","remote":{"url":"https://registry.gitlab.com/"}}`))
		if err != nil {
			t.Fatal(err)
		}
		got, err := rep.svc.Get(ctx, st.Repository.Name)
		if err != nil {
			t.Fatal(err)
		}
		if h := hosts(t, got); !reflect.DeepEqual(h, gitlab) {
			t.Fatalf("extraHosts = %+v", h)
		}
	})
	t.Run("R-REPO-062 another URL stores no host", func(t *testing.T) {
		st, err := rep.svc.Create(ctx, caller, "it", []byte(`{"key":"oci-private","class":"remote","format":"oci","remote":{"url":"https://registry.example.com/"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if h := hosts(t, st); h == nil || len(h) != 0 {
			t.Fatalf("extraHosts = %+v", h)
		}
	})
	t.Run("R-REPO-062 an omitted url takes Docker Hub and auth.docker.io", func(t *testing.T) {
		st, err := rep.svc.Create(ctx, caller, "it", []byte(`{"key":"oci-hub","class":"remote","format":"oci"}`))
		if err != nil {
			t.Fatal(err)
		}
		var remote struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(st.Document.Remote, &remote)
		if want := []repository.ExtraHost{{Pattern: "auth.docker.io", SendCredentials: true}}; remote.URL != "https://registry-1.docker.io/" || !reflect.DeepEqual(hosts(t, st), want) {
			t.Fatalf("url %q, extraHosts %+v", remote.URL, hosts(t, st))
		}
	})
	t.Run("R-REPO-062 a merge patch that omits extraHosts keeps them as stored", func(t *testing.T) {
		st, err := rep.svc.Patch(ctx, caller, qr("oci-gitlab"), repository.MatchAny(), mergePatch(`{"remote":{"url":"https://quay.io/"}}`))
		if err != nil {
			t.Fatal(err)
		}
		if h := hosts(t, st); !reflect.DeepEqual(h, gitlab) {
			t.Fatalf("extraHosts after the patch = %+v", h)
		}
	})
	t.Run("R-REPO-052 a mirror of Quay without a bound answers mirror_unbounded", func(t *testing.T) {
		_, err := rep.svc.Create(ctx, caller, "it", []byte(`{"key":"oci-quay-mirror","class":"remote","format":"oci","remote":{"url":"https://quay.io/","mode":"mirror"}}`))
		ferr := wantCode(t, err, 422, errs.CodeValidation)
		found := false
		for _, f := range ferr.Fields {
			found = found || (f.Pointer == "/remote/mode" && f.Code == errs.CodeMirrorUnbounded)
		}
		if !found {
			t.Fatalf("fields = %+v", ferr.Fields)
		}
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'oci-quay-mirror'`) != 0 {
			t.Fatal("a refused create wrote a row")
		}
	})
}
