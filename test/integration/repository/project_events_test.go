//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_AUTHZ_026_RepositoryEventsNameTheProject: the repository.* events of a repository
// of a project carry the member project, as the events of a project service account do, and
// the subject is the qualified name (R-AUTHZ-026, R-OPS-016, R-REPO-002).
func TestR_AUTHZ_026_RepositoryEventsNameTheProject(t *testing.T) {
	e := newStackEnv(t)
	e.exec(`INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'acme', 'Acme')`)
	rep := e.newReplica("events", stackOpts{})
	ctx := context.Background()
	acme := "acme"
	details := func(key, typ string) map[string]any {
		t.Helper()
		var last map[string]any
		for _, ev := range e.events(rep.Name) {
			if ev.Subject == key && ev.Type == typ {
				last = map[string]any{}
				if err := json.Unmarshal([]byte(ev.Details), &last); err != nil {
					t.Fatal(err)
				}
			}
		}
		if last == nil {
			t.Fatalf("no %s of %s", typ, key)
		}
		return last
	}

	t.Run("R-AUTHZ-026 the create of a repository into a project names the project", func(t *testing.T) {
		if _, err := rep.svc.Create(ctx, caller, acme, []byte(fmt.Sprintf(genericBody, "acme-files"))); err != nil {
			t.Fatal(err)
		}
		if d := details("acme/acme-files", repository.EventCreated); d["project"] != "acme" {
			t.Fatalf("details %v", d)
		}
	})
	t.Run("R-AUTHZ-026 the update and the delete name it as well", func(t *testing.T) {
		if _, err := rep.svc.Patch(ctx, caller, qname.Repo{Project: acme, Key: "acme-files"}, repository.MatchAny(), mergePatch(`{"description":"files"}`)); err != nil {
			t.Fatal(err)
		}
		if d := details("acme/acme-files", repository.EventUpdated); d["project"] != "acme" {
			t.Fatalf("details %v", d)
		}
		if _, err := rep.svc.Delete(ctx, caller, qname.Repo{Project: acme, Key: "acme-files"}, false); err != nil {
			t.Fatal(err)
		}
		if d := details("acme/acme-files", repository.EventDeleted); d["project"] != "acme" {
			t.Fatalf("details %v", d)
		}
	})
	t.Run("R-REPO-002 the event of the same key in another project names that project", func(t *testing.T) {
		if _, err := rep.svc.Create(ctx, caller, "it", []byte(fmt.Sprintf(genericBody, "plain-files"))); err != nil {
			t.Fatal(err)
		}
		if d := details("it/plain-files", repository.EventCreated); d["project"] != "it" {
			t.Fatalf("details %v", d)
		}
	})
}
