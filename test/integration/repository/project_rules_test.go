//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// createIn puts a repository into a project and fails the test when the service refuses it.
func (r *replica) createIn(project, key, class, format, extra string) repository.Stored {
	r.t.Helper()
	if class == "remote" && !strings.Contains(extra, `"remote"`) {
		remote := `"remote":{"url":"https://upstream.example.test/"}`
		if extra == "" {
			extra = remote
		} else {
			extra = remote + "," + extra
		}
	}
	st, err := r.svc.Create(context.Background(), caller, project, doc(key, class, format, extra))
	if err != nil {
		r.t.Fatalf("create %s/%s: %v", project, key, err)
	}
	return st
}

// TestR_REPO_002_AKeyIsUniqueWithinItsProject: the same key lives in two projects as two repositories
// (Q-AUTHZ-206, Q-REPO-212); a second create of a key in one project is 409 already_exists.
func TestR_REPO_002_AKeyIsUniqueWithinItsProject(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'other', 'Other')`)
	a := e.newReplica("keys-a", replicaOpts{catalog: pkgCatalog()})
	b := e.newReplica("keys-b", replicaOpts{catalog: pkgCatalog()})
	ctx := context.Background()

	one := a.createIn("it", "npm-remote", "remote", "pkgfmt", "")
	two := a.createIn("other", "npm-remote", "remote", "pkgfmt", "")

	t.Run("R-REPO-002 R-API-022 the same key in two projects is two repositories with two ids", func(t *testing.T) {
		if one.Repository.ID == two.Repository.ID {
			t.Fatalf("one id for it/npm-remote and other/npm-remote: %s", one.Repository.ID)
		}
		if one.Repository.Name != (qname.Repo{Project: "it", Key: "npm-remote"}) || two.Repository.Name != (qname.Repo{Project: "other", Key: "npm-remote"}) {
			t.Fatalf("names %v %v", one.Repository.Name, two.Repository.Name)
		}
		if n := e.scalar(`SELECT count(*) FROM repositories WHERE key = 'npm-remote'`); n != 2 {
			t.Fatalf("%d rows", n)
		}
	})

	t.Run("R-REPO-009 R-API-022 a key taken in the project answers 409 already_exists, in the other project it is free", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, "other", doc("npm-remote", "remote", "pkgfmt", `"remote":{"url":"https://upstream.example.test/"}`))
		wantCode(t, err, 409, errs.CodeAlreadyExists)
		if _, err := a.svc.Create(ctx, caller, "nowhere", doc("npm-x", "local", "pkgfmt", "")); err == nil {
			t.Fatal("a create into a project that does not exist succeeded")
		} else {
			wantCode(t, err, 404, errs.CodeNotFound)
		}
	})

	t.Run("R-REPO-010 a read names the repository by project and key and carries the project", func(t *testing.T) {
		st, err := a.svc.Get(ctx, qname.Repo{Project: "other", Key: "npm-remote"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Document.Project != "other" || st.Repository.Name.Project != "other" || st.Repository.ID != two.Repository.ID {
			t.Fatalf("stored %+v", st.Document)
		}
		if _, err := a.svc.Get(ctx, qname.Repo{Project: "other", Key: "missing"}); err == nil {
			t.Fatal("a missing key was found")
		}
	})

	t.Run("R-REPO-012 the list of a project holds its own repositories, the instance list both, each with its project", func(t *testing.T) {
		only, err := a.svc.List(ctx, repository.ListFilter{Project: "other"})
		if err != nil || len(only.Items) != 1 || only.Items[0].Repository.Name.Project != "other" {
			t.Fatalf("list of other: %+v, %v", only.Items, err)
		}
		all, err := a.svc.List(ctx, repository.ListFilter{})
		if err != nil || len(all.Items) != 2 || all.Items[0].Repository.Name.Project != "it" || all.Items[1].Repository.Name.Project != "other" {
			t.Fatalf("instance list: %+v, %v", all.Items, err)
		}
		page, err := a.svc.List(ctx, repository.ListFilter{After: qname.Repo{Project: "it", Key: "npm-remote"}})
		if err != nil || len(page.Items) != 1 || page.Items[0].Repository.Name.Project != "other" {
			t.Fatalf("the page after it/npm-remote: %+v, %v", page.Items, err)
		}
	})

	t.Run("R-REPO-001 R-REPO-009 a body that names another project is refused as immutable at /project", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, "it", doc("npm-moved", "local", "pkgfmt", `"project":"other"`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldPointers(t, ee); got["/project"] != "immutable" {
			t.Fatalf("fields %v", got)
		}
		_, err = a.svc.Replace(ctx, caller, qname.Repo{Project: "it", Key: "npm-remote"}, repository.MatchAny(),
			doc("npm-remote", "remote", "pkgfmt", `"project":"other","remote":{"url":"https://upstream.example.test/"}`))
		ee = wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldPointers(t, ee); got["/project"] != "immutable" {
			t.Fatalf("fields %v", got)
		}
		// The document that was read can be sent back: it carries its own project.
		st, err := a.svc.Get(ctx, qname.Repo{Project: "it", Key: "npm-remote"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Document.Project != "it" {
			t.Fatalf("document project %q", st.Document.Project)
		}
	})

	t.Run("R-REPO-049 the snapshot of another replica holds both repositories under their names", func(t *testing.T) {
		for _, n := range []qname.Repo{{Project: "it", Key: "npm-remote"}, {Project: "other", Key: "npm-remote"}} {
			r, err := b.reader.ByName(ctx, n)
			if err != nil || r.Name != n {
				t.Fatalf("ByName(%s) = %+v, %v", n, r.Name, err)
			}
		}
		listed, err := b.reader.ListProject(ctx, "other")
		if err != nil || len(listed) != 1 || listed[0].Name.Project != "other" {
			t.Fatalf("ListProject(other) = %v, %v", listed, err)
		}
	})
}

// TestR_REPO_017_AVirtualRepositoryListsRepositoriesOfItsOwnProject: members, the deploy target and the
// pin rules are bare keys of the virtual repository's own project (Q-AUTHZ-206); a key of another
// project is not found, and a qualified name is refused (R-REPO-008 invalid_member); the AI006 triggers
// are the backstop for a row written around the service.
func TestR_REPO_017_AVirtualRepositoryListsRepositoriesOfItsOwnProject(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'other', 'Other')`)
	a := e.newReplica("vproj-a", replicaOpts{catalog: pkgCatalog()})
	ctx := context.Background()

	a.createIn("it", "team-local", "local", "pkgfmt", "")
	a.createIn("other", "other-local", "local", "pkgfmt", "")
	a.createIn("other", "team-local", "local", "pkgfmt", "")

	t.Run("R-REPO-017 a member of another project is not found and the virtual repository is not created", func(t *testing.T) {
		before := e.scalar(`SELECT count(*) FROM repositories`)
		_, err := a.svc.Create(ctx, caller, "it", doc("all", "virtual", "pkgfmt", `"virtual":{"members":["team-local","other-local"]}`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldPointers(t, ee); got["/virtual/members/1"] != errs.CodeInvalidMember || len(got) != 1 {
			t.Fatalf("fields %v", got)
		}
		if e.scalar(`SELECT count(*) FROM repositories`) != before {
			t.Fatal("a refused create wrote a row")
		}
	})

	t.Run("R-REPO-017 a qualified name is no member: invalid_member, whatever the project", func(t *testing.T) {
		for _, m := range []string{"it/team-local", "other/other-local"} {
			_, err := a.svc.Create(ctx, caller, "it", doc("all", "virtual", "pkgfmt", `"virtual":{"members":["`+m+`"]}`))
			ee := wantCode(t, err, 422, errs.CodeValidation)
			if got := fieldPointers(t, ee); got["/virtual/members/0"] != errs.CodeInvalidMember {
				t.Fatalf("%s: fields %v", m, got)
			}
		}
	})

	t.Run("R-REPO-021 a deploy target of another project, or written with a slash, is invalid_deploy_target", func(t *testing.T) {
		_, err := a.svc.Create(ctx, caller, "it", doc("all", "virtual", "pkgfmt", `"virtual":{"members":["team-local"],"deployTarget":"other-local"}`))
		ee := wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldPointers(t, ee); got["/virtual/deployTarget"] != "invalid_deploy_target" {
			t.Fatalf("fields %v", got)
		}
		_, err = a.svc.Create(ctx, caller, "it", doc("all", "virtual", "pkgfmt", `"virtual":{"members":["team-local"],"deployTarget":"it/team-local"}`))
		ee = wantCode(t, err, 422, errs.CodeValidation)
		if got := fieldPointers(t, ee); got["/virtual/deployTarget"] != "invalid_deploy_target" {
			t.Fatalf("fields %v", got)
		}
	})

	t.Run("R-REPO-017 a key that two projects share resolves to the repository of the virtual repository's project", func(t *testing.T) {
		st := a.createIn("it", "all", "virtual", "pkgfmt", `"virtual":{"members":["team-local"],"deployTarget":"team-local"}`)
		other := a.createIn("other", "all", "virtual", "pkgfmt", `"virtual":{"members":["team-local"],"deployTarget":"team-local"}`)
		want := func(virtual repository.Stored, project string) {
			t.Helper()
			var got string
			if err := e.admin.QueryRow(ctx, `SELECT p.key FROM virtual_members m JOIN repositories r ON r.id = m.member_id JOIN project p ON p.id = r.project_id WHERE m.virtual_id = $1::bigint`, virtualID(virtual)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != project {
				t.Fatalf("the member of %s is a repository of %s", virtual.Repository.Name, got)
			}
			if members := virtual.Document.Virtual.Members; len(members) != 1 || members[0] != "team-local" {
				t.Fatalf("members %v", members)
			}
		}
		want(st, "it")
		want(other, "other")
		r, err := a.reader.ByName(ctx, qname.Repo{Project: "other", Key: "all"})
		if err != nil || len(r.Members()) != 1 || r.Members()[0].Name.Project != "other" {
			t.Fatalf("members of other/all: %v, %v", r.Members(), err)
		}
	})

	t.Run("R-REPO-017 the trigger refuses a member row of another project written around the service (AI006)", func(t *testing.T) {
		virtualRow := e.scalar(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'it' AND r.key = 'all'`)
		foreign := e.scalar(`SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'other' AND r.key = 'other-local'`)
		_, err := e.admin.Exec(ctx, `INSERT INTO virtual_members (virtual_id, position, member_id) VALUES ($1, 5, $2)`, virtualRow, foreign)
		if err == nil || !strings.Contains(err.Error(), "AI006") {
			t.Fatalf("err = %v, want SQLSTATE AI006", err)
		}
	})
}

func virtualID(st repository.Stored) int64 { return repoID(st.Repository) }
