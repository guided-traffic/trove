//go:build integration

package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formatdata/npm"
	"github.com/guided-traffic/trove/internal/journal"
)

// tagsOf reads the stored dist-tags of a package.
func (e *fenv) tagsOf(repo, name string) map[string]string {
	e.t.Helper()
	raw := e.st.Text(`SELECT COALESCE((SELECT n.dist_tags::text FROM npm_packages n JOIN packages p ON p.id = n.package_id JOIN repositories r ON r.id = p.repo_id
		WHERE r.key = $1 AND p.name = $2), '{}')`, repo, name)
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func TestR_NPM_047_EveryOutcomeOfTheReplayTable(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	name := "@acme/table"
	integrity := e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.0.0"})
	e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.1.0"})
	at := time.Now()
	entry := func(mut func(m map[string]any)) map[string]any {
		m := e.unpublishEntry(repoNPMLocal, name, "1.1.0", integrity)
		mut(m)
		return m
	}
	// 1.1.0 was published with other bytes than the entry's integrity, so every case below that names `integrity` for 1.1.0
	// is an entry about other bytes.
	real110 := e.st.Text(`SELECT nv.integrity FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id WHERE pv.version = '1.1.0'`)

	untouched := func(t *testing.T, what string) {
		t.Helper()
		if src, _, _ := e.tombstone(repoNPMLocal, name, "1.1.0"); src != "" {
			t.Fatalf("%s wrote a tombstone (%s)", what, src)
		}
		if vs, _ := e.npmDoc(r, repoNPMLocal, name); len(vs) != 2 {
			t.Fatalf("%s changed the document: %v", what, vs)
		}
	}
	for _, c := range []struct {
		name string
		e    map[string]any
		want journal.Outcome
	}{
		{"a version the repository holds with other bytes is subject unknown", entry(func(m map[string]any) { m["integrity"] = integrity }), journal.SubjectUnknown},
		{"a version the repository does not hold is subject unknown", entry(func(m map[string]any) { m["version"] = "9.9.9"; m["integrity"] = real110 }), journal.SubjectUnknown},
		{"a package the repository does not hold is subject unknown", entry(func(m map[string]any) { m["package"] = "@acme/none"; m["integrity"] = real110 }), journal.SubjectUnknown},
		{"a repository that does not exist is subject unknown", entry(func(m map[string]any) { m["repositoryId"] = 987654; m["integrity"] = real110 }), journal.SubjectUnknown},
		{"a NuGet repository is subject unknown", entry(func(m map[string]any) { m["repositoryId"] = e.repoID(repoNuGet); m["integrity"] = real110 }), journal.SubjectUnknown},
		{"a remote npm repository is subject unknown", entry(func(m map[string]any) { m["repositoryId"] = e.repoID(repoNPMRemote); m["integrity"] = real110 }), journal.SubjectUnknown},
		{"a virtual npm repository is subject unknown", entry(func(m map[string]any) { m["repositoryId"] = e.repoID(repoNPMAll); m["integrity"] = real110 }), journal.SubjectUnknown},
		{"an entry without a version is skipped", entry(func(m map[string]any) { delete(m, "version") }), journal.Skipped},
		{"an entry with a weak integrity is skipped", entry(func(m map[string]any) { m["integrity"] = "sha1-AAAA" }), journal.Skipped},
	} {
		t.Run("R-NPM-047 "+c.name, func(t *testing.T) {
			rev := e.revSeq(repoNPMLocal, name)
			res := e.replay(audit.EventNPMUnpublish, c.e, at)
			if res.Outcome != c.want {
				t.Fatalf("outcome %q, want %q: %+v", res.Outcome, c.want, res)
			}
			untouched(t, c.name)
			if got := e.revSeq(repoNPMLocal, name); got != rev {
				t.Errorf("rev_seq %d -> %d", rev, got)
			}
		})
	}

	t.Run("R-NPM-047 a hand-written entry for a live version with its correct integrity withholds it, and the actor is not trusted", func(t *testing.T) {
		res := e.replay(audit.EventNPMUnpublish, entry(func(m map[string]any) { m["integrity"] = real110; m["actorId"] = "not a uuid" }), at)
		if res.Outcome != journal.Applied {
			t.Fatalf("outcome %q", res.Outcome)
		}
		if got := e.st.Text(`SELECT unpublished_by::text FROM npm_tombstones WHERE name = $1 AND version = '1.1.0'`, name); got != "00000000-0000-0000-0000-000000000000" {
			t.Errorf("unpublished_by = %s, want the nil UUID for an actor that is no UUID", got)
		}
		if n := e.storedVersions(repoNPMLocal, name); n != 2 {
			t.Errorf("%d stored versions: the replay deleted a row", n)
		}
	})
	t.Run("R-NPM-047 a version tombstoned by a client's unpublish is already in effect, whatever the entry's integrity", func(t *testing.T) {
		res := e.replay(audit.EventNPMUnpublish, entry(func(m map[string]any) { m["integrity"] = integrity }), at)
		if res.Outcome != journal.AlreadyInEffect {
			t.Errorf("outcome %q", res.Outcome)
		}
	})
}

func TestR_NPM_047_TheDistTagRuleRunsInTheReplay(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	name := "tagged"
	ints := map[string]string{}
	for _, v := range []npmPkg{{name, "1.0.0", ""}, {name, "1.1.0", ""}, {name, "2.0.0-beta.1", "beta"}, {name, "1.2.0", "stable"}} {
		ints[v.Version] = e.publish(r, repoNPMLocal, v)
	}
	// 1.2.0 was published under "stable": latest follows the highest release published under latest, and stable points at 1.2.0.
	if tags := e.tagsOf(repoNPMLocal, name); tags["latest"] != "1.1.0" || tags["beta"] != "2.0.0-beta.1" || tags["stable"] != "1.2.0" {
		t.Fatalf("tags before: %v", tags)
	}
	at := time.Now()
	withhold := func(v string) {
		t.Helper()
		if res := e.replay(audit.EventNPMUnpublish, e.unpublishEntry(repoNPMLocal, name, v, ints[v]), at); res.Outcome != journal.Applied {
			t.Fatalf("withhold %s: %q", v, res.Outcome)
		}
	}
	withhold("2.0.0-beta.1")
	if tags := e.tagsOf(repoNPMLocal, name); tags["beta"] != "" || tags["latest"] != "1.1.0" {
		t.Errorf("a tag that pointed at the withheld version stays: %v", tags)
	}
	withhold("1.1.0")
	if tags := e.tagsOf(repoNPMLocal, name); tags["latest"] != "1.2.0" {
		t.Errorf("latest after withholding 1.1.0 = %q, want the highest remaining release 1.2.0: %v", tags["latest"], tags)
	}
	withhold("1.2.0")
	withhold("1.0.0")
	if tags := e.tagsOf(repoNPMLocal, name); len(tags) != 0 {
		t.Errorf("with every version withheld the package keeps the tags %v", tags)
	}
	if a := e.npmGet(r, repoNPMLocal, name); a.Status != http.StatusNotFound {
		t.Errorf("with every version withheld the document answers %d, want 404", a.Status)
	}
	if n := e.storedVersions(repoNPMLocal, name); n != 4 {
		t.Errorf("%d stored versions, want 4", n)
	}
}

func TestR_NPM_047_TheEntryOfARealUnpublishIsTheEntryTheReplayReads(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	name := "producer"
	integrity := e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.0.0"})
	doc := e.npmGet(r, repoNPMLocal, name+"?write=true")
	var d struct {
		Rev string `json:"_rev"`
	}
	if err := json.Unmarshal(doc.Body, &d); err != nil || d.Rev == "" {
		t.Fatalf("document: %d %s (%v)", doc.Status, doc.Body, err)
	}
	if a := e.st.Do(r, "DELETE", npmPath(repoNPMLocal, name)+"/-rev/"+d.Rev, e.tokDev, nil); a.Status != 200 {
		t.Fatalf("unpublish: %d %s", a.Status, a.Body)
	}
	raw := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'npm.unpublish'`)
	var entry map[string]any
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"repositoryId", "package", "version", "integrity", "actorId"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("the producer's entry lacks %q: %s", k, raw)
		}
	}
	if len(entry) != 5 || entry["integrity"] != integrity {
		t.Errorf("the producer's entry has members beyond the five of R-NPM-047, or another integrity: %s", raw)
	}
	// The handler reads exactly that entry: the version is tombstoned by the unpublish itself, so it is in effect.
	if res := e.replay(audit.EventNPMUnpublish, entry, time.Now()); res.Outcome != journal.AlreadyInEffect {
		t.Errorf("replay of the producer's entry = %q", res.Outcome)
	}
}

func TestR_NPM_047_ReinstateRefusesWhatItMustNotBringBack(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	store, err := npm.New(npm.Options{DB: e.db, Recorder: e.recorder()})
	if err != nil {
		t.Fatal(err)
	}
	reinstate := func(repo, name, version string) error {
		_, err := store.Reinstate(context.Background(), npm.ReinstateInput{
			Project: project, Repository: repo, Name: name, Version: version, Actor: audit.Actor{Kind: "admin-cli", Name: "root@test"},
		})
		return err
	}
	name := "refused"
	ints := map[string]string{}
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} {
		ints[v] = e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: v})
	}
	at := time.Now()
	for _, v := range []string{"1.1.0", "1.2.0", "1.3.0"} {
		if res := e.replay(audit.EventNPMUnpublish, e.unpublishEntry(repoNPMLocal, name, v, ints[v]), at); res.Outcome != journal.Applied {
			t.Fatalf("withhold %s: %q", v, res.Outcome)
		}
	}
	rev := e.revSeq(repoNPMLocal, name)
	refuse := func(t *testing.T, kind errs.Kind, err error, source string, version string) {
		t.Helper()
		if err == nil || errs.KindOf(err) != kind {
			t.Fatalf("error = %v (kind %v), want kind %v", err, errs.KindOf(err), kind)
		}
		if got := e.revSeq(repoNPMLocal, name); got != rev {
			t.Errorf("rev_seq %d -> %d: a refused reinstate changed the package", rev, got)
		}
		if src, _, _ := e.tombstone(repoNPMLocal, name, version); src != source {
			t.Errorf("tombstone source %q, want %q", src, source)
		}
		if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'npm.reinstate'`); n != 0 {
			t.Errorf("%d npm.reinstate events after a refusal", n)
		}
	}

	t.Run("R-NPM-047 R-NPM-008 a version the stored bytes of which differ from the tombstone is not brought back (Q-NPM-8)", func(t *testing.T) {
		e.st.Exec(`UPDATE npm_versions SET integrity = 'sha512-other' FROM package_versions pv WHERE pv.id = version_id AND pv.version = '1.1.0'`)
		refuse(t, errs.Conflict, reinstate(repoNPMLocal, name, "1.1.0"), "journal", "1.1.0")
		e.st.Exec(`UPDATE npm_versions SET integrity = $1 FROM package_versions pv WHERE pv.id = version_id AND pv.version = '1.1.0'`, ints["1.1.0"])
	})
	t.Run("R-NPM-047 a version whose withdrawal an administrator confirmed by deleting its items is gone for good", func(t *testing.T) {
		a := e.st.Do(r, "DELETE", "/files/"+project+"/"+repoNPMLocal+"/"+name+"/-/"+name+"-1.2.0.tgz", e.tokAdmin, nil)
		if a.Status != 200 && a.Status != 204 {
			t.Fatalf("delete the items: %d %s", a.Status, a.Body)
		}
		rev = e.revSeq(repoNPMLocal, name)
		refuse(t, errs.Conflict, reinstate(repoNPMLocal, name, "1.2.0"), "journal", "1.2.0")
		if a, _ := e.tryPublish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.2.0"}); a.Status != http.StatusConflict {
			t.Errorf("republishing the confirmed version answers %d, want 409", a.Status)
		}
	})
	t.Run("R-NPM-047 a version a client unpublished is not withheld and is not reinstated", func(t *testing.T) {
		doc := e.npmGet(r, repoNPMLocal, name+"?write=true")
		var d struct {
			Rev string `json:"_rev"`
		}
		_ = json.Unmarshal(doc.Body, &d)
		// Deleting the package unpublishes its live version 1.0.0 (a client's unpublish).
		if a := e.st.Do(r, "DELETE", npmPath(repoNPMLocal, name)+"/-rev/"+d.Rev, e.tokDev, nil); a.Status != 200 {
			t.Fatalf("unpublish: %d %s", a.Status, a.Body)
		}
		rev = e.revSeq(repoNPMLocal, name)
		refuse(t, errs.Conflict, reinstate(repoNPMLocal, name, "1.0.0"), "unpublish", "1.0.0")
	})
	t.Run("R-NPM-047 a version with no tombstone, an unknown package and a repository that is not local npm are refused", func(t *testing.T) {
		if err := reinstate(repoNPMLocal, name, "7.7.7"); errs.KindOf(err) != errs.NotFound {
			t.Errorf("no tombstone: %v", err)
		}
		if err := reinstate(repoNPMLocal, "nothing", "1.0.0"); errs.KindOf(err) != errs.NotFound {
			t.Errorf("unknown package: %v", err)
		}
		if err := reinstate(repoNuGet, name, "1.3.0"); errs.KindOf(err) != errs.Invalid {
			t.Errorf("a NuGet repository: %v", err)
		}
		if err := reinstate(repoNPMRemote, name, "1.3.0"); errs.KindOf(err) != errs.Invalid {
			t.Errorf("a remote repository: %v", err)
		}
		if err := reinstate("missing", name, "1.3.0"); errs.KindOf(err) != errs.NotFound {
			t.Errorf("an unknown repository: %v", err)
		}
	})
	t.Run("R-NPM-047 the version that nothing contradicts is reinstated", func(t *testing.T) {
		if err := reinstate(repoNPMLocal, name, "1.3.0"); err != nil {
			t.Fatal(err)
		}
		if vs, tags := e.npmDoc(r, repoNPMLocal, name); len(vs) != 1 || vs[0] != "1.3.0" || tags["latest"] != "1.3.0" {
			t.Errorf("document lists %v with tags %v: a package that has no latest tag gets one (R-NPM-016)", vs, tags)
		}
	})
}

// The command of R-NPM-047 runs as `trove admin npm reinstate`: built here and run against the installation's database.
func TestR_NPM_047_AdminNPMReinstateCommand(t *testing.T) {
	e := newFormatsEnv(t)
	r := e.replica("web-1")
	name := "@acme/cli"
	integrity := e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.0.0"})
	e.publish(r, repoNPMLocal, npmPkg{Name: name, Version: "1.1.0"})
	res := e.replay(audit.EventNPMUnpublish, e.unpublishEntry(repoNPMLocal, name, "1.1.0", e.st.Text(`SELECT nv.integrity FROM npm_versions nv JOIN package_versions pv ON pv.id = nv.version_id WHERE pv.version = '1.1.0'`)), time.Now())
	if res.Outcome != journal.Applied {
		t.Fatalf("replay: %q", res.Outcome)
	}
	_ = integrity

	bin := filepath.Join(t.TempDir(), "trove")
	build := exec.Command("go", "build", "-o", bin, "github.com/guided-traffic/trove/cmd/trove")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, append([]string{"admin", "npm", "reinstate"}, args...)...)
		cmd.Env = append(append(os.Environ(), e.st.Environ()...), "TROVE_POD_NAME=admin-cli-test")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(project+"/"+repoNPMLocal, name+"@9.9.9"); err == nil || !strings.Contains(out, "not withheld") {
		t.Fatalf("an unknown version: err %v, output %q", err, out)
	}
	out, err := run(project+"/"+repoNPMLocal, name+"@1.1.0")
	if err != nil || !strings.Contains(out, "reinstated "+name+"@1.1.0") {
		t.Fatalf("reinstate: err %v, output %q", err, out)
	}
	if vs, _ := e.npmDoc(r, repoNPMLocal, name); len(vs) != 2 {
		t.Errorf("versions after the command: %v", vs)
	}
	if n := e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'npm.reinstate' AND actor_type = 'admin-cli'`); n != 1 {
		t.Errorf("%d npm.reinstate events by the admin CLI, want 1", n)
	}
}
