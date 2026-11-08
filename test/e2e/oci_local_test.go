//go:build e2e

package e2e

// The OCI acceptance tests of a local repository (R-OCI-001, R-OCI-004, R-OCI-014, R-OCI-032): the real clients log in, push and pull
// through oci-e2e on the scatter profile, so that every request of a push or a pull may reach another packages replica, and across client
// pods, so that the image a pod pushed is pulled by another pod and another client.

import (
	"regexp"
	"strings"
	"testing"
)

func TestOCILocal(t *testing.T) {
	fx := ociFx(t)
	host := ociRegistryHost(t)

	t.Run("R-OCI-004 R-OCI-035 both clients log in with a trove token", func(t *testing.T) {
		fx.client(t, 0, ociDocker, "login").login(host)
		fx.client(t, 0, ociPodman, "login").login(host)
	})

	t.Run("R-OCI-001 R-OCI-032 docker push of a local image, docker pull of it by another pod", func(t *testing.T) {
		name, tag := fx.name("docker-roundtrip"), "1"
		ref := fx.image(t, ociLocalKey, name, tag)
		a, b := fx.client(t, 0, ociDocker, "push"), fx.client(t, 1, ociDocker, "pull")
		a.login(host)
		b.login(host)
		a.build(ref, "docker roundtrip "+fx.run)
		a.push(ref)
		digest := fx.digestOf(t, ociLocalKey, name, tag)
		if !a.holds(ref, digest) {
			t.Errorf("the pushing docker holds %q, the registry holds the manifest %s", a.digests(ref), digest)
		}
		b.pull(ref)
		if !b.holds(ref, digest) {
			t.Fatalf("the pulling docker holds %q, want the digest %s of the pushed manifest", b.digests(ref), digest)
		}
	})

	t.Run("R-OCI-001 R-OCI-004 podman push, docker pull and podman pull of the same image on other pods", func(t *testing.T) {
		name, tag := fx.name("podman-roundtrip"), "1"
		ref := fx.image(t, ociLocalKey, name, tag)
		p := fx.client(t, 0, ociPodman, "push")
		p.login(host)
		p.build(ref, "podman roundtrip "+fx.run)
		p.push(ref)
		digest := fx.digestOf(t, ociLocalKey, name, tag)
		d := fx.client(t, 1, ociDocker, "pull")
		d.login(host)
		d.pull(ref)
		if !d.holds(ref, digest) {
			t.Fatalf("docker holds %q after the pull, want the digest %s", d.digests(ref), digest)
		}
		p2 := fx.client(t, 2, ociPodman, "pull")
		p2.login(host)
		p2.pull(ref)
		if !p2.holds(ref, digest) {
			t.Fatalf("podman holds %q after the pull, want the digest %s", p2.digests(ref), digest)
		}
	})

	t.Run("R-OCI-004 a push without a login fails with unauthorized and stores nothing", func(t *testing.T) {
		name := fx.name("nologin")
		ref := fx.image(t, ociLocalKey, name, "1")
		for _, tool := range []string{ociDocker, ociPodman} {
			c := fx.client(t, 0, tool, "nologin")
			c.build(ref, "no login "+tool+" "+fx.run)
			out, err := c.try("push", ref)
			if err == nil || !regexp.MustCompile(`(?i)unauthorized|denied|authentication required|requires a credential`).MatchString(out) {
				t.Fatalf("%s push without a login: %v\n%s", tool, err, tailLines(out, 20))
			}
		}
		fx.head(t, ociLocalKey, name, "1").want(t, "the tag of the refused push", 404)
	})

	t.Run("R-OCI-004 anonymous pull from a repository that grants read to anonymous, anonymous push refused", func(t *testing.T) {
		fx.withAnonymousRead(t)
		name := fx.name("anonymous")
		ref := fx.image(t, ociOpenKey, name, "1")
		pub := fx.client(t, 0, ociDocker, "anon-push")
		pub.login(host)
		pub.build(ref, "anonymous "+fx.run)
		pub.push(ref)
		digest := fx.digestOf(t, ociOpenKey, name, "1")
		for _, tool := range []string{ociDocker, ociPodman} {
			c := fx.client(t, 1, tool, "anon-pull") // no login: this client's configuration directory is empty
			c.token = ""
			c.pull(ref)
			if !c.holds(ref, digest) {
				t.Fatalf("%s pulled %q anonymously, want the digest %s", tool, c.digests(ref), digest)
			}
		}
		anon := fx.client(t, 1, ociDocker, "anon-push")
		anon.token = ""
		anon.build(fx.image(t, ociOpenKey, name, "2"), "anonymous push "+fx.run)
		out, err := anon.try("push", fx.image(t, ociOpenKey, name, "2"))
		if err == nil || !regexp.MustCompile(`(?i)unauthorized|denied|authentication required|requires a credential`).MatchString(out) {
			t.Fatalf("an anonymous docker push: %v\n%s", err, tailLines(out, 20))
		}
	})

	t.Run("R-OCI-014 docker buildx build --push of a two-platform image, a pull of one platform, podman manifest inspect", func(t *testing.T) {
		name, tag := fx.name("buildx"), "1"
		ref := fx.image(t, ociLocalKey, name, tag)
		c := fx.client(t, 0, ociDocker, "buildx")
		c.login(host)
		ctx := c.buildContext("multi-platform " + fx.run)
		c.exec("buildx", "build", "--platform", "linux/amd64,linux/arm64", "--tag", ref, "--push", ctx)
		r := fx.head(t, ociLocalKey, name, tag).want(t, "the pushed index", 200)
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "index") && !strings.Contains(ct, "manifest.list") {
			t.Fatalf("buildx pushed %q, want an image index", ct)
		}
		other := fx.client(t, 1, ociDocker, "buildx-pull")
		other.login(host)
		other.exec("pull", "--platform", "linux/arm64", ref)
		if got := strings.TrimSpace(other.exec("image", "inspect", "--format", "{{.Architecture}}", ref)); got != "arm64" {
			t.Fatalf("docker pull --platform linux/arm64 gave an image of the architecture %q", got)
		}
		p := fx.client(t, 2, ociPodman, "inspect")
		p.login(host)
		out := p.exec("manifest", "inspect", ref)
		for _, arch := range []string{`"amd64"`, `"arm64"`} {
			if !strings.Contains(out, arch) {
				t.Fatalf("podman manifest inspect does not list %s:\n%s", arch, tailLines(out, 40))
			}
		}
	})

	t.Run("R-OCI-014 docker manifest create and push store a manifest list of two single-platform images", func(t *testing.T) {
		name := fx.name("dockermanifest")
		list := fx.image(t, ociLocalKey, name, "1")
		c := fx.client(t, 0, ociDocker, "manifest")
		c.login(host)
		var members []string
		for _, arch := range []string{"amd64", "arm64"} {
			ref := fx.image(t, ociLocalKey, name, arch)
			// The members are Docker image manifests (oci-mediatypes=false): `docker manifest push` writes the kind of list its members call for,
			// a Docker manifest list for Docker manifests and an OCI index for OCI manifests.
			c.exec("buildx", "build", "--provenance=false", "--sbom=false", "--platform", "linux/"+arch,
				"--output", "type=image,name="+ref+",push=true,oci-mediatypes=false", c.buildContext("docker manifest "+arch+" "+fx.run))
			members = append(members, ref)
		}
		c.exec(append([]string{"manifest", "create", list, "--amend"}, members...)...)
		c.exec("manifest", "push", list)
		r := fx.head(t, ociLocalKey, name, "1").want(t, "the pushed manifest list", 200)
		if ct := r.Header.Get("Content-Type"); ct != "application/vnd.docker.distribution.manifest.list.v2+json" {
			t.Fatalf("docker manifest push stored %q, want a Docker manifest list", ct)
		}
		out := c.exec("manifest", "inspect", list)
		if !strings.Contains(out, `"amd64"`) || !strings.Contains(out, `"arm64"`) {
			t.Fatalf("docker manifest inspect does not list both platforms:\n%s", tailLines(out, 40))
		}
	})

	t.Run("R-OCI-014 podman build --manifest and podman manifest push --all store an image index", func(t *testing.T) {
		name := fx.name("podmanmanifest")
		ref := fx.image(t, ociLocalKey, name, "1")
		p := fx.client(t, 0, ociPodman, "manifest")
		p.login(host)
		ctx := p.buildContext("podman manifest " + fx.run)
		p.exec("build", "--platform", "linux/amd64,linux/arm64", "--manifest", "m", ctx)
		p.exec("manifest", "push", "--all", "m", "docker://"+ref)
		r := fx.head(t, ociLocalKey, name, "1").want(t, "the pushed manifest", 200)
		if ct := r.Header.Get("Content-Type"); ct != "application/vnd.oci.image.index.v1+json" {
			t.Fatalf("podman manifest push --all stored %q, want an OCI image index", ct)
		}
		d := fx.client(t, 1, ociDocker, "podman-manifest-pull")
		d.login(host)
		d.exec("pull", "--platform", "linux/amd64", ref)
	})
}
