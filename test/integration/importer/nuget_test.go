//go:build integration

package importer

import (
	"context"
	"strings"
	"testing"

	imp "github.com/guided-traffic/trove/internal/importer"
	"github.com/guided-traffic/trove/test/nugetstub"
)

func seedNuGet(t *testing.T, s *nugetstub.Server) {
	t.Helper()
	for _, p := range [][2]string{{"Imp.Alpha", "1.0.0"}, {"Imp.Alpha", "1.1.0"}, {"Imp.Alpha", "2.0.0-beta.1"}, {"Imp.Beta", "0.1.0"}} {
		if err := s.Publish(p[0], p[1], nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetListed("Imp.Alpha", "1.1.0", false); err != nil {
		t.Fatal(err)
	}
}

// held reads what the target's registration says of a package.
func (e *env) held(repo, token, name string) map[string]imp.Held {
	e.t.Helper()
	f, _, dst := e.build("nuget", e.nugetURL, nugetTarget(repo), token)
	h, _, err := f.Held(context.Background(), dst, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func TestR_IMP_004_FullImportOfANuGetFeedTakesTheRoadOfAPush(t *testing.T) {
	e := newEnv(t)
	seedNuGet(t, e.nuget)
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.writer, "Imp.Alpha", "Imp.Beta")
	if err != nil || !rep.OK() {
		t.Fatalf("run: %v %+v", err, rep.Packages)
	}
	if got := e.versions(rNuGet, "Imp.Alpha"); got != 3 {
		t.Errorf("Imp.Alpha: %d versions, want 3 (the unlisted one included)", got)
	}
	if got := e.versions(rNuGet, "Imp.Beta"); got != 1 {
		t.Errorf("Imp.Beta: %d versions, want 1", got)
	}
	if got := e.published(); got != 4 {
		t.Errorf("package.published events: %d, want 4", got)
	}
	if got := e.st.Number(`SELECT count(*) FROM audit_event WHERE action IN ('npm.publish', 'package.published') AND actor_id = $1::uuid`, e.writerID); got != 4 {
		t.Errorf("events by the importing user: %d, want 4", got)
	}
	// A token that cannot read the configuration cannot know that a delete only unlists: the unlisted state is not kept and
	// the report says so (R-IMP-005).
	if !e.held(rNuGet, e.writer, "Imp.Alpha")["1.1.0"].Listed {
		t.Error("the unlisted version was unlisted by a token that could not read the repository's deleteMode")
	}
	if notes := strings.Join(pkgOf(t, rep, "Imp.Alpha").Notes, "|"); !strings.Contains(notes, "1.1.0") {
		t.Errorf("notes %q do not name the unlisted version that was not kept", notes)
	}
	// The same import again, with a token that reads the configuration, unlists it: the versions are skipped, the state is set.
	rep, err = e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.admin, "Imp.Alpha")
	if err != nil || !rep.OK() {
		t.Fatalf("second run: %v %+v", err, rep.Packages)
	}
	if got := pkgOf(t, rep, "Imp.Alpha").Count(imp.OutcomeSkipped); got != 3 {
		t.Errorf("second run: %d skipped, want 3", got)
	}
	held := e.held(rNuGet, e.admin, "Imp.Alpha")
	if held["1.1.0"].Listed || !held["1.0.0"].Listed {
		t.Errorf("listing after the second run: %v notes %v", held["1.1.0"].Listed, pkgOf(t, rep, "Imp.Alpha").Notes)
	}
	if got := e.published(); got != 4 {
		t.Errorf("the second run wrote publish events: %d, want 4", got)
	}
}

func TestR_IMP_005_AnUnlistedStateIsNeverReplayedWhereADeleteWouldRemoveTheVersion(t *testing.T) {
	e := newEnv(t)
	seedNuGet(t, e.nuget)
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGetHard), e.admin, "Imp.Alpha")
	if err != nil || !rep.OK() {
		t.Fatalf("run: %v %+v", err, rep.Packages)
	}
	if got := e.versions(rNuGetHard, "Imp.Alpha"); got != 3 {
		t.Errorf("%d versions stored, want 3: the unlisted one must not have been deleted", got)
	}
	if !e.held(rNuGetHard, e.admin, "Imp.Alpha")["1.1.0"].Listed {
		t.Error("the version was unlisted in a repository whose delete removes it")
	}
}

func TestR_IMP_004_APackageHashThatTheFileDoesNotHaveIsRefusedAndNothingIsSent(t *testing.T) {
	e := newEnv(t)
	if err := e.nuget.Publish("Imp.Bad", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.nuget.SetMode(nugetstub.Mode{BadDigest: []string{"Imp.Bad"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.writer, "Imp.Bad")
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome(t, rep, "Imp.Bad", "1.0.0"); got.Outcome != imp.OutcomeFailed || got.Code != imp.CodeDigestMismatch {
		t.Fatalf("outcome %+v, want failed digest_mismatch", got)
	}
	if got := e.versions(rNuGet, "Imp.Bad"); got != 0 {
		t.Errorf("%d versions stored of a file that failed its hash", got)
	}
	if got := e.published(); got != 0 {
		t.Errorf("%d publish events for a refused file", got)
	}
}

func TestR_IMP_006_ANuGetVersionWithOtherContentIsAConflict(t *testing.T) {
	e := newEnv(t)
	other, err := nugetstub.New(nugetstub.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Publish("Imp.Clash", "1.0.0", map[string]string{"Imp.Dep": "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if rep, err := e.run("nuget", e.serveNuGet(other), nugetTarget(rNuGet), e.writer, "Imp.Clash"); err != nil || !rep.OK() {
		t.Fatalf("seeding: %v %+v", err, rep)
	}
	if err := e.nuget.Publish("Imp.Clash", "1.0.0", nil); err != nil {
		t.Fatal(err)
	}
	before := e.published()
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.writer, "Imp.Clash")
	if err != nil {
		t.Fatal(err)
	}
	if got := outcome(t, rep, "Imp.Clash", "1.0.0"); got.Outcome != imp.OutcomeConflict {
		t.Fatalf("outcome %+v, want conflict", got)
	}
	if e.published() != before {
		t.Error("a conflict wrote an event")
	}
}

func TestR_IMP_004_ANuGetTokenWithoutThePushRightIsRefusedAndNothingIsStored(t *testing.T) {
	e := newEnv(t)
	seedNuGet(t, e.nuget)
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.reader, "Imp.Beta")
	if err != nil {
		t.Fatalf("a refused package does not stop the run: %v", err)
	}
	if got := outcome(t, rep, "Imp.Beta", "0.1.0"); got.Outcome != imp.OutcomeFailed || got.Code != imp.CodeForbidden {
		t.Fatalf("outcome %+v, want failed forbidden", got)
	}
	if got := e.versions(rNuGet, "Imp.Beta"); got != 0 {
		t.Errorf("%d versions stored by a reader", got)
	}
}

func TestR_IMP_007_ANuGetRunThatWasCutShortResumes(t *testing.T) {
	e := newEnv(t)
	seedNuGet(t, e.nuget)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := e.options("nuget", e.nugetURL, nugetTarget(rNuGet), e.writer, "Imp.Alpha", "Imp.Beta")
	o.Concurrency = 1
	cutAfter(o.Target, 2, cancel)
	if _, err := imp.Run(ctx, o); err == nil {
		t.Fatal("the run was cut short and returned no error")
	}
	rep, err := e.run("nuget", e.nugetURL, nugetTarget(rNuGet), e.writer, "Imp.Alpha", "Imp.Beta")
	if err != nil || !rep.OK() {
		t.Fatalf("resumed run: %v %+v", err, rep.Packages)
	}
	if got := e.published(); got != 4 {
		t.Errorf("%d versions published in all, want 4: none twice, none missing", got)
	}
}
