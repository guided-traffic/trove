package npm

import (
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// TestR_NPM_010_TheReadmeIsWikiContent: readme and readmeFilename of the full document are present only for a caller who may read
// the wiki of the repository (R-AUTHZ-034, Q-NPM-87). The rest of the document is unchanged, the validator and the rendition key
// carry the answer, so neither a 304 nor a cached rendition crosses from one caller to the other (R-NPM-013).
func TestR_NPM_010_TheReadmeIsWikiContent(t *testing.T) {
	var deny atomic.Bool
	rf := newRepo(t, func(o *formattest.HostOptions) {
		o.Allow = func(tg formats.Target) error {
			if tg.Part == authz.PartWiki && deny.Load() {
				return errs.E(errs.NotFound, errs.CodeNotFound, "no")
			}
			return nil
		}
	})
	rf.publish("@acme/ui", "1.0.0", map[string]any{"description": "Acme UI kit"})

	with := rf.get("@acme%2fui")
	doc := jsonOf(t, with)
	if doc["readme"] == nil || doc["readmeFilename"] == nil {
		t.Fatalf("a caller who may read the wiki gets no README: %v", doc)
	}
	etagWith := with.Header().Get("ETag")

	deny.Store(true)
	without := rf.get("@acme%2fui")
	bare := jsonOf(t, without)
	if _, ok := bare["readme"]; ok {
		t.Errorf("a caller who may not read the wiki got the README: %v", bare["readme"])
	}
	if _, ok := bare["readmeFilename"]; ok {
		t.Error("a caller who may not read the wiki got readmeFilename")
	}
	if bare["description"] != "Acme UI kit" || bare["_rev"] != doc["_rev"] || len(versionsOf(bare)) != 1 {
		t.Errorf("the rest of the document changed: %v", bare)
	}
	if without.Header().Get("ETag") == etagWith {
		t.Error("the validator does not carry the wiki answer")
	}
	if r := rf.get("@acme%2fui", "If-None-Match", etagWith); r.Code != 200 {
		t.Errorf("the validator of a document with the README is a %d for a caller without it", r.Code)
	}
	if r := rf.get("@acme%2fui", "Accept-Encoding", "gzip"); r.Code != 200 || r.Header().Get("ETag") != without.Header().Get("ETag") {
		t.Errorf("the gzip form: %d %q", r.Code, r.Header().Get("ETag"))
	}

	deny.Store(false)
	again := rf.get("@acme%2fui")
	if again.Header().Get("ETag") != etagWith || jsonOf(t, again)["readme"] == nil {
		t.Error("the caller who may read the wiki lost the README, or the validator moved with the answer of another caller")
	}
	t.Run("the abbreviated document and a version document carry no README for anyone", func(t *testing.T) {
		deny.Store(false)
		for _, p := range []string{"@acme/ui/1.0.0"} {
			if _, ok := jsonOf(t, rf.get(p))["readme"]; ok {
				t.Errorf("%s has a README", p)
			}
		}
	})
}

// TestR_NPM_010_TheReadmeMembersOfAVersionManifestFollowTheWiki: readmeFilename of a version manifest, in the full document and in the
// single-version document, is present only for a caller who may read the wiki; the validator of the version document carries the answer
// (R-AUTHZ-034, Q-NPM-88).
func TestR_NPM_010_TheReadmeMembersOfAVersionManifestFollowTheWiki(t *testing.T) {
	var deny atomic.Bool
	rf := newRepo(t, func(o *formattest.HostOptions) {
		o.Allow = func(tg formats.Target) error {
			if tg.Part == authz.PartWiki && deny.Load() {
				return errs.E(errs.NotFound, errs.CodeNotFound, "no")
			}
			return nil
		}
	})
	rf.publish("@acme/ui", "1.0.0", map[string]any{"readmeFilename": "README.md"})
	manifest := func(path string) map[string]any {
		t.Helper()
		doc := jsonOf(t, rf.get(path))
		if vs, ok := doc["versions"].(map[string]any); ok {
			return vs["1.0.0"].(map[string]any)
		}
		return doc
	}
	for _, path := range []string{"@acme%2fui", "@acme/ui/1.0.0"} {
		deny.Store(false)
		allowedTag := rf.get(path).Header().Get("ETag")
		if manifest(path)["readmeFilename"] != "README.md" {
			t.Errorf("%s: a caller who may read the wiki lost readmeFilename of the manifest", path)
		}
		deny.Store(true)
		m := manifest(path)
		for _, k := range []string{"readme", "readmeFilename"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s: a caller who may not read the wiki got %s in the version manifest", path, k)
			}
		}
		if m["version"] != "1.0.0" {
			t.Errorf("%s: the rest of the manifest changed: %v", path, m)
		}
		if rf.get(path).Header().Get("ETag") == allowedTag {
			t.Errorf("%s: the validator does not carry the wiki answer", path)
		}
	}
}
