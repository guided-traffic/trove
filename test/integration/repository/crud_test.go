//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

func wantCode(t *testing.T, err error, status int, code string) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok || e.Status() != status || e.Code != code {
		t.Fatalf("err = %v; want %d %s", err, status, code)
	}
	return e
}

func TestCreateReadUpdate(t *testing.T) {
	e := newEnv(t)
	rep := e.newReplica("crud", replicaOpts{})
	ctx := context.Background()
	svc := rep.svc
	epoch := func() int64 { return e.scalar(`SELECT epoch FROM cluster_epoch WHERE area = 'repo_config'`) }

	var v1 repository.Stored
	t.Run("R-REPO-009 R-REPO-001 create writes the row, the audit event and the change-log row in one transaction", func(t *testing.T) {
		before := epoch()
		st, err := svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "generic-local")))
		if err != nil {
			t.Fatal(err)
		}
		v1 = st
		r := st.Repository
		if r.Key != "generic-local" || r.Class != repository.Local || r.Format != "generic" || r.Version != 1 || r.Lifecycle != repository.Active {
			t.Fatalf("repository = %+v", r)
		}
		if st.Document.Layout != "generic" || st.Document.HideExistence != "inherit" || st.Document.IncludePatterns == nil {
			t.Fatalf("document = %+v", st.Document)
		}
		if got := rep.rec.types(); len(got) != 1 || got[0] != repository.EventCreated {
			t.Fatalf("events = %v", got)
		}
		if ev := rep.rec.last(); ev.Subject.Name != "generic-local" || ev.Actor.Name != "alice" || ev.Subject.Kind != "repository" {
			t.Fatalf("event = %+v", ev)
		}
		if epoch() != before+1 || e.scalar(`SELECT count(*) FROM cluster_change WHERE area = 'repo_config'`) != before+1 {
			t.Fatal("the create did not advance the repo_config epoch with a change row")
		}
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'generic-local' AND created_by = '0198a000-0000-7000-8000-000000000001' AND updated_by IS NOT NULL`) != 1 {
			t.Fatal("created_by and updated_by are not the caller")
		}
	})

	t.Run("R-REPO-009 a create is never an upsert", func(t *testing.T) {
		before := epoch()
		_, err := svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "generic-local")))
		wantCode(t, err, 409, errs.CodeRepositoryExists)
		if epoch() != before || len(rep.rec.types()) != 1 {
			t.Fatal("a refused create left an epoch or an event behind")
		}
	})

	t.Run("R-REPO-008 a refused create writes nothing and reports every violation", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM repositories`)
		_, err := svc.Create(ctx, caller, []byte(`{"key":"Bad Key","class":"remote","format":"generic","includePatterns":["a//b"]}`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if len(ee.Fields) != 3 {
			t.Fatalf("fields = %+v", ee.Fields)
		}
		_, err = svc.Create(ctx, caller, []byte(`{"key":"x1","class":"local","format":"generic","excludePattern":[]}`))
		wantCode(t, err, 400, errs.CodeInvalidDocument)
		_, err = svc.Create(ctx, caller, []byte(`{"key":"quick-setup","class":"local","format":"generic"}`))
		wantCode(t, err, 422, "reserved_name")
		if e.scalar(`SELECT count(*) FROM repositories`) != before {
			t.Fatal("a refused create wrote a row")
		}
	})

	t.Run("R-REPO-010 read returns the stored document from PostgreSQL", func(t *testing.T) {
		st, err := svc.Get(ctx, "generic-local")
		if err != nil {
			t.Fatal(err)
		}
		if st.Repository.Version != 1 || st.Document.Key != "generic-local" {
			t.Fatalf("stored = %+v", st)
		}
		_, err = svc.Get(ctx, "nope")
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})

	t.Run("R-REPO-011 patch changes only that setting, moves the version and records what changed", func(t *testing.T) {
		before := epoch()
		st, err := svc.Patch(ctx, caller, "generic-local", repository.MatchVersion(1), []byte(`{"description":"installers","includePatterns":["releases/**"]}`))
		if err != nil {
			t.Fatal(err)
		}
		if st.Repository.Version != 2 || st.Document.Description != "installers" || st.Document.AdminNotes != "" || st.Document.Disabled {
			t.Fatalf("stored = %+v", st)
		}
		if !st.Repository.Admits("releases/1.0/x.bin", false) || st.Repository.Admits("tmp/x", false) {
			t.Fatal("the patterns do not take effect")
		}
		if got := rep.rec.last(); got.Type != repository.EventUpdated || !strings.Contains(string(got.Details), `"changes":["description","includePatterns"]`) {
			t.Fatalf("event = %s %s", got.Type, got.Details)
		}
		if epoch() != before+1 {
			t.Fatal("the update did not advance the epoch")
		}
		again, err := svc.Get(ctx, "generic-local")
		if err != nil || again.Repository.Version != 2 || again.Document.Description != "installers" {
			t.Fatalf("read back %+v, %v", again, err)
		}
	})

	t.Run("R-REPO-011 a stale If-Match is 412 with the current version, If-Match * passes", func(t *testing.T) {
		_, err := svc.Patch(ctx, caller, "generic-local", repository.MatchVersion(1), []byte(`{"description":"stale"}`))
		wantCode(t, err, 412, errs.CodePreconditionFailed)
		var sv *errs.StaleRevision
		if !errors.As(err, &sv) || sv.Current != 2 {
			t.Fatalf("cause = %v", err)
		}
		st, err := svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"description":"blind"}`))
		if err != nil || st.Repository.Version != 3 {
			t.Fatalf("blind write: %+v, %v", st, err)
		}
		_, err = svc.Patch(ctx, caller, "nope", repository.MatchAny(), []byte(`{}`))
		wantCode(t, err, 404, errs.CodeRepositoryNotFound)
	})

	t.Run("R-REPO-001 R-REPO-011 key, class and format are immutable", func(t *testing.T) {
		for member, value := range map[string]string{"format": `"npm"`, "class": `"remote"`, "key": `"other"`} {
			before := epoch()
			_, err := svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"`+member+`":`+value+`}`))
			ee := wantCode(t, err, 422, errs.CodeImmutableField)
			if len(ee.Fields) != 1 || ee.Fields[0].Pointer != "/"+member {
				t.Fatalf("%s: fields = %+v", member, ee.Fields)
			}
			if epoch() != before {
				t.Fatalf("%s: a refused update advanced the epoch", member)
			}
		}
		// The same value is not a change.
		if _, err := svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"format":"generic","class":"local","key":"generic-local"}`)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-REPO-011 an update that changes nothing writes nothing", func(t *testing.T) {
		cur, _ := svc.Get(ctx, "generic-local")
		events, before := len(rep.rec.types()), epoch()
		st, err := svc.Patch(ctx, caller, "generic-local", repository.MatchVersion(cur.Repository.Version), []byte(`{"description":"blind"}`))
		if err != nil || st.Repository.Version != cur.Repository.Version {
			t.Fatalf("no-op: %+v, %v", st, err)
		}
		if len(rep.rec.types()) != events || epoch() != before {
			t.Fatal("a no-op update wrote an event or a change")
		}
	})

	t.Run("R-REPO-011 null removes a setting, an array is replaced as a whole", func(t *testing.T) {
		st, err := svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"description":null,"includePatterns":["a/**","b/**"]}`))
		if err != nil || st.Document.Description != "" || len(st.Document.IncludePatterns) != 2 {
			t.Fatalf("stored = %+v, %v", st.Document, err)
		}
		st, err = svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"includePatterns":["c/**"]}`))
		if err != nil || len(st.Document.IncludePatterns) != 1 || st.Document.IncludePatterns[0] != "c/**" {
			t.Fatalf("stored = %+v, %v", st.Document, err)
		}
	})

	t.Run("R-REPO-011 a patch that makes the document invalid reports 422 and changes nothing", func(t *testing.T) {
		cur, _ := svc.Get(ctx, "generic-local")
		_, err := svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"priorityMember":true,"local":{"packageOwnership":"enforce"},"excludePatterns":["a**b"]}`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if len(ee.Fields) != 3 {
			t.Fatalf("fields = %+v", ee.Fields)
		}
		_, err = svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"unknownSetting":1}`))
		wantCode(t, err, 400, errs.CodeInvalidDocument)
		after, _ := svc.Get(ctx, "generic-local")
		if after.Repository.Version != cur.Repository.Version {
			t.Fatal("a refused update moved the version")
		}
	})

	t.Run("R-REPO-011 PUT replaces the document, omitted settings revert to defaults", func(t *testing.T) {
		st, err := svc.Replace(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"description":"only this","disabled":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if st.Document.Description != "only this" || !st.Repository.Disabled || len(st.Document.IncludePatterns) != 0 || st.Document.Key != "generic-local" {
			t.Fatalf("stored = %+v", st.Document)
		}
	})

	t.Run("R-OPS-015 a change never exists without its audit record", func(t *testing.T) {
		rep.rec.setFail(true)
		defer rep.rec.setFail(false)
		before := epoch()
		_, err := svc.Create(ctx, caller, []byte(fmt.Sprintf(genericBody, "audit-fails")))
		if err == nil {
			t.Fatal("create succeeded although the audit insert failed")
		}
		cur, _ := svc.Get(ctx, "generic-local")
		_, err = svc.Patch(ctx, caller, "generic-local", repository.MatchAny(), []byte(`{"description":"never"}`))
		if err == nil {
			t.Fatal("update succeeded although the audit insert failed")
		}
		after, _ := svc.Get(ctx, "generic-local")
		if e.scalar(`SELECT count(*) FROM repositories WHERE key = 'audit-fails'`) != 0 || after.Repository.Version != cur.Repository.Version || epoch() != before {
			t.Fatal("a rolled-back write left a row, a version or an epoch behind")
		}
	})

	_ = v1
}

func TestList(t *testing.T) {
	e := newEnv(t)
	rep := e.newReplica("list", replicaOpts{})
	ctx := context.Background()
	for _, body := range []string{
		`{"key":"files-internal","class":"local","format":"generic","description":"Internal release files"}`,
		`{"key":"build-outputs","class":"local","format":"generic","description":"CI artifacts"}`,
		`{"key":"old-stuff","class":"local","format":"generic","disabled":true}`,
	} {
		if _, err := rep.svc.Create(ctx, caller, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(f repository.ListFilter) []string {
		res, err := rep.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range res.Items {
			out = append(out, it.Repository.Key)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	t.Run("R-REPO-012 sorted by key, filtered by class, format, query and state", func(t *testing.T) {
		eq(keys(repository.ListFilter{}), "build-outputs", "files-internal", "old-stuff")
		eq(keys(repository.ListFilter{Classes: []string{"remote", "virtual"}}))
		eq(keys(repository.ListFilter{Classes: []string{"local"}, Formats: []string{"generic"}}), "build-outputs", "files-internal", "old-stuff")
		eq(keys(repository.ListFilter{Formats: []string{"npm"}}))
		eq(keys(repository.ListFilter{Query: "RELEASE"}), "files-internal")
		eq(keys(repository.ListFilter{Query: "build"}), "build-outputs")
		eq(keys(repository.ListFilter{State: "inactive"}), "old-stuff")
	})
	t.Run("R-REPO-012 keyset paging returns every repository exactly once", func(t *testing.T) {
		var all []string
		after := ""
		for range 5 {
			res, err := rep.svc.List(ctx, repository.ListFilter{After: after, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range res.Items {
				all = append(all, it.Repository.Key)
				after = it.Repository.Key
			}
			if !res.HasMore {
				break
			}
		}
		eq(all, "build-outputs", "files-internal", "old-stuff")
	})
}
