package nuget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

const (
	acmePath   = "acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"
	acmeNuspec = "acme.widgets/1.2.3/acme.widgets.nuspec"
)

// TestR_NUGET_006_APushStoresThePackageItsNuspecAndTheRows: a multipart push answers 201 with
// an empty body, stores the .nupkg byte for byte and the .nuspec as a generated item on the
// canonical paths, writes the NuGet row with the metadata, the sort key and the search parts,
// raises the revision once and records one published event with its journal entry.
func TestR_NUGET_006_APushStoresThePackageItsNuspecAndTheRows(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := buildNupkg(t, "Acme.Widgets", "1.2.3.0", withDep("Newtonsoft.Json", "[13.0.1,)"), withType("Template"))
	r := h.push(t, pkg)
	if r.Code != http.StatusCreated || r.Body.Len() != 0 {
		t.Fatalf("status %d, body %q, want 201 and none", r.Code, r.Body.String())
	}
	it, ok, err := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: acmePath})
	if err != nil || !ok {
		t.Fatalf("the package item: found %v, err %v", ok, err)
	}
	if !bytes.Equal(it.Digests.SHA512, sha512Of(pkg)) {
		t.Error("the stored package is not the pushed one, byte for byte")
	}
	if !h.hasItem(t, acmeNuspec) {
		t.Error("the generated .nuspec is not stored")
	}
	if got := len(h.st.Published); got != 1 {
		t.Fatalf("%d published rows, want 1", got)
	}
	v := h.st.Published[0]
	if v.LowerID != "acme.widgets" || v.DeclaredID != "Acme.Widgets" || v.LowerVersion != "1.2.3" || v.OriginalVersion != "1.2.3.0" {
		t.Errorf("identity = %q %q %q %q", v.LowerID, v.DeclaredID, v.LowerVersion, v.OriginalVersion)
	}
	if len(v.SortKey) == 0 || v.IDTokens != "acme widgets" || v.Tags != "widgets tools" || v.Origin != rows.OriginPush {
		t.Errorf("sort key %x, id tokens %q, tags %q, origin %q", v.SortKey, v.IDTokens, v.Tags, v.Origin)
	}
	if len(v.PackageTypes) != 1 || v.PackageTypes[0] != "template" {
		t.Errorf("package types = %v", v.PackageTypes)
	}
	var md rows.Metadata
	if err := json.Unmarshal(v.Metadata, &md); err != nil {
		t.Fatal(err)
	}
	if md.Authors != "Acme" || len(md.DependencyGroups) != 1 || md.DependencyGroups[0].TargetFramework != "net8.0" ||
		md.DependencyGroups[0].Dependencies[0].ID != "Newtonsoft.Json" || md.DependencyGroups[0].Dependencies[0].Version != "[13.0.1,)" {
		t.Errorf("metadata = %s", v.Metadata)
	}
	if v.FullVersion != "1.2.3" {
		t.Errorf("full version = %q, want the full normalized form of 1.2.3.0", v.FullVersion)
	}
	if len(v.Files) != 1 || v.Files[0].Path != acmePath || v.Files[0].Size != int64(len(pkg)) {
		t.Errorf("files = %+v", v.Files)
	}
	if v.Audit.RepoKey != "it/main" || v.Audit.Actor.ID != kitSubject.ID {
		t.Errorf("audit = %+v", v.Audit)
	}
	if len(h.st.Journal) != 1 || !strings.Contains(h.st.Journal[0], `"id":"acme.widgets"`) || !strings.Contains(h.st.Journal[0], `"version":"1.2.3"`) {
		t.Errorf("journal = %v", h.st.Journal)
	}
	if rev := h.RevSeq("acme.widgets"); rev != 1 {
		t.Errorf("rev_seq = %d, want 1 (one transaction raises it once)", rev)
	}
	// The package writer signals the early eviction of merged documents itself (Q-ARCH-259): the
	// format declares no area for a change that raised a package row and calls no Notify.
	if notes := h.Notifications(); len(notes) != 1 || notes[0].Area != catalog.MetadataChangedArea {
		t.Errorf("notifications = %+v, want one metadata_changed", notes)
	}
}

// TestR_NUGET_006_ABareBodyIsTakenAsThePackage: a body that is not multipart/form-data is the
// raw package, for scripted uploads with curl.
func TestR_NUGET_006_ABareBodyIsTakenAsThePackage(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := buildNupkg(t, "Acme.Widgets", "1.0.0")
	resp, err := h.Serve(context.Background(), http.MethodPut, "v3/package", bytes.NewReader(pkg), false)
	if err != nil || resp.Code != http.StatusCreated {
		t.Fatalf("status %d, err %v: %s", resp.Code, err, resp.Body.String())
	}
}

// TestR_NUGET_006_FurtherPartsAreIgnored: the first part is the package; the other parts, their
// names and headers change nothing.
func TestR_NUGET_006_FurtherPartsAreIgnored(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	body, ct := multipartBody(t, buildNupkg(t, "Acme.Widgets", "1.0.0"), "garbage", strings.Repeat("x", 1<<16))
	resp, err := h.Serve(context.Background(), http.MethodPut, "v3/package", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
	if err != nil || resp.Code != http.StatusCreated {
		t.Fatalf("status %d, err %v: %s", resp.Code, err, resp.Body.String())
	}
}

// TestR_NUGET_006_AMultipartPreambleIsBounded: a body that runs on for longer than the bound
// before its first part starts is refused, and not read to its end looking for the boundary.
func TestR_NUGET_006_AMultipartPreambleIsBounded(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	body := strings.Repeat("preamble line\r\n", 1<<16) + "--xx\r\nContent-Disposition: form-data; name=\"package\"\r\n\r\n" + string(buildNupkg(t, "Acme.Widgets", "1.0.0")) + "\r\n--xx--\r\n"
	resp, err := h.Serve(context.Background(), http.MethodPut, "v3/package", strings.NewReader(body), false, func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=xx") })
	if err != nil || resp.Code != http.StatusBadRequest {
		t.Fatalf("status %d, err %v: %s", resp.Code, err, resp.Body.String())
	}
}

// TestR_NUGET_006_AMultipartBodyWithoutAPartIsRefused: a form without a part, and one without a
// boundary, are 400 and store nothing.
func TestR_NUGET_006_AMultipartBodyWithoutAPartIsRefused(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	for name, c := range map[string]struct{ ct, body string }{
		"no part":     {`multipart/form-data; boundary=xx`, "--xx--\r\n"},
		"no boundary": {`multipart/form-data`, "--xx--\r\n"},
		"not a form":  {`multipart/form-data; boundary=xx`, "this is no multipart body"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := h.Serve(context.Background(), http.MethodPut, "v3/package", strings.NewReader(c.body), false, func(r *http.Request) { r.Header.Set("Content-Type", c.ct) })
			if err != nil || resp.Code != http.StatusBadRequest {
				t.Fatalf("status %d, err %v: %s", resp.Code, err, resp.Body.String())
			}
		})
	}
	if len(h.st.Published) != 0 {
		t.Error("a refused push stored a version")
	}
}

// TestR_NUGET_005_PackagesThatBreakTheRulesAre400: the fixture packages of R-NUGET-005 answer
// 400 with their code, and nothing is stored: no .nuspec, a version without its label,
// a dependency range of two numbers, an ID with two dots, an ID outside ASCII, a symbol package
// and not a ZIP at all.
func TestR_NUGET_005_PackagesThatBreakTheRulesAre400(t *testing.T) {
	cases := []struct {
		name string
		pkg  []byte
		code string
	}{
		{"no nuspec", buildNupkg(t, "Acme.Widgets", "1.0.0", withoutNuspec()), "nuget_invalid_package"},
		{"version without its label", buildNupkg(t, "Acme.Widgets", "1.0.0-"), "nuget_invalid_version"},
		{"range of two numbers", buildNupkg(t, "Acme.Widgets", "1.0.0", withDep("Other", "(1.0)")), "nuget_invalid_version_range"},
		{"dependency ID", buildNupkg(t, "Acme.Widgets", "1.0.0", withDep("Other..Id", "1.0.0")), "nuget_invalid_id"},
		{"ID with two dots", buildNupkg(t, "Acme..Widgets", "1.0.0"), "nuget_invalid_id"},
		{"ID outside ASCII", buildNupkg(t, "Acme.Wïdgets", "1.0.0"), "nuget_invalid_id"},
		{"symbol package", buildNupkg(t, "Acme.Widgets", "1.0.0", withType("SymbolsPackage")), "nuget_invalid_package"},
		{"entry that leaves the package", buildNupkg(t, "Acme.Widgets", "1.0.0", withEntry("..\\evil.dll", []byte("x"))), "nuget_invalid_package"},
		{"not a zip", []byte("this is not an archive"), "nuget_invalid_package"},
		{"empty", nil, "nuget_invalid_package"},
	}
	h := newWriteHost(t, formattest.HostOptions{})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.push(t, c.pkg)
			if r.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", r.Code, r.Body.String())
			}
			if got := code(t, r); got != c.code {
				t.Errorf("code %q, want %q", got, c.code)
			}
		})
	}
	if len(h.st.Published) != 0 || h.RevSeq("acme.widgets") != 0 {
		t.Error("a refused push changed the repository")
	}
	r := h.push(t, buildNupkg(t, "Acme.Widgets", "1.0.0", withType("SymbolsPackage")))
	if !strings.Contains(r.Body.String(), "symbolpackage") {
		t.Errorf("the refusal of a symbol package does not name the symbol publish resource: %s", r.Body.String())
	}
}

// TestR_NUGET_006_IdenticalBytesOfAStoredVersionAnswer201AndChangeNothing: the repeat of a push
// whose answer was lost answers 201, writes no row, raises no revision and records no event.
func TestR_NUGET_006_IdenticalBytesOfAStoredVersionAnswer201AndChangeNothing(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := h.mustPush(t, "Acme.Widgets", "1.2.3")
	rev, journal, notes := h.RevSeq("acme.widgets"), len(h.st.Journal), len(h.Notifications())
	for i := range 3 {
		r := h.push(t, pkg)
		if r.Code != http.StatusCreated || r.Body.Len() != 0 {
			t.Fatalf("repeat %d: status %d, body %q", i, r.Code, r.Body.String())
		}
	}
	if len(h.st.Published) != 1 || len(h.st.Journal) != journal {
		t.Errorf("published %d, journal %d -> %d: a replay wrote a row or an event", len(h.st.Published), journal, len(h.st.Journal))
	}
	if got := h.RevSeq("acme.widgets"); got != rev {
		t.Errorf("rev_seq %d -> %d: a replay raised the revision", rev, got)
	}
	if got := len(h.Notifications()); got != notes {
		t.Errorf("a replay sent %d notifications", got-notes)
	}
}

// TestR_NUGET_006_OtherBytesOfAStoredVersionAnswer409: a rebuilt package under the number of a
// stored version is refused with nuget_version_exists, in any spelling of the version.
func TestR_NUGET_006_OtherBytesOfAStoredVersionAnswer409(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.2.3")
	for _, spelling := range []string{"1.2.3", "1.2.3.0", "01.02.03"} {
		r := h.push(t, buildNupkg(t, "ACME.WIDGETS", spelling, withDescription("rebuilt")))
		if r.Code != http.StatusConflict || code(t, r) != errs.CodeNuGetVersionExists {
			t.Fatalf("%s: status %d: %s", spelling, r.Code, r.Body.String())
		}
	}
	if len(h.st.Published) != 1 {
		t.Error("a refused push stored a version")
	}
}

// TestR_NUGET_006_ADeletedNumberIsNeverAcceptedAgain: after a hard delete the number is
// tombstoned and the identical bytes are refused like any other, with nuget_version_deleted;
// another number of the package is accepted.
func TestR_NUGET_006_ADeletedNumberIsNeverAcceptedAgain(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.deleteMode(DeleteModeDelete)
	pkg := h.mustPush(t, "Acme.Widgets", "1.2.3")
	if r := h.do(t, http.MethodDelete, "v3/package/acme.widgets/1.2.3"); r.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d: %s", r.Code, r.Body.String())
	}
	for name, p := range map[string][]byte{"identical bytes": pkg, "rebuilt": buildNupkg(t, "Acme.Widgets", "1.2.3", withDescription("again"))} {
		r := h.push(t, p)
		if r.Code != http.StatusConflict || code(t, r) != errs.CodeNuGetVersionDeleted {
			t.Errorf("%s: status %d: %s", name, r.Code, r.Body.String())
		}
	}
	h.mustPush(t, "Acme.Widgets", "1.2.5")
}

// TestR_NUGET_006_AReservedNumberAdmitsOnlyItsBytes: the tombstone a restore's replay wrote for
// a version published after the snapshot refuses other bytes with nuget_version_reserved and
// admits the package whose SHA-512 it holds, removing itself in that push's transaction.
func TestR_NUGET_006_AReservedNumberAdmitsOnlyItsBytes(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := buildNupkg(t, "Acme.Widgets", "3.0.0")
	h.st.SeedTombstone(h.Ref().ID, "acme.widgets", "3.0.0", sha512Of(pkg), rows.TombstoneReserved)
	r := h.push(t, buildNupkg(t, "Acme.Widgets", "3.0.0", withDescription("rebuilt")))
	if r.Code != http.StatusConflict || code(t, r) != errs.CodeNuGetVersionReserved {
		t.Fatalf("rebuilt: status %d: %s", r.Code, r.Body.String())
	}
	if r := h.push(t, pkg); r.Code != http.StatusCreated {
		t.Fatalf("the original: status %d: %s", r.Code, r.Body.String())
	}
	if _, found, _ := h.st.Tombstone(context.Background(), nil, h.Ref().ID, "acme.widgets", "3.0.0"); found {
		t.Error("the reservation stays after the package it was kept for arrived")
	}
	if r := h.push(t, buildNupkg(t, "Acme.Widgets", "3.0.0", withDescription("again"))); code(t, r) != errs.CodeNuGetVersionExists {
		t.Errorf("after the original: %s", r.Body.String())
	}
}

// TestR_NUGET_005_TheFirstPushFixesTheDisplayNameOfTheID: any casing is accepted, the version
// that created the package fixes its display name, and each version keeps its own casing
// (Q-NUGET-5).
func TestR_NUGET_005_TheFirstPushFixesTheDisplayNameOfTheID(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	h.mustPush(t, "Acme.Widgets", "1.0.0")
	h.mustPush(t, "ACME.WIDGETS", "1.1.0")
	row, ok, err := h.Packages().Package(context.Background(), h.Ref(), "acme.widgets")
	if err != nil || !ok || row.DisplayName != "Acme.Widgets" {
		t.Fatalf("display name = %q (found %v, err %v), want Acme.Widgets", row.DisplayName, ok, err)
	}
	if got := h.st.Published[1].DeclaredID; got != "ACME.WIDGETS" {
		t.Errorf("the second version declares %q", got)
	}
}

// TestR_NUGET_006_ThePermissionIsDecidedOnTheDeclaredID: the ID is the one the package
// declares, so a caller without deploy on it is refused 403 after the body was read and nothing
// is stored; the ownership is asked before the number is looked at.
func TestR_NUGET_006_ThePermissionIsDecidedOnTheDeclaredID(t *testing.T) {
	denied := errs.E(errs.Forbidden, errs.CodeForbidden, "no")
	h := newWriteHost(t, formattest.HostOptions{
		Allow: func(t formats.Target) error {
			if t.Package == "acme.secret" {
				return denied
			}
			return nil
		},
	})
	if r := h.push(t, buildNupkg(t, "Acme.Secret", "1.0.0")); r.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	h.mustPush(t, "Acme.Widgets", "1.0.0")
	if got := len(h.st.Published); got != 1 {
		t.Errorf("%d versions stored, want 1", got)
	}
	checks := h.OwnershipChecks()
	if len(checks) == 0 || !checks[len(checks)-1].Write.FirstVersion || !checks[len(checks)-1].Write.Publish {
		t.Errorf("ownership checks = %+v, want a publish of a first version", checks)
	}
}

// TestR_NUGET_006_APushIsRefusedWhenTheOwnershipRuleSaysNo: the ownership of an ID decides in
// the early check and again in the commit transaction, which rolls everything back.
func TestR_NUGET_006_APushIsRefusedWhenTheOwnershipRuleSaysNo(t *testing.T) {
	owned := errs.E(errs.Forbidden, errs.CodePackageOwnedByOtherOwner, "owned")
	for name, o := range map[string]formattest.HostOptions{
		"early":  {Precheck: func(formats.Target, formats.OwnershipWrite) error { return owned }},
		"commit": {Claim: func(formats.Target, formats.OwnershipWrite) error { return owned }},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWriteHost(t, o)
			r := h.push(t, buildNupkg(t, "Acme.Widgets", "1.0.0"))
			if r.Code != http.StatusForbidden {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			if len(h.st.Published) != 0 || h.hasItem(t, "acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg") {
				t.Error("a refused push left a version or an item behind")
			}
		})
	}
}

// shadowResolver is a Resolver whose shadowing check says what a test wants.
type shadowResolver struct {
	formats.Resolver
	out formats.ShadowOutcome
}

func (s shadowResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return s.out, nil
}

// TestR_NUGET_006_TheShadowingCheckRefusesAFirstPush: the first push of an ID that a remote
// member of a containing virtual repository serves is 403 shadows_remote_package without
// manage, and 503 shadow_check_unavailable with a Retry-After of at most 30 seconds when no
// member serves it and one cannot be asked; nothing is stored in either case.
func TestR_NUGET_006_TheShadowingCheckRefusesAFirstPush(t *testing.T) {
	refusal := errs.E(errs.Forbidden, errs.CodeShadowsRemotePackage, "serves it")
	for name, c := range map[string]struct {
		out    formats.ShadowOutcome
		status int
		code   string
	}{
		"served by a remote": {formats.ShadowOutcome{Refusal: refusal}, http.StatusForbidden, errs.CodeShadowsRemotePackage},
		"cannot be asked":    {formats.ShadowOutcome{RetryAfter: 90 * time.Second}, http.StatusServiceUnavailable, errs.CodeShadowCheckUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := newWriteHost(t, formattest.HostOptions{Resolver: shadowResolver{out: c.out}})
			r := h.push(t, buildNupkg(t, "Newtonsoft.Json", "99.0.0"))
			if r.Code != c.status || code(t, r) != c.code {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			if c.status == http.StatusServiceUnavailable && r.Header().Get("Retry-After") != "30" {
				t.Errorf("Retry-After = %q, want 30 (capped)", r.Header().Get("Retry-After"))
			}
			if len(h.st.Published) != 0 || h.hasItem(t, "newtonsoft.json/99.0.0/newtonsoft.json.99.0.0.nupkg") {
				t.Error("a refused first push stored something")
			}
		})
	}
}

// TestR_NUGET_006_TheShadowingCheckAsksOnlyForAFirstVersion: a push into an ID the repository
// holds a version of asks nothing, and a first push that proceeds hands the pairs it found to
// the event of the commit.
func TestR_NUGET_006_TheShadowingCheckAsksOnlyForAFirstVersion(t *testing.T) {
	asked := 0
	res := &countingResolver{asked: &asked, out: formats.ShadowOutcome{Proceed: true, Shadowed: []formats.ShadowPair{{Virtual: "nuget-all", Remote: "nuget-org"}}}}
	h := newWriteHost(t, formattest.HostOptions{Resolver: res})
	h.mustPush(t, "Acme.Widgets", "1.0.0")
	h.mustPush(t, "Acme.Widgets", "1.1.0")
	if asked != 1 {
		t.Errorf("the check was asked %d times, want 1 (the first version)", asked)
	}
	first, second := h.st.Published[0].Shadowing, h.st.Published[1].Shadowing
	if len(first.Shadowed) != 1 || first.Shadowed[0].Remote != "nuget-org" || len(second.Shadowed) != 0 {
		t.Errorf("shadowing of the commits: %+v, %+v", first, second)
	}
}

type countingResolver struct {
	formats.Resolver
	asked *int
	out   formats.ShadowOutcome
}

func (c *countingResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	*c.asked++
	return c.out, nil
}

// TestR_NUGET_006_AnUploadAroundThePushRouteIsRefused: a commit that is not a push (the upload
// of the management API, copy) reaches OnCommit with no payload and is refused with the 501 of
// Q-NUGET-32, which rolls the item back.
func TestR_NUGET_006_AnUploadAroundThePushRouteIsRefused(t *testing.T) {
	h := newWriteHost(t, formattest.HostOptions{})
	pkg := buildNupkg(t, "Acme.Widgets", "1.2.3")
	rq, err := h.NewRequest(context.Background(), http.MethodPut, "v3/package", nil)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := h.Ingest().Spool(context.Background(), rq, bytes.NewReader(pkg), formats.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Ingest().Commit(context.Background(), rq, sp, formats.Commit{Path: formats.Path{Path: acmePath}})
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.NotImplemented {
		t.Fatalf("Commit = %v, want 501", err)
	}
	if h.hasItem(t, acmePath) {
		t.Error("the refused upload left its item behind")
	}
}

// TestR_NUGET_004_ADependencyRangeWithASemVer2BoundMakesAPushedVersionSemVer2Specific: a
// version whose own number is SemVer 1 is SemVer 2.0.0-specific when a bound of one of its
// dependency ranges is (a dotted pre-release label or build metadata); a range whose bounds
// are SemVer 1 leaves it SemVer 1. The cases are the table the recomputation of stored rows
// decides alike (version/testdata/dependency-semver2.json, Q-NUGET-93).
func TestR_NUGET_004_ADependencyRangeWithASemVer2BoundMakesAPushedVersionSemVer2Specific(t *testing.T) {
	raw, err := os.ReadFile("version/testdata/dependency-semver2.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Name    string `json:"name"`
			Range   string `json:"range"`
			Semver2 bool   `json:"semver2"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil || len(table.Cases) == 0 {
		t.Fatalf("the case table: %d cases, %v", len(table.Cases), err)
	}
	for _, c := range table.Cases {
		t.Run(c.Name, func(t *testing.T) {
			h := newWriteHost(t, formattest.HostOptions{})
			h.mustPush(t, "Acme.Widgets", "1.0.0", withDep("Newtonsoft.Json", "[13.0.1,)"), withDep("Acme.Core", c.Range))
			if got := len(h.st.Published); got != 1 {
				t.Fatalf("%d published rows, want 1", got)
			}
			if got := h.st.Published[0].IsSemver2; got != c.Semver2 {
				t.Errorf("is_semver2 = %v for a dependency on %q, want %v", got, c.Range, c.Semver2)
			}
		})
	}
}
