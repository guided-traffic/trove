//go:build integration

package registry

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/qname"
)

var teamLocal = qname.Repo{Project: "web", Key: "team-local"}

// mountCase is one mount through /v2/ on a host: the endpoint and host it arrives on, the image names the host's address spells, and the
// repositories the source and the target are in.
type mountCase struct {
	endpoint, host string
	bound          bool
	srcRepo        qname.Repo // the repository of the source
	src, tgt       string     // the image names as the client spells them in its scopes
	from           string     // the `from` as the client spells it
	blobURL        func(name, digest string) string
}

// post sends the mount with a token of the grant and returns the answer.
func (e *env) post(c mountCase, bearerToken, query string) answer {
	e.t.Helper()
	return e.send(c.endpoint, req{method: "POST", host: c.host, auth: bearer(bearerToken), path: strings.TrimSuffix(c.blobURL(c.tgt, ""), "/") + "/uploads/?" + query})
}

// entries of a grant for the mount: push on the target and, when pull is set, pull on the source.
func (c mountCase) grant(tgtRepo qname.Repo, pull bool) registrytoken.Grant {
	g := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: tgtRepo, Name: c.tgt, Actions: registrytoken.Push}}}
	if pull {
		g.Entries = append(g.Entries, registrytoken.Entry{Repo: c.srcRepo, Name: c.src, Actions: registrytoken.Pull})
	}
	return g
}

func (e *env) blobStatus(c mountCase, name, digest string) int {
	return e.send(c.endpoint, req{method: "GET", host: c.host, auth: bearer(e.admin), path: c.blobURL(name, digest)}).Code
}

func (e *env) upload(c mountCase, name string, data []byte) {
	e.t.Helper()
	a := e.send(c.endpoint, req{
		method: "POST", host: c.host, auth: bearer(e.admin), body: data, header: map[string]string{"Content-Type": "application/octet-stream"},
		path: strings.TrimSuffix(c.blobURL(name, ""), "/") + "/uploads/?digest=" + digestOf(data),
	})
	if a.Code != http.StatusCreated {
		e.t.Fatalf("upload of %s: %d %s", name, a.Code, a.Body.String())
	}
}

// R-OCI-012 through /v2/: the grant on the source of a mount decides whether the blob is linked. A registry token with push on the target
// and pull on the source links it (201); one with push alone gets the session of an upload (202) and nothing is linked, and a principal who
// may not read the source gets the same 202 whatever its token carries. The same holds on a host bound to the target's repository, where
// `from` is an image name of it, and with the parameters sent twice: the enforcement point and the handler read the first value of each.
func TestR_OCI_012_AMountThroughV2IsLimitedByTheGrantOnItsSource(t *testing.T) {
	e := newEnv(t, nil)
	layer := []byte("a layer that a mount links")
	d := digestOf(layer)

	shared := mountCase{
		endpoint: "default", srcRepo: teamLocal, src: "base", tgt: "tgt", from: "web/team-local/base",
		blobURL: func(name, digest string) string {
			return "/v2/web/docker-local/" + name + "/blobs/" + digest
		},
	}
	sharedSrc := func(name, digest string) string { return "/v2/web/team-local/" + name + "/blobs/" + digest }
	boundHost := mountCase{
		endpoint: "mirror", host: mirrorHost, bound: true, srcRepo: local, src: "base", tgt: "tgt", from: "base",
		blobURL: func(name, digest string) string { return "/v2/" + name + "/blobs/" + digest },
	}
	// the source blobs: one in team-local for the shared host, one under the bound repository for the bound host
	e.send("default", req{
		method: "POST", auth: bearer(e.admin), body: layer, header: map[string]string{"Content-Type": "application/octet-stream"},
		path: "/v2/web/team-local/base/blobs/uploads/?digest=" + d,
	})
	if got := e.send("default", req{method: "GET", auth: bearer(e.admin), path: sharedSrc("base", d)}).Code; got != http.StatusOK {
		t.Fatalf("the source blob of the shared host: %d", got)
	}
	e.upload(boundHost, "base", layer)
	// a second source that holds the same blob, for the case whose token covers only it
	e.send("default", req{
		method: "POST", auth: bearer(e.admin), body: layer, header: map[string]string{"Content-Type": "application/octet-stream"},
		path: "/v2/web/team-local/alt/blobs/uploads/?digest=" + d,
	})
	e.upload(boundHost, "alt", layer)

	for name, c := range map[string]mountCase{"the shared host": shared, "a host bound to the target's repository": boundHost} {
		t.Run(name, func(t *testing.T) {
			tgtRepo := local
			tok := func(pull bool) string { return e.mint(c.endpoint, c.host, e.admin, c.grant(tgtRepo, pull), c.bound) }
			q := "mount=" + d + "&from=" + c.from
			linked := func(tgt string) bool { return e.blobStatus(c, tgt, d) == http.StatusOK }

			// the positive control: push on the target and pull on the source link the blob
			a := e.post(c, tok(true), q)
			if a.Code != http.StatusCreated || a.Header().Get("Docker-Content-Digest") != d {
				t.Fatalf("a mount with pull on the source: %d %v %s", a.Code, a.Header(), a.Body.String())
			}
			if !linked(c.tgt) {
				t.Fatal("the mounted blob is not served through the target")
			}

			// no pull on the source: a session, and nothing is linked
			c2 := c
			c2.tgt = "tgt2"
			a = e.post(c2, e.mint(c.endpoint, c.host, e.admin, c2.grant(tgtRepo, false), c.bound), q)
			if a.Code != http.StatusAccepted || a.Header().Get("Docker-Upload-UUID") == "" {
				t.Fatalf("a mount without pull on the source: %d %v %s", a.Code, a.Header(), a.Body.String())
			}
			if linked("tgt2") {
				t.Fatal("a mount without pull on the source linked the blob")
			}

			// the first value of each parameter decides on both sides: a second `from` or `mount` changes neither the check nor the link
			c3 := c
			c3.tgt = "tgt3"
			other := "web/docker-local/none"
			if c.bound {
				other = "none"
			}
			a = e.post(c3, e.mint(c.endpoint, c.host, e.admin, c3.grant(tgtRepo, true), c.bound), q+"&from="+other+"&mount="+digestOf([]byte("another")))
			if a.Code != http.StatusCreated || !linked("tgt3") {
				t.Fatalf("a mount whose second from and mount differ: %d %s", a.Code, a.Body.String())
			}
			c4 := c
			c4.tgt = "tgt4"
			// the token carries pull on the second source only: the first one, which both sides read, is not covered
			g := c4.grant(tgtRepo, false)
			g.Entries = append(g.Entries, registrytoken.Entry{Repo: c.srcRepo, Name: "decoy", Actions: registrytoken.Pull})
			decoyFrom := "web/team-local/decoy"
			if c.bound {
				decoyFrom = "decoy"
			}
			a = e.post(c4, e.mint(c.endpoint, c.host, e.admin, g, c.bound), q+"&from="+decoyFrom)
			if a.Code != http.StatusAccepted || linked("tgt4") {
				t.Fatalf("a token with pull on the second from only: %d %s", a.Code, a.Body.String())
			}

			// the strip removes every value: two mount and two from values, a token with pull on the second source only, and that
			// source holds the blob. A strip of the first values alone would leave mount and from=<second> for the handler, which links.
			c5 := c
			c5.tgt = "tgt5b"
			altFrom, altName := "web/team-local/alt", "alt"
			g5 := c5.grant(tgtRepo, false)
			g5.Entries = append(g5.Entries, registrytoken.Entry{Repo: c.srcRepo, Name: altName, Actions: registrytoken.Pull})
			if c.bound {
				altFrom = "alt"
			}
			a = e.post(c5, e.mint(c.endpoint, c.host, e.admin, g5, c.bound), "mount="+d+"&mount="+d+"&from="+c.from+"&from="+altFrom)
			if a.Code != http.StatusAccepted || linked("tgt5b") {
				t.Fatalf("a strip that left the second values: %d %s", a.Code, a.Body.String())
			}
		})
	}

	t.Run("a principal who may not read the source gets the session whatever the token carries", func(t *testing.T) {
		c := shared
		c.tgt = "tgt5"
		a := e.post(c, e.mint(c.endpoint, c.host, e.pusher, c.grant(local, true), false), "mount="+d+"&from="+c.from)
		if a.Code != http.StatusAccepted || a.Header().Get("Docker-Upload-UUID") == "" || e.blobStatus(c, "tgt5", d) == http.StatusOK {
			t.Fatalf("%d %v %s", a.Code, a.Header(), a.Body.String())
		}
	})
	t.Run("the spelling of the shared host is no image name on a bound host", func(t *testing.T) {
		c := boundHost
		c.tgt = "tgt6"
		g := c.grant(local, true)
		a := e.post(c, e.mint(c.endpoint, c.host, e.admin, g, true), "mount="+d+"&from=web/docker-local/base")
		if a.Code != http.StatusAccepted || e.blobStatus(c, "tgt6", d) == http.StatusOK {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
	})
}
