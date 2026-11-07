//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_BAK_029_DerivedState is scenario 7 of R-BAK-029, the criteria of R-BAK-022 through the create pipeline and served by
// replicas: an npm client that fetched a package document from the old installation after the snapshot and kept its ETag gets
// 200, not 304, from the restored installation after one publish to the same package; a job that was running at the snapshot is
// queued again, one that was cancelling is cancelled, and the restored installation lives in a keyspace of its own.
func TestR_BAK_029_DerivedState(t *testing.T) {
	e := newFormatsEnv(t)
	ctx := context.Background()
	src := e.replica("derived-src")
	e.publish(src, repoNPMLocal, npmPkg{Name: "derived", Version: "1.0.0"})
	for _, j := range []struct{ kind, state string }{{"gc", "running"}, {"reindex", "cancelling"}, {"scan", "queued"}} {
		e.st.Exec(`INSERT INTO job (id, kind, state, attempt, locked_by, locked_until)
			VALUES (gen_random_uuid(), $1, $2::job_state, 2, CASE WHEN $2 IN ('running', 'cancelling') THEN gen_random_uuid() END,
			        CASE WHEN $2 IN ('running', 'cancelling') THEN now() + interval '1 hour' END)`, j.kind, j.state)
	}
	oldKeyspace := e.st.Text(`SELECT value #>> '{}' FROM system_state WHERE key = 'keyspace_id'`)

	in := installOnStack(t, e.st)
	r := newBackupRig(t, in)
	res, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// After the snapshot: a second version, and the document's ETag in the client's hands.
	e.publish(src, repoNPMLocal, npmPkg{Name: "derived", Version: "1.1.0"})
	old := e.npmGet(src, repoNPMLocal, "derived")
	etag := old.Header.Get("ETag")
	if old.Status != 200 || etag == "" {
		t.Fatalf("the old installation's document: %d, ETag %q", old.Status, etag)
	}
	if again := e.st.Do(src, "GET", npmPath(repoNPMLocal, "derived"), e.tokReader, nil, "If-None-Match", etag); again.Status != 304 {
		t.Fatalf("the old installation answers %d to its own ETag, want 304", again.Status)
	}

	s := in.sourceOf(t, r, res.BackupID)
	dst := s.destination()
	if _, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rest := dst.stack.Start(t, testenv.ReplicaOptions{Name: "derived-dst"})
	e2 := &fenv{t: t, st: dst.stack, tokDev: e.tokDev, tokReader: e.tokReader, tokAdmin: e.tokAdmin}
	e2.publish(rest, repoNPMLocal, npmPkg{Name: "derived", Version: "1.0.1"})

	cond := dst.stack.Do(rest, "GET", npmPath(repoNPMLocal, "derived"), e.tokReader, nil, "If-None-Match", etag)
	if cond.Status != 200 {
		t.Fatalf("R-BAK-022: the old ETag %s answers %d from the restored installation, want 200 with the restored document", etag, cond.Status)
	}
	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(cond.Body, &doc); err != nil {
		t.Fatal(err)
	}
	var versions []string
	for v := range doc.Versions {
		versions = append(versions, v)
	}
	slices.Sort(versions)
	if !slices.Equal(versions, []string{"1.0.0", "1.0.1"}) {
		t.Fatalf("the restored document lists %v, want the snapshot's 1.0.0 and the new 1.0.1 (1.1.0 was published after the snapshot)", versions)
	}
	if cond.Header.Get("ETag") == etag {
		t.Fatal("the restored document carries the old installation's ETag")
	}

	t.Run("R-BAK-022 jobs and keyspace", func(t *testing.T) {
		row := func(kind string) string {
			return dst.stack.Text(`SELECT state::text || '/' || attempt || '/' || (locked_by IS NULL) FROM job WHERE kind = $1`, kind)
		}
		for kind, want := range map[string]string{"gc": "queued/2/true", "reindex": "cancelled/2/true", "scan": "queued/2/true"} {
			if got := row(kind); got != want {
				t.Errorf("job %s = %s, want %s", kind, got, want)
			}
		}
		if got := dst.stack.Text(`SELECT value #>> '{}' FROM system_state WHERE key = 'keyspace_id'`); got == oldKeyspace || got == "" {
			t.Errorf("keyspace_id %q is the old one (%q) or empty", got, oldKeyspace)
		}
	})
}
