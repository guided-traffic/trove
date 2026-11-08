package formathost

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

type fakeFormatAuth struct {
	gotRC     formats.RemoteContext
	gotPath   formats.Path
	gotStatus int
}

func (f *fakeFormatAuth) TokenRequest(_ context.Context, rc formats.RemoteContext, orig *http.Request, p formats.Path, status int, _ http.Header) (*http.Request, string, bool, error) {
	f.gotRC, f.gotPath, f.gotStatus = rc, p, status
	return orig, "key", true, nil
}

func (f *fakeFormatAuth) ReadToken(_ context.Context, rc formats.RemoteContext, status int, body []byte) (string, time.Duration, error) {
	f.gotRC = rc
	return "Bearer " + string(body), time.Duration(status) * time.Second, nil
}

type fakeFormatRevalidator struct {
	stored formats.RemoteDocument
	path   formats.Path
	get    *http.Request
}

func (f *fakeFormatRevalidator) RevalidationRequest(_ context.Context, _ formats.RemoteContext, p formats.Path, _ string, stored formats.RemoteDocument, get *http.Request) (*http.Request, bool, error) {
	f.path, f.stored, f.get = p, stored, get
	return nil, true, nil
}

func (f *fakeFormatRevalidator) Unchanged(_ formats.Path, stored formats.RemoteDocument, resp *http.Response) bool {
	return resp.StatusCode == 200 && stored.Digest[0] == 7
}

func newTestAdapter(caps formats.RemoteCaps) *adapter {
	return &adapter{caps: caps, repo: &repository.Repository{}, settings: []byte(`{"k":1}`)}
}

// TestR_ARCH_008_AdapterPassesTheHandshakeToTheFormat: the engine's Authenticator over a format's adapter hands the repository's
// upstream, the format's settings, the path and the status through, and a format without a handshake answers ok false so the
// engine leaves the 401 as it is.
func TestR_ARCH_008_AdapterPassesTheHandshakeToTheFormat(t *testing.T) {
	up, _ := url.Parse("https://registry.example.com/")
	orig, _ := http.NewRequest(http.MethodGet, "https://registry.example.com/v2/x", nil)

	fa := &fakeFormatAuth{}
	a := newTestAdapter(formats.RemoteCaps{Auth: fa})
	req, key, ok, err := a.TokenRequest(context.Background(), proxy.UpstreamBase{URL: up}, orig, "library/nginx", 401, http.Header{})
	if err != nil || !ok || key != "key" || req != orig {
		t.Fatalf("TokenRequest: %v %v %q", err, ok, key)
	}
	if fa.gotRC.Upstream != up || string(fa.gotRC.Settings) != `{"k":1}` || fa.gotPath.Path != "library/nginx" || fa.gotStatus != 401 {
		t.Errorf("the format was handed %+v, %+v, %d", fa.gotRC, fa.gotPath, fa.gotStatus)
	}
	if v, ttl, err := a.ReadToken(context.Background(), 30, []byte("abc")); err != nil || v != "Bearer abc" || ttl != 30*time.Second {
		t.Errorf("ReadToken: %q %v %v", v, ttl, err)
	}

	none := newTestAdapter(formats.RemoteCaps{})
	if _, _, ok, err := none.TokenRequest(context.Background(), proxy.UpstreamBase{URL: up}, orig, "", 401, http.Header{}); ok || err != nil {
		t.Errorf("a format without a handshake: ok %v, err %v", ok, err)
	}
	if _, _, err := none.ReadToken(context.Background(), 200, nil); err == nil {
		t.Error("ReadToken of a format without a handshake succeeded")
	}
	if handshakeOf(formats.RemoteCaps{}, &repository.Repository{}, nil, nil) != nil {
		t.Error("handshakeOf returned a handshake for a format without one")
	}
}

// TestR_ARCH_008_AdapterPassesTheRevalidationToTheFormat: the stored document reaches the format with its digest and media type;
// a format without a revalidation request answers ok false, and its answer is never unchanged.
func TestR_ARCH_008_AdapterPassesTheRevalidationToTheFormat(t *testing.T) {
	up, _ := url.Parse("https://registry.example.com/")
	orig, _ := http.NewRequest(http.MethodGet, "https://registry.example.com/v2/x/manifests/1", nil)
	fr := &fakeFormatRevalidator{}
	a := newTestAdapter(formats.RemoteCaps{Revalidate: fr})
	stored := proxy.Document{Digest: [32]byte{7}, Size: 12, MediaType: "application/vnd.oci.image.manifest.v1+json"}
	if _, ok, err := a.RevalidationRequest(context.Background(), proxy.UpstreamBase{URL: up}, "n/_tags/1", "", stored, orig); !ok || err != nil {
		t.Fatalf("RevalidationRequest: %v %v", ok, err)
	}
	if fr.get != orig {
		t.Error("the engine's GET did not reach the format")
	}
	if fr.path.Path != "n/_tags/1" || fr.stored.Digest != stored.Digest || fr.stored.MediaType != stored.MediaType || fr.stored.Size != 12 {
		t.Errorf("the format was handed %+v at %+v", fr.stored, fr.path)
	}
	if !a.Unchanged("n/_tags/1", stored, &http.Response{StatusCode: 200}) {
		t.Error("the format's verdict did not come through")
	}

	none := newTestAdapter(formats.RemoteCaps{})
	if _, ok, err := none.RevalidationRequest(context.Background(), proxy.UpstreamBase{URL: up}, "p", "", stored, nil); ok || err != nil {
		t.Errorf("a format without a revalidation: ok %v, err %v", ok, err)
	}
	if none.Unchanged("p", stored, &http.Response{StatusCode: 200}) {
		t.Error("a format without a revalidation reported an unchanged document")
	}
}
