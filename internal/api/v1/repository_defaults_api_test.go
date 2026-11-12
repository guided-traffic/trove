package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_REPO_063_TheDefaultsRefuseAnUnknownFormatAndAMissingClassLikeACreate: the defaults of a format the catalog does not know
// and of a class the format lacks answer the 422 of a create, with the code at its member, before anything is read.
func TestR_REPO_063_TheDefaultsRefuseAnUnknownFormatAndAMissingClassLikeACreate(t *testing.T) {
	s := upstreamServer(t, &recordingProber{})
	for _, c := range []struct {
		format, class, pointer, code string
	}{
		{"nope", "local", "/format", "unknown_format"},
		{"npmlike", "virtual", "/class", "unsupported_class"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/projects/it/repository-defaults?format="+c.format+"&class="+c.class, nil)
		s.GetRepositoryDefaults(w, r, "it", gen.GetRepositoryDefaultsParams{Format: c.format, Class: gen.GetRepositoryDefaultsParamsClass(c.class)})
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s/%s: %d %s", c.format, c.class, w.Code, w.Body)
		}
		var problem struct {
			Code   string                           `json:"code"`
			Errors []struct{ Pointer, Code string } `json:"errors"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range problem.Errors {
			found = found || (e.Pointer == c.pointer && e.Code == c.code)
		}
		if problem.Code != "validation_failed" || !found {
			t.Errorf("%s/%s: %s", c.format, c.class, w.Body)
		}
	}
}

// TestR_REPO_063_TheDefaultsDocumentHasNoKey: the document of the defaults is rendered as a read shows a repository, without
// the key, which is the suggestion's.
func TestR_REPO_063_TheDefaultsDocumentHasNoKey(t *testing.T) {
	doc, err := withoutKey(repository.Document{Key: "generic-local", Class: repository.Local, Format: "generic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["key"]; ok {
		t.Fatalf("the document has a key: %v", doc)
	}
	if string(doc["class"]) != `"local"` || string(doc["format"]) != `"generic"` {
		t.Fatalf("document = %v", doc)
	}
}
