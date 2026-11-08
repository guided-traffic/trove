//go:build integration

package ocivirtual

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/ocistub"
)

// referrers reads the digests and artifact types of a referrers answer.
func referrers(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var idx struct {
		Manifests []struct{ Digest, ArtifactType string }
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range idx.Manifests {
		out[m.Digest] = m.ArtifactType
	}
	return out
}

func TestR_OCI_027_TheReferrersOfADigestAreTheUnionOfTheRemoteMembersWithoutRepeats(t *testing.T) {
	e := newEnv(t, options{})
	signed := e.stubDigest("acme/signed", "1.0")
	direct := func(repo string) testenv.Answer { return e.get(repo, "acme/signed/referrers/"+signed) }

	// Neither upstream has the referrers API: the virtual repository answers 404, as a remote does, so that the client falls back to the tag schema.
	if a := direct(rTwo); a.Status != http.StatusNotFound {
		t.Fatalf("members without the API: %d %s", a.Status, a.Body)
	}
	for _, s := range []*ocistub.Server{e.stubA, e.stubB} {
		if err := s.SetMode(ocistub.Mode{Referrers: true}); err != nil {
			t.Fatal(err)
		}
	}
	one := e.get(rHubA, "acme/signed/referrers/"+signed)
	if one.Status != http.StatusOK {
		t.Fatalf("the remote member: %d %s", one.Status, one.Body)
	}
	want := referrers(t, one.Body)
	if len(want) != 2 {
		t.Fatalf("the stub's subject has %d referrers, want a signature and an SBOM", len(want))
	}
	a := direct(rTwo)
	if a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	got := referrers(t, a.Body)
	if len(got) != 2 {
		t.Fatalf("the union lists %v: the same descriptors from both members are listed once", got)
	}
	for d, at := range want {
		if got[d] != at {
			t.Errorf("referrer %s: %q, want %q", d, got[d], at)
		}
	}
	// The filter is applied and said.
	var some string
	for _, at := range want {
		some = at
		break
	}
	f := e.get(rTwo, "acme/signed/referrers/"+signed+"?artifactType="+url.QueryEscape(some))
	if f.Header.Get("OCI-Filters-Applied") != "artifactType" || len(referrers(t, f.Body)) != 1 {
		t.Errorf("filtered: %q, %s", f.Header.Get("OCI-Filters-Applied"), f.Body)
	}
}

func TestR_OCI_027_ALocalMembersClaimOnAnImageNameKeepsTheRemoteMembersReferrersAway(t *testing.T) {
	e := newEnv(t, options{autoClaim: true})
	signed := e.stubDigest("acme/signed", "1.0")
	if err := e.stubA.SetMode(ocistub.Mode{Referrers: true}); err != nil {
		t.Fatal(err)
	}
	// Pushing the name to docker-local first-claims it for the local member (the name is served by hub-a, so the writer decides the shadowing).
	blob := []byte("{}")
	e.uploadBlob(rLocal, "acme/signed", blob)
	ref := []byte(`{"schemaVersion":2,"mediaType":"` + ctManifest + `","artifactType":"application/vnd.example.note","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"` +
		digestOf(blob) + `","size":2},"layers":[],"subject":{"mediaType":"` + ctManifest + `","digest":"` + signed + `","size":1}}`)
	if a := e.putManifest(rLocal, "acme/signed", digestOf(ref), ref); a.Status != http.StatusCreated {
		t.Fatalf("push the referrer: %d %s", a.Status, a.Body)
	}
	a := e.get(rVirtual, "acme/signed/referrers/"+signed)
	got := referrers(t, a.Body)
	if a.Status != http.StatusOK || len(got) != 1 || got[digestOf(ref)] != "application/vnd.example.note" {
		t.Fatalf("%d %v: the local member claims the name, so the upstream's referrers are not listed", a.Status, got)
	}
}
