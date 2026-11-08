// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
)

// pull does what a registry client does: follows the challenge to the realm, fetches an index, picks a platform manifest and
// fetches its configuration and layers, following the blob redirects. It verifies every digest.
func pull(t *testing.T, f *fixture, name, tag, platform string, extra map[string]string) {
	t.Helper()
	follow := *f.client
	follow.CheckRedirect = nil // the default policy: Go drops Authorization on a redirect to another host
	scope := "repository:" + name + ":pull"
	get := func(target string, hdr map[string]string) (http.Header, int, []byte) {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := follow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.Header, resp.StatusCode, b
	}

	firstHeader, _, _ := get(f.reg+"/v2/", nil)
	ch := parseChallenge(t, firstHeader.Get("WWW-Authenticate"))
	q := url.Values{"service": {ch["service"]}, "scope": {scope}}
	_, _, tokBody := get(ch["realm"]+"?"+q.Encode(), extra)
	var tok struct{ Token string }
	if err := json.Unmarshal(tokBody, &tok); err != nil || tok.Token == "" {
		t.Fatalf("token: %v %s", err, tokBody)
	}
	auth := map[string]string{"Authorization": "Bearer " + tok.Token, "Accept": acceptAll}

	header, status, body := get(f.reg+"/v2/"+name+"/manifests/"+tag, auth)
	if status != 200 || sha(body) != header.Get("Docker-Content-Digest") {
		t.Fatalf("manifest: %d", status)
	}
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if isIndex(doc.MediaType) {
		var picked *Descriptor
		for i, d := range doc.Manifests {
			if d.Platform != nil && d.Platform.OS+"/"+d.Platform.Architecture == platform {
				picked = &doc.Manifests[i]
			}
		}
		if picked == nil {
			t.Fatalf("no %s manifest in the index", platform)
		}
		_, status, body = get(f.reg+"/v2/"+name+"/manifests/"+picked.Digest, auth)
		if status != 200 || sha(body) != picked.Digest {
			t.Fatalf("platform manifest: %d", status)
		}
		doc = manifestDoc{}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range append([]Descriptor{*doc.Config}, doc.Layers...) {
		_, status, blob := get(f.reg+"/v2/"+name+"/blobs/"+d.Digest, auth)
		if status != 200 || sha(blob) != d.Digest || int64(len(blob)) != d.Size {
			t.Fatalf("blob %s: %d, %d bytes", d.Digest, status, len(blob))
		}
	}
}

// A whole pull through the three hosts: one handshake, each blob delivered once, and the CDN host never sees the token (R-OCI-023).
func TestPullThroughTheThreeHosts(t *testing.T) {
	f := newFixture(t, Config{})
	pull(t, f, "library/alpine", "3", "linux/arm64", nil)

	c := f.stub.Counts()
	info, _ := f.stub.Image("library/alpine", "3")
	arm, _ := info.ForPlatform("linux/arm64")
	for _, d := range append([]string{arm.Config.Digest}, layerDigests(arm)...) {
		if n := c.BlobFetches(d); n != 1 {
			t.Errorf("blob %s fetched %d times, want 1", d, n)
		}
	}
	if v := c.Violations(1, 1); len(v) != 0 {
		t.Errorf("violations: %v", v)
	}
	if c.TokenRequests("") != 1 {
		t.Errorf("token requests = %d, want 1", c.TokenRequests(""))
	}
	if got := c.AuthSchemes(hostPort(t, f.cdn)); len(got) != 1 || got[0] != "none" {
		t.Errorf("the CDN host saw credential schemes %v, want none", got)
	}
	if c.ManifestGets("library/alpine", "3") != 1 {
		t.Errorf("tag manifest GETs = %d", c.ManifestGets("library/alpine", "3"))
	}
	// The amd64 layers were not pulled.
	amd, _ := info.ForPlatform("linux/amd64")
	if c.BlobFetches(amd.Layers[1].Digest) != 0 {
		t.Error("a layer of another platform was fetched")
	}
}

func layerDigests(i ImageInfo) []string {
	var out []string
	for _, l := range i.Layers {
		out = append(out, l.Digest)
	}
	return out
}

func TestPullWithCredentials(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthBasic}); err != nil {
		t.Fatal(err)
	}
	pull(t, f, "library/busybox", "latest", "linux/amd64", basic(DefaultUsername, DefaultPassword))
	c := f.stub.Counts()
	if got := c.AuthSchemes(hostPort(t, f.realm)); len(got) != 1 || got[0] != "basic" {
		t.Errorf("the realm host saw %v, want basic only", got)
	}
	if got := c.AuthSchemes(hostPort(t, f.reg)); len(got) != 2 {
		t.Errorf("the registry host saw %v, want none and bearer", got)
	}
}

func isIndex(mediaType string) bool {
	return mediaType == MediaOCIIndex || mediaType == MediaDockerList
}
