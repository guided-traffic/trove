package v1

import (
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestRepositoryBodies(t *testing.T) {
	t.Run("R-API-003 a body obtained by GET can be sent back: the read-only members are dropped", func(t *testing.T) {
		in := `{"key":"a-repo","class":"local","format":"generic","lifecycle":"active","createdAt":"2026-10-28T09:00:00Z",` +
			`"updatedAt":"2026-10-28T09:00:00Z","lastActivityAt":null,"warnings":[]}`
		out, err := stripReadOnly([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if err := StrictUnmarshal(out, &repository.Document{}); err != nil {
			t.Fatalf("the stripped body is refused: %v", err)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		for _, k := range readOnlyMembers() {
			if _, ok := m[k]; ok {
				t.Errorf("%s was kept", k)
			}
		}
	})
	t.Run("R-REPO-008 R-API-003 an unknown member is 400 invalid_document with its pointer", func(t *testing.T) {
		out, err := stripReadOnly([]byte(`{"key":"a-repo","class":"local","format":"generic","local":{"packageOwnrship":"enforce"}}`))
		if err != nil {
			t.Fatal(err)
		}
		err = asInvalidDocument(StrictUnmarshal(out, &repository.Document{}))
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != errs.CodeInvalidDocument || e.Status() != 400 || len(e.Fields) != 1 || e.Fields[0].Pointer != "/local/packageOwnrship" {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("R-REPO-008 a duplicate member and a body that is not an object are 400 invalid_document", func(t *testing.T) {
		for _, body := range []string{`{"key":"a","key":"b"}`, `[]`, `"x"`, `{"description":1}`} {
			out, err := stripReadOnly([]byte(body))
			if err == nil {
				err = StrictUnmarshal(out, &repository.Document{})
			}
			err = asInvalidDocument(err)
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != errs.CodeInvalidDocument || e.Status() != 400 {
				t.Errorf("%s: %v", body, err)
			}
		}
	})
	t.Run("R-API-013 a merge patch with an unknown member names it, one with a read-only member ignores it", func(t *testing.T) {
		var probe any
		_ = json.Unmarshal([]byte(`{"description":"x","excludePattern":["a"]}`), &probe)
		if p, ok := unknownMember(probe, typeOfDocument(), ""); !ok || p != "/excludePattern" {
			t.Errorf("unknownMember = %q, %v", p, ok)
		}
		out, err := stripReadOnly([]byte(`{"description":"x","updatedAt":"2030-01-01T00:00:00Z"}`))
		if err != nil || strings.Contains(string(out), "updatedAt") {
			t.Errorf("%s, %v", out, err)
		}
	})
}

func TestRepositoryPrecondition(t *testing.T) {
	req := func(v string) Precondition {
		r := httptest.NewRequest("PATCH", "/", nil)
		if v != "" {
			r.Header.Set("If-Match", v)
		}
		p, err := ParseIfMatch(r)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("R-REPO-011 a tag is the config_version, * is the blind write", func(t *testing.T) {
		if got := repositoryPrecondition(req(`"7"`)); got != repository.MatchVersion(7) {
			t.Errorf("got %+v", got)
		}
		if got := repositoryPrecondition(req(`*`)); got != repository.MatchAny() {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("R-API-012 a tag that was never issued matches no version", func(t *testing.T) {
		for _, v := range []string{`"0"`, `"abc"`, `"7-consumer"`, `"-3"`} {
			if got := repositoryPrecondition(req(v)); got != repository.MatchVersion(math.MaxInt64) {
				t.Errorf("%s: got %+v", v, got)
			}
		}
	})
	t.Run("R-API-012 the repository service answers the one stale error, with the current tag", func(t *testing.T) {
		err := errs.Stale("The repository", 5)
		var e *errs.Error
		if !errors.As(err, &e) || e.Extensions["currentEtag"] != `"5"` || e.Status() != 412 {
			t.Fatalf("err = %#v", err)
		}
	})
}

func TestRepositoryListFilters(t *testing.T) {
	t.Run("R-REPO-012 class and format take repeated and comma-separated values", func(t *testing.T) {
		got := splitValues([]string{"local,remote", "virtual", " ", "a, b"})
		if strings.Join(got, "|") != "local|remote|virtual|a|b" {
			t.Errorf("splitValues = %v", got)
		}
	})
	t.Run("R-REPO-012 an unknown class, state or view is 400", func(t *testing.T) {
		for _, q := range []string{"class=cobol", "state=active", "view=wide"} {
			r := httptest.NewRequest("GET", "/api/v1/repositories?"+q, nil)
			lq, err := ParseList(r, repositoryListSpec(), apiConfigForTest())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := repositoryFilter(lq); err == nil {
				t.Errorf("%s was accepted", q)
			}
		}
	})
	t.Run("R-API-010 a sort parameter is not known to the list", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/v1/repositories?sort=-key", nil)
		if _, err := ParseList(r, repositoryListSpec(), apiConfigForTest()); err == nil {
			t.Error("sort was accepted")
		}
	})
}

func typeOfDocument() reflect.Type { return reflect.TypeFor[repository.Document]() }

func apiConfigForTest() config.API { return config.API{PageSizeDefault: 50, PageSizeMax: 500} }
