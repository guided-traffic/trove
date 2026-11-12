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
