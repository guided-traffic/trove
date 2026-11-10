//go:build integration

package importer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	imp "github.com/guided-traffic/trove/internal/importer"
	"github.com/guided-traffic/trove/test/npmstub"
)

// seedNPM puts the packages of the tests into a stub: three versions of imp-a with a beta tag, one of a scoped package and one
// of imp-b.
func seedNPM(t *testing.T, s *npmstub.Server) {
	t.Helper()
	for _, p := range []struct {
		name, version string
		tags          []string
	}{
		{"imp-a", "1.0.0", nil},
		{"imp-a", "1.1.0", nil},
		{"imp-a", "2.0.0-beta.1", []string{"beta"}},
		{"@imp/scoped", "1.0.0", nil},
		{"imp-b", "0.1.0", nil},
	} {
		if err := s.Publish(p.name, p.version, nil, p.tags...); err != nil {
			t.Fatal(err)
		}
	}
}

// document reads the package document a repository serves.
func (e *env) document(repo, name, token string) map[string]any {
	e.t.Helper()
	a := e.st.Do(e.rep, "GET", "/api/npm/it/"+repo+"/"+strings.ReplaceAll(name, "/", "%2f"), token, nil)
	if a.Status != 200 {
		e.t.Fatalf("GET %s: %d %s", name, a.Status, a.Body)
	}
	var doc map[string]any
	if err := json.Unmarshal(a.Body, &doc); err != nil {
		e.t.Fatal(err)
	}
	return doc
}

func integrityOf(doc map[string]any, version string) string {
	v, _ := doc["versions"].(map[string]any)[version].(map[string]any)
	d, _ := v["dist"].(map[string]any)
	s, _ := d["integrity"].(string)
	return s
}

func TestR_IMP_004_FullImportOfAnNPMRegistryTakesTheRoadOfAPublish(t *testing.T) {
	e := newEnv(t)
	seedNPM(t, e.npm)
	names := []string{"imp-a", "@imp/scoped", "imp-b"}
	rep, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.writer, names...)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("the report is not clean: %+v", rep.Packages)
	}
	for name, want := range map[string]int64{"imp-a": 3, "@imp/scoped": 1, "imp-b": 1} {
		if got := e.versions(rNPM, name); got != want {
			t.Errorf("%s: %d versions stored, want %d", name, got, want)
		}
	}
	// The audit event of a publish, written by the target, for every version and by the token's principal (R-IMP-004).
	if got := e.published(); got != 5 {
		t.Errorf("package.published events: %d, want 5", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('npm.publish', 'package.published') AND actor_id = $1::uuid`, e.writerID); got != 5 {
		t.Errorf("events by the importing user: %d, want 5", got)
	}
	// The digest the target stores is the source's.
	src := e.document(rNPM, "imp-a", e.writer)
	srcDoc := e.sourceDoc("imp-a")
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0-beta.1"} {
		if got, want := integrityOf(src, v), integrityOf(srcDoc, v); got == "" || got != want {
			t.Errorf("imp-a@%s: integrity %q in the target, %q in the source", v, got, want)
		}
	}
	// The dist-tags of the source (R-IMP-005).
	tags, _ := src["dist-tags"].(map[string]any)
	if tags["latest"] != "1.1.0" || tags["beta"] != "2.0.0-beta.1" {
		t.Errorf("dist-tags %v, want latest 1.1.0 and beta 2.0.0-beta.1", tags)
	}
}

func TestR_IMP_006_AnImportThatRunsAgainSkipsWhatIsThereAndWritesNothing(t *testing.T) {
	e := newEnv(t)
	seedNPM(t, e.npm)
	names := []string{"imp-a", "imp-b"}
	if _, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.writer, names...); err != nil {
		t.Fatal(err)
	}
	before := e.published()
	rep, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.writer, names...)
	if err != nil || !rep.OK() {
		t.Fatalf("second run: %v %+v", err, rep.Packages)
	}
	if got := pkgOf(t, rep, "imp-a").Count(imp.OutcomeSkipped); got != 3 {
		t.Errorf("imp-a: %d skipped, want 3", got)
	}
	if got := e.published(); got != before {
		t.Errorf("a second run wrote %d publish events", got-before)
	}
}

func TestR_IMP_007_ARunThatWasCutShortResumesWithoutDuplicatesOrGaps(t *testing.T) {
	e := newEnv(t)
	seedNPM(t, e.npm)
	names := []string{"@imp/scoped", "imp-a", "imp-b"}
	state := filepath.Join(t.TempDir(), "state.jsonl")

	open := func() *imp.Journal {
		j, err := imp.OpenJournal(state, "npm", e.npmURL, npmTarget(rNPM))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = j.Close() })
		return j
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.options("npm", e.npmURL, npmTarget(rNPM), e.writer, names...)
	o.Concurrency, o.Journal = 1, open()
	cutAfter(o.Target, 2, cancel)
	rep, err := imp.Run(ctx, o)
	if err == nil {
		t.Fatal("the run was cut short and returned no error")
	}
	if got := e.published(); got != 2 {
		t.Fatalf("after the cut: %d versions published, want 2", got)
	}
	if rep.OK() {
		t.Fatal("a cut run reports no problem")
	}

	o2 := e.options("npm", e.npmURL, npmTarget(rNPM), e.writer, names...)
	o2.Concurrency, o2.Journal = 1, open()
	rep, err = imp.Run(context.Background(), o2)
	if err != nil || !rep.OK() {
		t.Fatalf("the resumed run: %v %+v", err, rep.Packages)
	}
	if !pkgOf(t, rep, "@imp/scoped").Resumed {
		t.Error("the package that was complete before the cut was read again")
	}
	if got := pkgOf(t, rep, "imp-a"); got.Count(imp.OutcomeSkipped) != 1 || got.Count(imp.OutcomeImported) != 2 {
		t.Errorf("imp-a after the resume: %+v", got.Versions)
	}
	if got := e.published(); got != 5 {
		t.Errorf("%d versions published in all, want 5: none twice, none missing", got)
	}
	for name, want := range map[string]int64{"imp-a": 3, "@imp/scoped": 1, "imp-b": 1} {
		if got := e.versions(rNPM, name); got != want {
			t.Errorf("%s: %d versions, want %d", name, got, want)
		}
	}
}

func TestR_IMP_006_AVersionWithOtherContentIsAConflictAndNothingIsOverwritten(t *testing.T) {
	e := newEnv(t)
	// The target holds imp-conflict@1.0.0 as another registry published it ...
	other, err := npmstub.New(npmstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Publish("imp-conflict", "1.0.0", map[string]string{"left-pad": "^1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if rep, err := e.run("npm", e.serveNPM(other), npmTarget(rNPM), e.writer, "imp-conflict"); err != nil || !rep.OK() {
		t.Fatalf("seeding: %v %+v", err, rep)
	}
	stored := integrityOf(e.document(rNPM, "imp-conflict", e.writer), "1.0.0")
	// ... and the registry that is imported holds the same number with other bytes, and a second version.
	if err := e.npm.Publish("imp-conflict", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.npm.Publish("imp-conflict", "1.1.0", nil); err != nil {
		t.Fatal(err)
	}
	before := e.published()
	rep, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.writer, "imp-conflict")
	if err != nil {
		t.Fatal(err)
	}
	if v := pkgOf(t, rep, "imp-conflict"); v.Count(imp.OutcomeConflict) != 1 || v.Count(imp.OutcomeImported) != 1 {
		t.Fatalf("outcomes %+v, want one conflict and one import", v.Versions)
	}
	if rep.OK() {
		t.Error("a conflict is a problem the run reports")
	}
	if got := integrityOf(e.document(rNPM, "imp-conflict", e.writer), "1.0.0"); got != stored {
		t.Errorf("the stored version changed: %s, was %s", got, stored)
	}
	if got := e.published() - before; got != 1 {
		t.Errorf("%d publish events, want 1 (the second version; the conflict wrote nothing)", got)
	}
}

func TestR_IMP_004_ADigestThatTheFileDoesNotHaveIsRefusedAndNothingIsSent(t *testing.T) {
	e := newEnv(t)
	if err := e.npm.Publish("imp-bad", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.npm.SetMode(npmstub.Mode{BadIntegrity: []string{"imp-bad"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.writer, "imp-bad")
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome(t, rep, "imp-bad", "1.0.0"); got.Outcome != imp.OutcomeFailed || got.Code != imp.CodeDigestMismatch {
		t.Fatalf("outcome %+v, want failed digest_mismatch", got)
	}
	if got := e.versions(rNPM, "imp-bad"); got != 0 {
		t.Errorf("%d versions stored of a file that failed its digest", got)
	}
	if got := e.published(); got != 0 {
		t.Errorf("%d publish events for a refused file", got)
	}
}

func TestR_IMP_004_ATokenWithoutThePublishRightIsRefusedPerPackageAndNothingIsStored(t *testing.T) {
	e := newEnv(t)
	seedNPM(t, e.npm)
	rep, err := e.run("npm", e.npmURL, npmTarget(rNPM), e.reader, "imp-a", "imp-b")
	if err != nil {
		t.Fatalf("a refused package does not stop the run: %v", err)
	}
	for _, name := range []string{"imp-a", "imp-b"} {
		p := pkgOf(t, rep, name)
		if p.Count(imp.OutcomeFailed) == 0 || p.Versions[0].Code != imp.CodeForbidden {
			t.Errorf("%s: %+v, want failed forbidden", name, p.Versions)
		}
		if got := e.versions(rNPM, name); got != 0 {
			t.Errorf("%s: %d versions stored by a reader", name, got)
		}
	}
	if rep.OK() {
		t.Error("the report of a refused import is clean")
	}
	// A token the target does not know stops the run before a package is read (R-IMP-006).
	_, err = e.run("npm", e.npmURL, npmTarget(rNPM), "trv_pat_doesnotexist", "imp-a")
	if err == nil || !strings.Contains(err.Error(), "not usable") {
		t.Errorf("unknown token: %v, want the target is not usable", err)
	}
}

func TestR_IMP_003_TheSearchOfTheSourceFindsNothingWhereItHasNoEndpointAndTheNamesStillWork(t *testing.T) {
	e := newEnv(t)
	seedNPM(t, e.npm)
	o := e.options("npm", e.npmURL, npmTarget(rNPM), e.writer, "imp-b")
	q := "imp"
	o.Search = &q
	rep, err := imp.Run(context.Background(), o)
	if err != nil || !rep.OK() {
		t.Fatalf("%v %+v", err, rep)
	}
	if got := e.versions(rNPM, "imp-b"); got != 1 {
		t.Errorf("imp-b: %d versions", got)
	}
}
