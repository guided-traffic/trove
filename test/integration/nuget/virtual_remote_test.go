//go:build integration

package nuget

import (
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_NUGET_018_AVirtualRepositoryMergesALocalAndARemoteMember puts a local repository and a
// remote of the counting upstream (the remote class is in the test process, over TLS) behind one
// virtual repository with the local one as its deployment target: a package only the remote
// serves comes through the virtual repository with every address on the virtual repository, a
// push through it lands in the local member and nowhere else, and the merged documents of an
// ID list what the members hold (R-NUGET-016 to R-NUGET-018, R-REPO-021).
func TestR_NUGET_018_AVirtualRepositoryMergesALocalAndARemoteMember(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{Valkey: true})
	const mixed = "nuget-mixed"
	e.st.Repository(mixed, "virtual", "nuget", `{}`)
	for i, m := range []string{rLocal, rRemote} {
		e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
			SELECT v.id, $2, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $3`, mixed, i, m)
	}
	e.st.Exec(`INSERT INTO virtual_deploy_targets (virtual_id, target_id)
		SELECT v.id, t.id FROM repositories v, repositories t WHERE v.key = $1 AND t.key = $2`, mixed, rLocal)
	a := e.start(1)[0]
	get := func(repo, path string, hdr ...string) testenv.Answer {
		return e.st.Do(a, "GET", "/api/nuget/"+repo+"/"+path, e.tokAdmin, nil, hdr...)
	}

	t.Run("a package only the remote member serves comes through the virtual repository", func(t *testing.T) {
		flat := get(mixed, "v3/flatcontainer/newtonsoft.json/index.json")
		if flat.Status != 200 {
			t.Fatalf("flat container: %d %s", flat.Status, flat.Body)
		}
		if got := versionsOf(t, flat); !slices.Equal(got, []string{"13.0.1", "13.0.3"}) {
			t.Errorf("versions = %v", got)
		}
		reg := get(mixed, "v3/registration-semver2/newtonsoft.json/index.json", "Accept-Encoding", "gzip")
		if reg.Status != 200 {
			t.Fatalf("registration: %d %s", reg.Status, reg.Body)
		}
		leaves := 0
		for _, p := range registrationOf(t, reg).Items {
			for _, l := range p.Items {
				leaves++
				if !strings.HasPrefix(l.PackageContent, feed(mixed)+"flatcontainer/newtonsoft.json/") {
					t.Errorf("packageContent %q is not an address of the virtual repository", l.PackageContent)
				}
			}
		}
		if leaves != 2 {
			t.Errorf("%d leaves, want 2", leaves)
		}
		pkg := get(mixed, "v3/flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg")
		if pkg.Status != 200 || len(pkg.Body) == 0 {
			t.Errorf("package through the virtual repository: %d, %d bytes", pkg.Status, len(pkg.Body))
		}
	})

	t.Run("a push through the virtual repository lands in its deployment target", func(t *testing.T) {
		body, ct := form(t, nupkg(t, pkgSpec{id: "Acme.Local", version: "1.0.0"}))
		ans := e.st.Do(a, "PUT", "/api/nuget/"+mixed+"/v3/package", e.tokAdmin, body, "Content-Type", ct)
		if ans.Status != 201 {
			t.Fatalf("push: %d %s", ans.Status, ans.Body)
		}
		in := func(repo string) int64 {
			return e.st.Number(`SELECT count(*) FROM nuget_version v JOIN repositories r ON r.id = v.repository_id WHERE r.key = $1 AND v.lower_id = 'acme.local'`, repo)
		}
		if in(rLocal) != 1 || in(rRemote) != 0 {
			t.Errorf("rows: local %d, remote %d, want 1 and 0", in(rLocal), in(rRemote))
		}
		flat := get(mixed, "v3/flatcontainer/acme.local/index.json")
		if flat.Status != 200 || !slices.Equal(versionsOf(t, flat), []string{"1.0.0"}) {
			t.Errorf("merged flat container: %d %s", flat.Status, flat.Body)
		}
		if remote := get(rRemote, "v3/flatcontainer/acme.local/index.json"); remote.Status != 404 {
			t.Errorf("the remote member answers %d for the pushed package, want 404", remote.Status)
		}
	})
}
