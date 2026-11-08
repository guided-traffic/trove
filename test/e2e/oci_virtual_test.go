//go:build e2e

package e2e

// The OCI acceptance tests of a virtual repository (R-OCI-027, R-OCI-028): a virtual repository over a local repository (the deployment target)
// and a remote of the counting test upstream, used by the real clients to pull, to push and to build on a base image that only the remote's
// cache holds.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestOCIVirtual(t *testing.T) {
	fx := ociFx(t)
	s := ociStub(t)
	host := ociRegistryHost(t)
	repos := fx.upstreamRepos(t, "virtual", nil)
	s.resetAll(t)
	defer s.resetAll(t)
	for i := range 3 {
		fx.client(t, i, ociDocker, "virtual-login").login(host)
	}
	pruneClients(t, 3, ociDocker, ociPodman) // the base image below must come from the remote member, not from a client's store

	t.Run("R-OCI-027 a tag the local member has is served from it and the upstream is not asked", func(t *testing.T) {
		ref := fx.image(t, repos.local, "app", "1")
		c := fx.client(t, 0, ociDocker, "virtual-local")
		c.login(host)
		c.build(ref, "the local app "+fx.run)
		c.push(ref)
		s.reset(t) // the push asked the upstream whether a remote member serves the name (R-REPO-048); the pull below must not
		local := fx.digestOf(t, repos.local, "app", "1")
		upstream := s.ociImage(t, "app", "1").Digest
		if local == upstream {
			t.Fatalf("the local image and the upstream's are the same image (%s): the test cannot tell them apart", local)
		}
		viaVirtual := fx.image(t, repos.all, "app", "1")
		d := fx.client(t, 1, ociPodman, "virtual-pull")
		d.login(host)
		d.pull(viaVirtual)
		if !d.holds(viaVirtual, local) {
			t.Fatalf("podman holds %q after pulling app:1 through the virtual repository, want the local digest %s (not the upstream's %s)", d.digests(viaVirtual), local, upstream)
		}
		if from := fx.head(t, repos.all, "app", "1").Header.Get("Trove-Resolved-From"); !strings.Contains(from, repos.local) {
			t.Errorf("Trove-Resolved-From is %q, want the local member %s", from, repos.local)
		}
		if n := s.ociCounts(t).RepositoryRequests("app"); n != 0 {
			t.Errorf("the upstream saw %d requests for app, want none: the local member answers first", n)
		}
	})

	t.Run("R-OCI-027 a tag only the remote member has is pulled through the virtual repository", func(t *testing.T) {
		s.reset(t)
		info := s.ociImage(t, "library/alpine", "3")
		ref := fx.image(t, repos.all, "library/alpine", "3")
		c := fx.client(t, 2, ociDocker, "virtual-remote")
		c.login(host)
		c.pull(ref)
		if !c.holds(ref, info.Digest) {
			t.Fatalf("docker holds %q after the pull, the upstream's index is %s", c.digests(ref), info.Digest)
		}
		if n := s.ociCounts(t).ManifestGets("library/alpine", "3"); n != 1 {
			t.Errorf("the upstream saw %d manifest GET requests for library/alpine:3, want 1", n)
		}
	})

	t.Run("R-OCI-028 docker push through the virtual repository stores the image in the deployment target", func(t *testing.T) {
		ref := fx.image(t, repos.all, "app", "2")
		c := fx.client(t, 0, ociDocker, "virtual-push")
		c.login(host)
		c.build(ref, "pushed through the virtual repository "+fx.run)
		out := c.push(ref)
		t.Logf("docker push through %s:\n%s", repos.all, tailLines(out, 12))
		digest := fx.digestOf(t, repos.local, "app", "2")
		other := fx.client(t, 1, ociDocker, "virtual-push-pull")
		other.login(host)
		direct := fx.image(t, repos.local, "app", "2")
		other.pull(direct)
		if !other.holds(direct, digest) {
			t.Fatalf("docker holds %q after pulling %s, want the digest %s of the push", other.digests(direct), direct, digest)
		}
		if got := fx.digestOf(t, repos.all, "app", "2"); got != digest {
			t.Errorf("the virtual repository resolves app:2 to %s, the deployment target holds %s", got, digest)
		}
	})

	t.Run("R-OCI-028 an image built on a base image the remote member cached is pushed without uploading the base layers", func(t *testing.T) {
		base := fx.image(t, repos.all, "library/alpine", "3")
		ref := fx.image(t, repos.all, "derived", "1")
		c := fx.client(t, 0, ociDocker, "virtual-derived")
		c.login(host)
		c.pull(base)
		ctx := c.buildContext("derived "+fx.run, "FROM "+base+"\nCOPY payload /payload\n")
		c.exec("build", "--tag", ref, ctx)
		out := c.push(ref)
		// docker names a layer by the first twelve digits of its blob; a layer the virtual repository answers 200 for is "Already exists", one
		// docker mounts from the base image's name is "Mounted from", an uploaded one is "Pushed"
		baseImage, ok := s.ociImage(t, "library/alpine", "3").ForPlatform("linux/" + ociArch(t))
		if !ok || len(baseImage.Layers) == 0 {
			t.Fatalf("the upstream's library/alpine:3 has no layers for linux/%s", ociArch(t))
		}
		for _, l := range baseImage.Layers {
			id := strings.TrimPrefix(l.Digest, "sha256:")[:12]
			line := ""
			for _, ln := range strings.Split(out, "\n") {
				if strings.HasPrefix(ln, id+": ") && !strings.Contains(ln, "Waiting") {
					line = ln
				}
			}
			if line == "" || strings.Contains(line, "Pushed") {
				t.Errorf("the base layer %s was uploaded (%q): R-OCI-028 expects it to be skipped or mounted\n%s", id, line, tailLines(out, 20))
			}
		}
		digest := fx.digestOf(t, repos.local, "derived", "1")
		p := fx.client(t, 2, ociPodman, "virtual-derived-pull")
		p.login(host)
		direct := fx.image(t, repos.local, "derived", "1")
		p.pull(direct)
		if !p.holds(direct, digest) {
			t.Fatalf("podman holds %q after pulling %s, want the digest %s", p.digests(direct), direct, digest)
		}
	})

	t.Run("R-OCI-027 the tags list of a name is the union of the members' tags", func(t *testing.T) {
		r := fx.registry(t).do(http.MethodGet, "/v2/"+repoRef(repos.all)+"/app/tags/list", nil).want(t, "tags list through the virtual repository", 200)
		var list struct {
			Tags []string `json:"tags"`
		}
		r.JSON(t, &list)
		// 1 is in both members, 2 only in the deployment target
		if !slices.Equal(list.Tags, []string{"1", "2"}) {
			t.Fatalf("tags of app through the virtual repository: %v, want [1 2]", list.Tags)
		}
	})

	t.Run("R-OCI-028 a delete through the virtual repository answers 405 and removes nothing", func(t *testing.T) {
		digest := fx.digestOf(t, repos.local, "app", "2")
		fx.registry(t).do(http.MethodDelete, "/v2/"+repoRef(repos.all)+"/app/manifests/"+digest, nil).want(t, "delete through the virtual repository", 405)
		if got := fx.digestOf(t, repos.local, "app", "2"); got != digest {
			t.Fatalf("app:2 is %s after the refused delete, was %s", got, digest)
		}
		var errs struct {
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}
		r := fx.registry(t).do(http.MethodDelete, "/v2/"+repoRef(repos.all)+"/app/manifests/"+digest, nil)
		if err := json.Unmarshal(r.Body, &errs); err != nil || len(errs.Errors) == 0 || errs.Errors[0].Code != "UNSUPPORTED" {
			t.Errorf("the refusal's body is %s, want the code UNSUPPORTED", r.Body)
		}
	})
}
