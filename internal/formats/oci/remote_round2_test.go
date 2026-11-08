package oci

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// Mechanism of R-OCI-021, not a proof of its criteria: it checks a parser or the shape of one handshake request; the criteria are
// the credential sent to the realm only, the refused foreign realm and the token reuse per replica.
func TestTheScopeOfAQueryIsReadFromTheRightOfThePath(t *testing.T) {
	rc := rctx(t, "https://ghcr.io/acme")
	challenge := http.Header{"Www-Authenticate": {`Bearer realm="https://auth.example.com/t",service="s"`}}
	for orig, want := range map[string]string{
		// A valid image name may hold a component that is a protocol word.
		"https://ghcr.io/v2/acme/a/manifests/b/tags/list?n=5":                    "repository:acme/a/manifests/b:pull",
		"https://ghcr.io/v2/acme/a/blobs/x/referrers/" + sha([]byte("x")):        "repository:acme/a/blobs/x:pull",
		"https://ghcr.io/v2/acme/a/manifests/b/manifests/1":                      "repository:acme/a/manifests/b:pull",
		"https://ghcr.io/v2/acme/tags/tags/list":                                 "repository:acme/tags:pull",
		"https://ghcr.io/v2/acme/team/app/blobs/" + sha([]byte("b")):             "repository:acme/team/app:pull",
		"https://ghcr.io/v2/acme/app/tags/list?last=x":                           "repository:acme/app:pull",
		"https://ghcr.io/v2/acme/app/unknown":                                    "",
		"https://ghcr.io/v2/acme/app/referrers":                                  "",
		"https://ghcr.io/other/acme/app/tags/list":                               "",
		"https://ghcr.io/v2/acme/a/referrers/" + sha([]byte("x")) + "/tags/list": "repository:acme/a/referrers/" + sha([]byte("x")) + ":pull",
	} {
		o, _ := http.NewRequest(http.MethodGet, orig, nil)
		r, _, ok, err := adapter().TokenRequest(context.Background(), rc, o, formats.Path{}, 401, challenge)
		if err != nil || !ok {
			t.Fatalf("%s: ok %v err %v", orig, ok, err)
		}
		if got := r.URL.Query().Get("scope"); got != want {
			t.Errorf("%s: scope %q, want %q", orig, got, want)
		}
	}
}

// Mechanism of R-OCI-025, not a proof of its criteria: it checks a decision on both spellings, the quota answer or the mapping;
// the criteria are the single library/nginx tag item and the quota_header count of a mirror pass.
func TestTheLibraryMappingNeedsDockerHubWithoutANamespacePrefix(t *testing.T) {
	for _, c := range []struct{ url, name, want string }{
		{"https://registry-1.docker.io/", "nginx", "library/nginx"},
		{"https://registry-1.docker.io", "nginx", "library/nginx"},
		{"https://REGISTRY-1.docker.io:443/", "nginx", "library/nginx"},
		{"https://registry-1.docker.io/", "bitnami/nginx", "bitnami/nginx"},
		{"https://registry-1.docker.io/bitnami", "nginx", "nginx"},
		{"https://registry-1.docker.io/bitnami/", "library", "library"},
		{"https://ghcr.io/", "nginx", "nginx"},
		{"https://example.com/library", "nginx", "nginx"},
		{"", "nginx", "nginx"},
	} {
		if got := libraryName(c.url, c.name); got != c.want {
			t.Errorf("libraryName(%q, %q) = %q, want %q", c.url, c.name, got, c.want)
		}
	}
}

// Mechanism of R-OCI-021, not a proof of its criteria: it checks a parser or the shape of one handshake request; the criteria are
// the credential sent to the realm only, the refused foreign realm and the token reuse per replica.
func TestAStoodChallengeWithInsufficientScopeIsAMissingImage(t *testing.T) {
	a := adapter()
	h := func(v ...string) http.Header { return http.Header{"Www-Authenticate": v} }
	for name, c := range map[string]struct {
		h    http.Header
		want bool
	}{
		"insufficient scope":        {h(`Bearer realm="https://a.example.com/t",service="s",scope="repository:x/y:pull",error="insufficient_scope"`), true},
		"in the second field":       {h(`Basic realm="r"`, `Bearer realm="https://a.example.com/t",error="insufficient_scope"`), true},
		"the first challenge":       {h(`Bearer realm="https://a.example.com/t",service="s"`), false},
		"an invalid token":          {h(`Bearer realm="https://a.example.com/t",error="invalid_token"`), false},
		"Basic":                     {h(`Basic realm="r", error="insufficient_scope"`), false},
		"no challenge":              {http.Header{}, false},
		"the word in another param": {h(`Bearer realm="https://a.example.com/insufficient_scope"`), false},
	} {
		if got := a.StoodMissing(formats.Path{}, c.h); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// Mechanism of R-OCI-024, not a proof of its criteria: it checks a window, a bound or a spool detail of the tags list; the
// criteria are the first five tags with a Link to trove and the referrers pass-through.
func TestACutTagsListHasNoLengthTheSpoolCouldCheck(t *testing.T) {
	body := `{"name":"app","tags":["a","b"]}`
	h := http.Header{"Link": {`</v2/app/tags/list?last=b&n=2>; rel="next"`}, "Content-Length": {"31"}}
	resp := &http.Response{StatusCode: 200, Header: h, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}
	defer resp.Body.Close()
	if err := adapter().AcceptResponse(formats.Path{Path: "app/_taglist"}, resp); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	// The body is longer than the length the upstream announced, so the announcement is gone.
	if int64(len(got)) == int64(len(body)) || resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
		t.Errorf("body %d bytes, ContentLength %d, header %q", len(got), resp.ContentLength, resp.Header.Get("Content-Length"))
	}
	// A whole list is passed as it is, with its length.
	whole := &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {"31"}}, ContentLength: 31, Body: io.NopCloser(strings.NewReader(body))}
	defer whole.Body.Close()
	if err := adapter().AcceptResponse(formats.Path{Path: "app/_taglist"}, whole); err != nil || whole.ContentLength != 31 {
		t.Errorf("whole list: %v length %d", err, whole.ContentLength)
	}
}

// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestAManifestIsBoundedBeforeItIsSpooled(t *testing.T) {
	big := &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: MaxDocumentBytes + 1, Body: http.NoBody}
	err := adapter().AcceptResponse(formats.Path{Path: "app/_manifests/" + sha([]byte("x"))}, big)
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.UpstreamFailed || e.Code != errs.CodeUpstreamItemTooLarge {
		t.Errorf("announced length above the bound: %v", err)
	}
	// An upstream that announces no length is stopped at the bound while the body is read.
	chunk := strings.NewReader(strings.Repeat("x", MaxDocumentBytes+10))
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(chunk)}
	defer resp.Body.Close()
	if err := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, resp); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); !errors.As(err, &e) || e.Code != errs.CodeUpstreamItemTooLarge {
		t.Errorf("unbounded body: %v", err)
	}
}

// The verdict on the digest of a tag is given when the body ends, so it holds for a consumer that reads to the end however it reads:
// the spool reads a body of a known length through a reader that stops one byte past it (blob/spool.go), a body of an unknown length until
// it ends. A consumer that stops early never gets it, which this test pins as the reliance (Q-OCI-120).
//
// Mechanism of R-OCI-020, not a proof of its criteria: it checks digest verification or an ambiguity rule of the parser; the
// criteria are the crafted inputs refused before any blob is looked up and the memory bound.
func TestTheDigestOfATagIsCheckedWhicheverWayTheBodyIsRead(t *testing.T) {
	body := string(imageManifestBytes(sha([]byte("cfg")), sha([]byte("layer"))))
	wrong := sha([]byte("another"))
	open := func(announce string, length int64) *http.Response {
		return &http.Response{StatusCode: 200, ContentLength: length, Header: http.Header{"Docker-Content-Digest": {announce}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	reads := map[string]func(r io.Reader) error{
		"to the end":               func(r io.Reader) error { _, err := io.ReadAll(r); return err },
		"one byte past the length": func(r io.Reader) error { _, err := io.ReadAll(io.LimitReader(r, int64(len(body))+1)); return err },
		"in small pieces": func(r io.Reader) error {
			_, err := io.CopyBuffer(io.Discard, struct{ io.Reader }{r}, make([]byte, 7))
			return err
		},
		"the length and no further": func(r io.Reader) error { _, err := io.CopyN(io.Discard, r, int64(len(body))); return err },
	}
	for name, read := range reads {
		for _, length := range []int64{int64(len(body)), -1} {
			ok := open(sha([]byte(body)), length)
			if err := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, ok); err != nil {
				t.Fatal(err)
			}
			err := read(ok.Body)
			bad := open(wrong, length)
			if aerr := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, bad); aerr != nil {
				t.Fatal(aerr)
			}
			berr := read(bad.Body)
			_ = ok.Body.Close()
			_ = bad.Body.Close()
			if err != nil {
				t.Errorf("%s, length %d: the right digest failed: %v", name, length, err)
			}
			var e *errs.Error
			caught := errors.As(berr, &e) && e.Code == errs.CodeUpstreamIntegrityMismatch
			// Reading exactly the announced length stops before the end of the body: the verdict is not given there, and the spool
			// never reads that way.
			if name == "the length and no further" {
				if caught {
					t.Logf("caught anyway")
				}
				continue
			}
			if !caught {
				t.Errorf("%s, length %d: the wrong digest was not caught: %v", name, length, berr)
			}
		}
	}
}
