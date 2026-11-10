//go:build integration

package ansiblevirtual

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/test/ansiblestub"
)

func TestR_ANS_022_TheVersionListIsTheUnionOfTheMembersNewestFirstAndPagedAfterTheMerge(t *testing.T) {
	e := newEnv(t, options{})
	upstream := e.stubVersions("acme", "tools")
	if a, _ := e.publish(rLocal, e.writer, spec("acme", "tools", "9.0.0")); a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	list, a := e.versions(rVirtual, "acme", "tools", e.reader)
	if a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	got := list.versions()
	if list.Meta.Count != len(upstream)+1 || got[0] != "9.0.0" {
		t.Fatalf("count %d, versions %v: want the stub's %d versions and the local 9.0.0 first", list.Meta.Count, got, len(upstream))
	}
	for _, v := range upstream {
		if !has(got, v.Version) {
			t.Errorf("the stub's %s is not in %v", v.Version, got)
		}
	}
	for _, d := range list.Data {
		if !strings.Contains(d.Href, "/api/ansible/"+"it"+"/"+rVirtual+"/") {
			t.Errorf("href %s names another repository", d.Href)
		}
	}
	// The client's page is cut after the merge; the links name the virtual repository and lead through the whole list.
	var seen []string
	query := "limit=2"
	for range 20 {
		page, a := e.versions(rVirtual, "acme", "tools", e.reader, query)
		if a.Status != http.StatusOK {
			t.Fatalf("%s: %d %s", query, a.Status, a.Body)
		}
		seen = append(seen, page.versions()...)
		if page.Links.Next == nil {
			break
		}
		if !strings.Contains(*page.Links.Next, "/api/ansible/it/"+rVirtual+"/") {
			t.Fatalf("next = %s", *page.Links.Next)
		}
		_, query, _ = strings.Cut(*page.Links.Next, "?")
	}
	if strings.Join(seen, ",") != strings.Join(got, ",") {
		t.Errorf("the pages hold %v, the list %v", seen, got)
	}
}

func TestR_ANS_022_AVersionBothMembersHoldIsServedFromTheLocalMemberAndTheConflictIsCounted(t *testing.T) {
	e := newEnv(t, options{})
	shared := e.stubVersions("acme", "tools")[0]
	s := spec("acme", "tools", shared.Version)
	s.Files["CHANGELOG.md"] = "mine\n"
	a, archive := e.publish(rLocal, e.writer, s)
	if a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	local := collectiontest.Sum(archive)
	if local == shared.SHA256 {
		t.Fatal("the local archive equals the stub's")
	}
	doc := e.get(rVirtual, "v3/collections/acme/tools/versions/"+shared.Version+"/", e.reader)
	var v struct {
		DownloadURL string `json:"download_url"`
		Artifact    struct{ Sha256 string }
	}
	if doc.Status != http.StatusOK || json.Unmarshal(doc.Body, &v) != nil || v.Artifact.Sha256 != local {
		t.Fatalf("version document: %d %s", doc.Status, doc.Body)
	}
	if !strings.HasPrefix(v.DownloadURL, "http://trove.test/api/ansible/it/"+rVirtual+"/v3/artifacts/collections/") {
		t.Errorf("download_url = %s", v.DownloadURL)
	}
	file := e.get(rVirtual, "v3/artifacts/collections/acme-tools-"+shared.Version+".tar.gz", e.reader)
	if file.Status != http.StatusOK || collectiontest.Sum(file.Body) != local {
		t.Fatalf("archive: %d, sha256 %s, want the local %s", file.Status, collectiontest.Sum(file.Body), local)
	}
	// The merge reads the document of the shared version to compare the digests, and counts the difference.
	e.versions(rVirtual, "acme", "tools", e.reader)
	metrics := e.rep.Metrics(t)
	if !strings.Contains(metrics, `trove_ansible_virtual_digest_conflicts_total{repo="it/`+rVirtual+`"} 1`) {
		t.Errorf("the conflict counter is not 1:\n%s", grepLines(metrics, "digest_conflicts"))
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestR_ANS_022_ACollectionOnlyTheRemoteMemberHoldsIsInstalledFromItAndItsArchiveIsFetchedOnce(t *testing.T) {
	e := newEnv(t, options{})
	other := e.stubVersions("other", "stuff")[0]
	file := "other-stuff-" + other.Version + ".tar.gz"
	for range 2 {
		a := e.get(rVirtual, "v3/artifacts/collections/"+file, e.reader)
		if a.Status != http.StatusOK || collectiontest.Sum(a.Body) != other.SHA256 {
			t.Fatalf("%d, sha256 %s, want %s", a.Status, collectiontest.Sum(a.Body), other.SHA256)
		}
	}
	if n := e.stub.Counts().OfKind(ansiblestub.ClassArtifact, http.MethodGet); n != 1 {
		t.Errorf("the stub served %d archive requests for two installs, want one", n)
	}
	// The collection document lists the highest version of the merged list at the address of the virtual repository.
	a := e.get(rVirtual, "v3/collections/other/stuff/", e.reader)
	var c struct {
		Highest struct{ Version, Href string } `json:"highest_version"`
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &c) != nil || c.Highest.Version != other.Version || !strings.Contains(c.Highest.Href, "/it/"+rVirtual+"/") {
		t.Errorf("collection document: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_022_AClaimKeepsTheRemoteMembersNameAwayFromTheMerge(t *testing.T) {
	e := newEnv(t, options{claim: "acme.*"})
	if a, _ := e.publish(rLocal, e.writer, spec("acme", "mine", "1.0.0")); a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	if _, a := e.versions(rVirtual, "acme", "base", e.reader); a.Status != http.StatusNotFound {
		t.Errorf("the remote's acme.base is offered although acme.* is claimed for the local member: %d %s", a.Status, a.Body)
	}
	if list, a := e.versions(rVirtual, "acme", "mine", e.reader); a.Status != http.StatusOK || list.Meta.Count != 1 {
		t.Errorf("the local acme.mine: %d %s", a.Status, a.Body)
	}
	if _, a := e.versions(rVirtual, "other", "stuff", e.reader); a.Status != http.StatusOK {
		t.Errorf("other.stuff is outside the claim: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_024_APublishThroughTheVirtualRepositoryIsStoredInTheTargetAndPolledThroughTheVirtualAddress(t *testing.T) {
	e := newEnv(t, options{})
	a, archive := e.publish(rVirtual, e.writer, spec("mine", "tools", "1.0.0"))
	if a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	var ans struct{ Task string }
	if err := json.Unmarshal(a.Body, &ans); err != nil {
		t.Fatal(err)
	}
	prefix := "http://trove.test/api/ansible/it/" + rVirtual + "/v3/imports/collections/"
	if !strings.HasPrefix(ans.Task, prefix) {
		t.Fatalf("task = %s, want the address on the virtual repository %s", ans.Task, prefix)
	}
	task := e.get(rVirtual, strings.TrimPrefix(ans.Task, "http://trove.test/api/ansible/it/"+rVirtual+"/"), e.reader)
	if task.Status != http.StatusOK || !contains(task, `"state":"completed"`) || !contains(task, `"finished_at"`) {
		t.Fatalf("task: %d %s", task.Status, task.Body)
	}
	// The version is in the local repository and nowhere else, and a subsequent install through the virtual repository gets it.
	if n := e.st.Number(`SELECT count(*) FROM ansible_collection_versions v JOIN repositories r ON r.id = v.repo_id WHERE r.key = $1 AND v.name = 'mine.tools'`, rLocal); n != 1 {
		t.Errorf("%d rows in %s", n, rLocal)
	}
	got := e.get(rVirtual, "v3/artifacts/collections/mine-tools-1.0.0.tar.gz", e.reader)
	if got.Status != http.StatusOK || collectiontest.Sum(got.Body) != collectiontest.Sum(archive) {
		t.Errorf("install: %d", got.Status)
	}
	// The task of the target is looked up through the virtual repository only; the other virtual repository over the target finds it, and
	// the one without a target does not.
	if r := e.get(rReverse, strings.TrimPrefix(ans.Task, "http://trove.test/api/ansible/it/"+rVirtual+"/"), e.reader); r.Status != http.StatusOK {
		t.Errorf("task through ans-rev: %d", r.Status)
	}
	if r := e.get(rNoLocal, strings.TrimPrefix(ans.Task, "http://trove.test/api/ansible/it/"+rVirtual+"/"), e.reader); r.Status != http.StatusNotFound {
		t.Errorf("task through a repository without a target: %d", r.Status)
	}
	// The same publish again is idempotent through the virtual repository.
	if a, _ := e.publish(rVirtual, e.writer, spec("mine", "tools", "1.0.0")); a.Status != http.StatusAccepted {
		t.Errorf("second publish: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_024_AVirtualRepositoryWithoutADeploymentTargetTakesNoPublish(t *testing.T) {
	e := newEnv(t, options{})
	a, _ := e.publish(rNoLocal, e.writer, spec("mine", "tools", "1.0.0"))
	// The decision point refuses the deploy action on a virtual repository that has no target before the format is asked (403, as
	// for npm, Q-NPM-36); the handler's 405 no_deploy_target for a target that is no local repository is proven by the unit test.
	if a.Status != http.StatusForbidden {
		t.Errorf("%d %s", a.Status, a.Body)
	}
}

func TestR_ANS_024_AFirstPublishOfANameARemoteMemberServesIsRefusedWithoutManage(t *testing.T) {
	e := newEnv(t, options{})
	a, _ := e.publish(rVirtual, e.plain, spec("other", "stuff", "77.0.0"))
	if a.Status != http.StatusForbidden || errorCode(a) != "shadows_remote_package" {
		t.Fatalf("without manage: %d %s", a.Status, a.Body)
	}
	if n := e.st.Number(`SELECT count(*) FROM ansible_collection_versions WHERE name = 'other.stuff'`); n != 0 {
		t.Errorf("%d rows after the refusal", n)
	}
	if a, _ := e.publish(rVirtual, e.writer, spec("other", "stuff", "77.0.0")); a.Status != http.StatusAccepted {
		t.Errorf("with manage: %d %s", a.Status, a.Body)
	}
	// A name no remote member serves needs no manage.
	if a, _ := e.publish(rVirtual, e.plain, spec("mine", "fresh", "1.0.0")); a.Status != http.StatusAccepted {
		t.Errorf("a new name: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_023_ACallerWhoMayOnlyReadTheLocalMemberSeesItsVersionsAndNoErrorForTheRemoteOne(t *testing.T) {
	e := newEnv(t, options{})
	if a, _ := e.publish(rLocal, e.writer, spec("acme", "tools", "9.0.0")); a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	list, a := e.versions(rVirtual, "acme", "tools", e.narrow)
	if a.Status != http.StatusOK || strings.Join(list.versions(), ",") != "9.0.0" {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if contains(a, rGalaxy) {
		t.Errorf("a document names a member: %s", a.Body)
	}
	if _, a := e.versions(rVirtual, "other", "stuff", e.narrow); a.Status != http.StatusNotFound {
		t.Errorf("a collection only the unreadable member holds: %d %s", a.Status, a.Body)
	}
	// The member path stays reachable by its own grants only.
	if a := e.get(rGalaxy, "v3/collections/other/stuff/versions/", e.narrow); a.Status == http.StatusOK {
		t.Errorf("the remote member answered a caller without a grant: %d", a.Status)
	}
}

func TestR_ANS_023_AMergeThatCannotAskTheRemoteMemberIsRefusedAsPartialAndCompleteWhenTheStubIsBack(t *testing.T) {
	e := newEnv(t, options{})
	if a, _ := e.publish(rLocal, e.writer, spec("acme", "tools", "9.0.0")); a.Status != http.StatusAccepted {
		t.Fatalf("publish: %d %s", a.Status, a.Body)
	}
	if err := e.stub.SetMode(ansiblestub.Mode{Down: ansiblestub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	// The local member answers and the remote one cannot be asked (nothing of acme.tools is cached): the list would be partial.
	_, a := e.versions(rVirtual, "acme", "tools", e.reader)
	if a.Status != http.StatusServiceUnavailable || errorCode(a) != "upstream_unavailable" {
		t.Fatalf("with the stub down: %d %s", a.Status, a.Body)
	}
	if cs := a.Header.Get("Cache-Status"); !strings.Contains(cs, "detail=partial") {
		t.Errorf("Cache-Status = %q", cs)
	}
	// A collection only the remote member could hold: no member answered, and the failure is the engine's own.
	if _, a := e.versions(rVirtual, "other", "stuff", e.reader); a.Status != http.StatusBadGateway || errorCode(a) != "upstream_unavailable" {
		t.Errorf("no member answered: %d %s", a.Status, a.Body)
	}
	if err := e.stub.SetMode(ansiblestub.Mode{}); err != nil {
		t.Fatal(err)
	}
	list, a := e.versions(rVirtual, "acme", "tools", e.reader)
	if a.Status != http.StatusOK || list.Meta.Count != len(e.stubVersions("acme", "tools"))+1 {
		t.Errorf("after the stub is back: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_022_TheDiscoveryDocumentListsV1OnlyWhenARemoteMemberHasIt(t *testing.T) {
	e := newEnv(t, options{})
	hasV1 := func(repo string) bool {
		a := e.get(repo, "", e.reader)
		var d struct {
			Available map[string]string `json:"available_versions"`
		}
		if a.Status != http.StatusOK || json.Unmarshal(a.Body, &d) != nil {
			t.Fatalf("%s: %d %s", repo, a.Status, a.Body)
		}
		_, v1 := d.Available["v1"]
		return v1
	}
	if !hasV1(rVirtual) {
		t.Error("v1 is not listed by a virtual repository over the stub")
	}
	if hasV1(rLocalV) {
		t.Error("v1 is listed by a virtual repository over a local member alone")
	}
}

func TestR_ANS_022_ARoleIdNamesItsMemberAndTheVersionsAndTheArchiveFollowIt(t *testing.T) {
	e := newEnv(t, options{})
	a := e.get(rVirtual, "v1/roles/?owner__username=geerlingguy&name=pip", e.reader)
	var found struct {
		Results []struct {
			ID      string `json:"id"`
			Related struct{ Versions string }
		}
	}
	if a.Status != http.StatusOK || json.Unmarshal(a.Body, &found) != nil || len(found.Results) != 1 {
		t.Fatalf("lookup: %d %s", a.Status, a.Body)
	}
	id := found.Results[0].ID
	if !strings.HasPrefix(id, rGalaxy+"-") {
		t.Fatalf("id = %q, want %q followed by the upstream's id", id, rGalaxy+"-")
	}
	vs := e.get(rVirtual, "v1/roles/"+id+"/versions/?page_size=50", e.reader)
	var versions struct {
		Results []struct {
			Name        string `json:"name"`
			DownloadURL string `json:"download_url"`
		}
	}
	if vs.Status != http.StatusOK || json.Unmarshal(vs.Body, &versions) != nil || len(versions.Results) == 0 {
		t.Fatalf("versions: %d %s", vs.Status, vs.Body)
	}
	first := versions.Results[0]
	if !strings.Contains(first.DownloadURL, "/api/ansible/it/"+rVirtual+"/v1/roles/archives/"+id+"/") || contains(vs, ansiblestub.DefaultGitHubHost) {
		t.Errorf("download_url = %s", first.DownloadURL)
	}
	archive := e.get(rVirtual, "v1/roles/archives/"+id+"/"+first.Name+".tar.gz", e.reader)
	if archive.Status != http.StatusOK || len(archive.Body) == 0 {
		t.Errorf("archive: %d", archive.Status)
	}
	// An id of a member the repository does not have, and one without a member, are 404 in the v1 shape.
	for _, bad := range []string{"gone-1", "1", rLocal + "-1"} {
		a := e.get(rVirtual, "v1/roles/"+bad+"/versions/", e.reader)
		if a.Status != http.StatusNotFound || !contains(a, `"default"`) {
			t.Errorf("%s: %d %s", bad, a.Status, a.Body)
		}
	}
}

func TestR_AUTHZ_005_AVersionTheCallersPathGrantHidesIsInNoDocumentOfTheVirtualRepository(t *testing.T) {
	e := newEnv(t, options{})
	for _, v := range []string{"8.0.0", "9.0.0"} {
		if a, _ := e.publish(rLocal, e.writer, spec("acme", "tools", v)); a.Status != http.StatusAccepted {
			t.Fatalf("publish %s: %d %s", v, a.Status, a.Body)
		}
	}
	all, _ := e.versions(rVirtual, "acme", "tools", e.reader)
	list, a := e.versions(rVirtual, "acme", "tools", e.pathed)
	if a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if has(list.versions(), "9.0.0") || !has(list.versions(), "8.0.0") || list.Meta.Count != all.Meta.Count-2 {
		t.Errorf("the list of the narrowed caller is %v (count %d); the reader's is %v (count %d)", list.versions(), list.Meta.Count, all.versions(), all.Meta.Count)
	}
	// A local version: no version document, no archive, and the collection document names the newest version the caller may read.
	if a := e.get(rVirtual, "v3/collections/acme/tools/versions/9.0.0/", e.pathed); a.Status != http.StatusNotFound {
		t.Errorf("the version document of a hidden version: %d %s", a.Status, a.Body)
	}
	if a := e.get(rVirtual, "v3/artifacts/collections/acme-tools-9.0.0.tar.gz", e.pathed); a.Status != http.StatusNotFound && a.Status != http.StatusForbidden {
		t.Errorf("the archive of a hidden version: %d", a.Status)
	}
	if a := e.get(rVirtual, "v3/collections/acme/tools/versions/8.0.0/", e.pathed); a.Status != http.StatusOK {
		t.Errorf("a version the caller may read: %d %s", a.Status, a.Body)
	}
	var c struct {
		Highest struct{ Version string } `json:"highest_version"`
	}
	ca := e.get(rVirtual, "v3/collections/acme/tools/", e.pathed)
	if ca.Status != http.StatusOK || json.Unmarshal(ca.Body, &c) != nil || c.Highest.Version == "9.0.0" {
		t.Errorf("collection document of the narrowed caller: %d %s", ca.Status, ca.Body)
	}
	// A remote version is hidden the same way, in the list of the same collection.
	if has(list.versions(), e.hiddenRemote) || list.Meta.Count != all.Meta.Count-2 {
		t.Errorf("the remote version %s is in the narrowed caller's list %v", e.hiddenRemote, list.versions())
	}
	if a := e.get(rVirtual, "v3/collections/acme/tools/versions/"+e.hiddenRemote+"/", e.pathed); a.Status != http.StatusNotFound {
		t.Errorf("the version document of a hidden remote version: %d", a.Status)
	}
	// The reader still sees everything: the merged document is shared and the view is the caller's.
	if !has(all.versions(), "9.0.0") || !has(all.versions(), e.hiddenRemote) {
		t.Errorf("the reader lost a version: %v", all.versions())
	}
}
