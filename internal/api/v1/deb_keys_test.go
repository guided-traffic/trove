package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// fakeDebKeys stands in for the key group of the Debian data port; it records the calls.
type fakeDebKeys struct {
	keys  []rows.Key
	calls []string
	err   error
	got   rows.NewKey
}

func (f *fakeDebKeys) Keys(context.Context, int64) ([]rows.Key, error) { return f.keys, f.err }
func (f *fakeDebKeys) CreateNext(_ context.Context, in rows.NewKey) (rows.Key, error) {
	f.calls, f.got = append(f.calls, "create"), in
	return rows.Key{Fingerprint: strings.Repeat("A", 40), State: rows.KeyNext, Origin: rows.KeyGenerated, Algorithm: 22, Bits: 256}, f.err
}

func (f *fakeDebKeys) Promote(context.Context, int64, string) (rows.Promotion, error) {
	f.calls = append(f.calls, "promote")
	return rows.Promotion{New: rows.Key{Fingerprint: strings.Repeat("B", 40), State: rows.KeyPrimary}}, f.err
}

func (f *fakeDebKeys) RetireNext(_ context.Context, _ int64, fp, _ string) error {
	f.calls = append(f.calls, "retire "+fp[:1])
	return f.err
}

func (f *fakeDebKeys) ConfirmWithheld(_ context.Context, _ int64, fp, _ string) error {
	f.calls = append(f.calls, "confirm "+fp[:1])
	return f.err
}

func (f *fakeDebKeys) ReinstateKey(_ context.Context, _ int64, fp, _ string) (rows.Reinstated, error) {
	f.calls = append(f.calls, "reinstate "+fp[:1])
	return rows.Reinstated{Key: rows.Key{Fingerprint: fp, State: rows.KeyPrimary}}, f.err
}

type fakeDebRepos struct{ repository.Reader }

func (fakeDebRepos) ByName(_ context.Context, name qname.Repo) (repository.Repository, error) {
	switch name.Key {
	case "apt-local":
		return repository.Repository{ID: "7", Name: name, Class: repository.Local, Format: "deb"}, nil
	case "apt-remote":
		return repository.Repository{ID: "8", Name: name, Class: repository.Remote, Format: "deb"}, nil
	case "npm-local":
		return repository.Repository{ID: "9", Name: name, Class: repository.Local, Format: "npm"}, nil
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
}

func debKeysServer(store DebKeyStore) *Server {
	o := Options{DebKeys: store}
	o.Repositories.Reader = fakeDebRepos{}
	o.API.Config = config.API{MaxRequestBodyBytes: 1 << 20}
	return New(o)
}

func (s *Server) debDo(t *testing.T, h func(http.ResponseWriter, *http.Request), method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestR_DEB_015_ListNeverCarriesAPrivateKey(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeDebKeys{keys: []rows.Key{
		{Fingerprint: strings.Repeat("A", 40), State: rows.KeyPrimary, Origin: rows.KeyGenerated, Algorithm: 22, Bits: 256, KeyCreated: now, BoundSince: now, CreatedAt: now, PublicKey: "-----BEGIN PGP PUBLIC KEY BLOCK-----"},
	}}
	s := debKeysServer(store)
	w := s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.ListDebSigningKeys(w, r, "p", "apt-local") }, "GET", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out struct{ Keys []map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Keys) != 1 {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	for field := range out.Keys[0] {
		if strings.Contains(strings.ToLower(field), "private") || strings.Contains(strings.ToLower(field), "secret") {
			t.Errorf("the answer has a member %q", field)
		}
	}
	if strings.Contains(w.Body.String(), "PRIVATE KEY BLOCK") {
		t.Error("the answer holds a private key block")
	}
}

func TestR_DEB_015_KeyOperationsApplyOnlyToLocalAndVirtualDebianRepositories(t *testing.T) {
	store := &fakeDebKeys{}
	s := debKeysServer(store)
	for _, repo := range []string{"apt-remote", "npm-local"} {
		w := s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.ListDebSigningKeys(w, r, "p", repo) }, "GET", "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "not_applicable") {
			t.Errorf("%s: %d %s", repo, w.Code, w.Body)
		}
	}
	w := s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.PromoteDebSigningKey(w, r, "p", "unknown") }, "POST", "")
	if w.Code != 404 {
		t.Errorf("an unknown repository = %d", w.Code)
	}
	if len(store.calls) != 0 {
		t.Errorf("the store was called: %v", store.calls)
	}
	// Without the port or the repository snapshot the operations answer 503.
	n := New(Options{})
	w = n.debDo(t, func(w http.ResponseWriter, r *http.Request) { n.ListDebSigningKeys(w, r, "p", "apt-local") }, "GET", "")
	if w.Code != 503 {
		t.Errorf("without a store = %d", w.Code)
	}
}

func TestR_DEB_015_CreateTakesGenerateOrAnImport(t *testing.T) {
	store := &fakeDebKeys{}
	s := debKeysServer(store)
	create := func(body string) *httptest.ResponseRecorder {
		return s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.CreateDebSigningKey(w, r, "p", "apt-local") }, "POST", body)
	}
	if w := create(`{"generate": true}`); w.Code != 201 || !store.got.Generate || store.got.RepoID != 7 {
		t.Errorf("generate = %d %s, got %+v", w.Code, w.Body, store.got)
	}
	if w := create(`{"privateKey": "KEY", "passphrase": "pw"}`); w.Code != 201 || store.got.Generate || len(store.got.Armored) != 3 {
		t.Errorf("import = %d %s", w.Code, w.Body)
	}
	// The secret and the passphrase are wiped after the call: used once, never kept.
	if string(store.got.Armored) == "KEY" || string(store.got.Passphrase) == "pw" {
		t.Error("the request's key material was not wiped after the call")
	}
	for name, body := range map[string]string{"neither": `{}`, "both": `{"generate": true, "privateKey": "K"}`, "empty key": `{"privateKey": "  "}`, "unknown member": `{"generate": true, "x": 1}`} {
		if w := create(body); w.Code != 400 && w.Code != 422 {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	// An error of the store keeps its status and never echoes the body.
	store.err = errs.E(errs.Unprocessable, errs.CodeDebKeyUnsupported, "refused")
	if w := create(`{"privateKey": "SECRET-MATERIAL"}`); w.Code != 422 || strings.Contains(w.Body.String(), "SECRET-MATERIAL") {
		t.Errorf("refusal = %d %s", w.Code, w.Body)
	}
}

func TestR_DEB_017_RetireDispatchesByState(t *testing.T) {
	fp := func(c string) string { return strings.Repeat(c, 40) }
	store := &fakeDebKeys{keys: []rows.Key{
		{Fingerprint: fp("A"), State: rows.KeyPrimary},
		{Fingerprint: fp("B"), State: rows.KeyNext},
		{Fingerprint: fp("C"), State: rows.KeyWithheld},
		{Fingerprint: fp("D"), State: rows.KeyRetired},
	}}
	s := debKeysServer(store)
	retire := func(f string) *httptest.ResponseRecorder {
		return s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.RetireDebSigningKey(w, r, "p", "apt-local", f) }, "DELETE", "")
	}
	if w := retire(fp("B")); w.Code != 204 {
		t.Errorf("next = %d", w.Code)
	}
	if w := retire(fp("C")); w.Code != 204 {
		t.Errorf("withheld = %d", w.Code)
	}
	for name, f := range map[string]string{"primary": fp("A"), "retired": fp("D")} {
		if w := retire(f); w.Code != 409 || !strings.Contains(w.Body.String(), "deb_key_state") {
			t.Errorf("%s = %d %s", name, w.Code, w.Body)
		}
	}
	if w := retire(fp("E")); w.Code != 404 {
		t.Errorf("unknown = %d", w.Code)
	}
	if got := strings.Join(store.calls, ","); got != "retire B,confirm C" {
		t.Errorf("calls %q", got)
	}
}

func TestR_DEB_017_PromoteAndReinstateAnswerTheKey(t *testing.T) {
	store := &fakeDebKeys{}
	s := debKeysServer(store)
	w := s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.PromoteDebSigningKey(w, r, "p", "apt-local") }, "POST", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"primary"`) {
		t.Errorf("promote = %d %s", w.Code, w.Body)
	}
	w = s.debDo(t, func(w http.ResponseWriter, r *http.Request) {
		s.ReinstateDebSigningKey(w, r, "p", "apt-local", strings.Repeat("C", 40))
	}, "POST", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), strings.Repeat("C", 40)) {
		t.Errorf("reinstate = %d %s", w.Code, w.Body)
	}
	store.err = errs.E(errs.Conflict, errs.CodeDebNoNextKey, "no next key")
	w = s.debDo(t, func(w http.ResponseWriter, r *http.Request) { s.PromoteDebSigningKey(w, r, "p", "apt-local") }, "POST", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "deb_no_next_key") {
		t.Errorf("no next key = %d %s", w.Code, w.Body)
	}
}
