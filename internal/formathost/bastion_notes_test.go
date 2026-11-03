package formathost

import (
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/bastionmain"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_BAS_013_RemoteAnswersTellTheMarkingWhatANotFoundMeans: a NotFound the engine marked absent is a lookup that
// found the item missing for every subject; any other NotFound (offline, not cached, a gone package) proves
// nothing, and neither does an upstream failure.
func TestR_BAS_013_RemoteAnswersTellTheMarkingWhatANotFoundMeans(t *testing.T) {
	note := func(err error, d *proxy.Document) *bastionmain.Absence {
		ctx, a := bastionmain.WithAbsence(t.Context())
		rq := &formats.Request{Request: httptest.NewRequest("GET", "/", nil).WithContext(ctx), Repo: &repository.Repository{Format: "npm"}}
		r := &remote{h: &Host{o: Options{Formats: func(string) (formats.Format, bool) { return nil, false }}}}
		_, _ = r.document(rq, d, err)
		return a
	}
	if !note(errs.E(errs.NotFound, errs.CodeNotFound, "x", errs.WithAbsent()), nil).Absent() {
		t.Error("an upstream 404 is absent")
	}
	if note(errs.E(errs.NotFound, errs.CodeNotFound, "x"), nil).Absent() {
		t.Error("a NotFound that is not marked is not absent")
	}
	if note(errs.E(errs.NotFound, errs.CodeNotCachedOffline, "x"), nil).Absent() {
		t.Error("an offline miss is not absent")
	}
	if !note(nil, &proxy.Document{Status: proxy.CacheStatus{Negative: true}}).Absent() {
		t.Error("a negative entry stands for an upstream 404")
	}
	if note(nil, &proxy.Document{}).Absent() {
		t.Error("a served document is not absent")
	}
	if note(errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "x"), nil).Absent() {
		t.Error("an upstream failure is not absent")
	}
	// An offline miss after an upstream 404 (a virtual repository with two remote members) is not absent either.
	ctx, a := bastionmain.WithAbsence(t.Context())
	rq := &formats.Request{Request: httptest.NewRequest("GET", "/", nil).WithContext(ctx), Repo: &repository.Repository{Format: "npm"}}
	noteRemote(rq, errs.E(errs.NotFound, errs.CodeNotFound, "x", errs.WithAbsent()))
	noteRemote(rq, errs.E(errs.NotFound, errs.CodeNotCachedOffline, "x"))
	if a.Absent() {
		t.Error("a member that could not tell makes the 404 mean less")
	}
}

// TestR_BAS_013_NotFoundMarkedAbsentSurvivesTheCacheStatusCopy: the engine copies its error to add Cache-Status.
func TestR_BAS_013_NotFoundMarkedAbsentSurvivesTheCacheStatusCopy(t *testing.T) {
	e := errs.E(errs.NotFound, errs.CodeNotFound, "x", errs.WithAbsent())
	c := *e
	if !errs.IsAbsent(&c) || errs.IsAbsent(errs.E(errs.Forbidden, errs.CodeForbidden, "x", errs.WithAbsent())) {
		t.Fatal("IsAbsent")
	}
}
