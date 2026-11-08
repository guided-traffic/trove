//go:build integration

package npm

import (
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// The digest an npm remote compares a tarball with (R-REPO-041, R-NPM-025). The repositories are rows of the seed, so the
// policies ignore and pass-through, which the API refuses for npm (Q-NPM-79), reach the engine and its own rule is what is tested.

// integritySection is the integrity member of a remote with the policy.
func integritySection(policy string) map[string]any {
	return map[string]any{
		"integrity": map[string]any{"checksumPolicy": policy, "blockUnexpectedContentTypes": true, "allowedContentTypes": []any{}},
	}
}

const itemCount = `SELECT count(*) FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path = $2`

// tamperedThenCorrect makes the upstream announce an integrity no tarball has, expects the refusal and no item in the remote, then
// restores the upstream and expects the correct tarball, stored once.
func (r *renv) tamperedThenCorrect(t *testing.T, rep *testenv.Replica, repo string, p *corpus.Package, label string) {
	t.Helper()
	v := p.Versions[0].Version
	path := p.Name + "/-/" + corpus.TarballFile(p.Name, v)
	if err := r.stub.SetMode(npmstub.Mode{BadIntegrity: []string{p.Name}}); err != nil {
		t.Fatal(err)
	}
	a := r.tarball(rep, repo, p.Name, v, r.tokA)
	if a.Status != 502 || code(t, a) != "upstream_integrity_mismatch" {
		t.Fatalf("a tampered tarball under %s: %d %.300s", label, a.Status, a.Body)
	}
	if n := r.st.Number(itemCount, rRemote, path); n != 0 {
		t.Errorf("%d items of the refused tarball under %s", n, label)
	}
	if err := r.stub.SetMode(npmstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2200 * time.Millisecond)
	b := r.tarball(rep, repo, p.Name, v, r.tokA)
	if b.Status != 200 || integrityOf(b.Body) != p.Versions[0].Dist.Integrity {
		t.Errorf("the correct tarball under %s: %d %.200s", label, b.Status, b.Body)
	}
	if n := r.st.Number(itemCount, rRemote, path); n != 1 {
		t.Errorf("%d items of the served tarball under %s, want 1", n, label)
	}
}

func TestR_REPO_041_NpmAlwaysVerifiesTheAnnouncedIntegrityWhateverTheChecksumPolicySays(t *testing.T) {
	for _, policy := range []string{"verify-if-available", "require", "ignore", "pass-through"} {
		t.Run(policy, func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2, Remote: integritySection(policy)})
			rep := r.start(1)[0]
			r.tamperedThenCorrect(t, rep, rRemote, upstreamPackage(t, 3), policy)
		})
	}
	// The fill of a virtual repository goes through the same engine request: the loosest setting of its member.
	t.Run("through a virtual repository over a pass-through remote", func(t *testing.T) {
		r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2, Remote: integritySection("pass-through")})
		rep := r.start(1)[0]
		r.tamperedThenCorrect(t, rep, rAll, upstreamPackage(t, 3), "a virtual repository over a pass-through remote")
	})
}

func TestR_REPO_041_NpmVerifiesTheStrongestDigestOfAnIntegrityThatHasNoSha512(t *testing.T) {
	for _, algo := range []string{"sha256", "sha1"} {
		t.Run(algo+" only: a tampered tarball is refused and a correct one served", func(t *testing.T) {
			r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
			rep := r.start(1)[0]
			p := upstreamPackage(t, 3)
			v := p.Versions[0].Version
			algos := map[string]string{p.Name: algo}
			// dist.shasum is the real SHA-1 and would accept the bytes: the integrity decides.
			if err := r.stub.SetMode(npmstub.Mode{BadIntegrity: []string{p.Name}, IntegrityAlgorithm: algos}); err != nil {
				t.Fatal(err)
			}
			if a := r.tarball(rep, rRemote, p.Name, v, r.tokA); a.Status != 502 || code(t, a) != "upstream_integrity_mismatch" {
				t.Fatalf("%d %.300s", a.Status, a.Body)
			}
			if n := r.st.Number(itemCount, rRemote, p.Name+"/-/"+corpus.TarballFile(p.Name, v)); n != 0 {
				t.Errorf("%d items of the refused tarball", n)
			}
			if err := r.stub.SetMode(npmstub.Mode{IntegrityAlgorithm: algos}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2200 * time.Millisecond)
			if b := r.tarball(rep, rRemote, p.Name, v, r.tokA); b.Status != 200 || integrityOf(b.Body) != p.Versions[0].Dist.Integrity {
				t.Errorf("the correct tarball: %d %.200s", b.Status, b.Body)
			}
		})
	}
	t.Run("sha384 only: refused as unverifiable, nothing stored", func(t *testing.T) {
		r := newRemoteEnv(t, remoteOptions{MetadataTTL: 2})
		rep := r.start(1)[0]
		p := upstreamPackage(t, 3)
		v := p.Versions[0].Version
		if err := r.stub.SetMode(npmstub.Mode{IntegrityAlgorithm: map[string]string{p.Name: "sha384"}}); err != nil {
			t.Fatal(err)
		}
		a := r.tarball(rep, rRemote, p.Name, v, r.tokA)
		if a.Status != 502 || code(t, a) != "upstream_digest_missing" {
			t.Fatalf("%d %.300s", a.Status, a.Body)
		}
		if n := r.st.Number(itemCount, rRemote, p.Name+"/-/"+corpus.TarballFile(p.Name, v)); n != 0 {
			t.Errorf("%d items", n)
		}
	})
}
