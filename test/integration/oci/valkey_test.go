//go:build integration

package oci

import (
	"fmt"
	"testing"
)

// TestRegistryWorksWithValkeyStopped starts three replicas whose Valkey cannot be reached (the state "Valkey stopped" of R-HA-001,
// reached by pointing their Sentinel at an address that answers nothing, so the stack's Valkey is not touched) and runs the
// registry through them: the token service, a chunked push and a monolithic push, pulls, the tags list and the catalog through a local
// and through a virtual repository, every request on another replica. The sessions of an upload, the token cache's invalidation and
// the flights have a path that needs no Valkey (R-OCI-032). The criterion of R-OCI-032 itself runs Docker and Podman and is the closing
// run of the e2e set oci; this proves the protocol half of it without a client.
func TestRegistryWorksWithValkeyStopped(t *testing.T) {
	p := newProto(t, protoOptions{replicas: 3, valkeyDown: true})
	for i, r := range []string{repoLocal, repoVirt} {
		name := fmt.Sprint("down/app", i)
		img := newImage("down-" + r)
		tok := p.registryToken(p.next(), "pusher", scopeOf(r, name, "pull,push"), "registry:catalog:*")
		want(t, p.pushImage(bearer(tok), r, name, "1", img), 201, "")
		for rep := range p.reps {
			got := p.do(call{rep: rep, method: "GET", path: repo(r, name) + "/manifests/1", cred: bearer(tok)})
			if got.Status != 200 || digestOf(got.Body) != img.digest() {
				t.Fatalf("%s: pull on replica %d: %d", r, rep, got.Status)
			}
		}
		want(t, p.do(call{rep: p.next(), method: "GET", path: repo(r, name) + "/tags/list", cred: bearer(tok)}), 200, "")
		// A chunked upload whose requests reach different replicas.
		data := []byte("a layer pushed in two chunks without Valkey, " + r)
		loc := p.startUpload(p.next(), bearer(tok), r, name)
		a := p.do(call{rep: p.next(), method: "PATCH", path: loc, cred: bearer(tok), body: data[:10], hdr: []string{"Content-Type", octet}})
		want(t, a, 202, "")
		loc = hdr(a, "Location")[len("http://trove.test"):]
		a = p.do(call{rep: p.next(), method: "PATCH", path: loc, cred: bearer(tok), body: data[10:], hdr: []string{"Content-Type", octet}})
		want(t, a, 202, "")
		loc = hdr(a, "Location")[len("http://trove.test"):]
		want(t, p.do(call{rep: p.next(), method: "PUT", path: withDigest(loc, digestOf(data)), cred: bearer(tok)}), 201, "")
	}
	cat := p.do(call{rep: p.next(), method: "GET", path: "/v2/_catalog", cred: p.as("reader")})
	if cat.Status != 200 || !contains(catalogNames(t, cat), project+"/"+repoLocal+"/down/app1") {
		t.Fatalf("catalog: %d %s", cat.Status, cat.Body)
	}
}
