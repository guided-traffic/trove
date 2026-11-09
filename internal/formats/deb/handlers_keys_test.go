package deb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// keyHost is the Host of a request for signing-key.asc: it decides with a fixed answer and hands out the port.
type keyHost struct {
	formats.Host
	store  Store
	denied error
	seen   []formats.Target
}

func (h *keyHost) Authorize(_ context.Context, _ *formats.Request, t formats.Target) error {
	h.seen = append(h.seen, t)
	return h.denied
}
func (h *keyHost) FormatData(formats.ID) any { return h.store }

type keyPort struct {
	Store
	armored string
	err     error
}

func (p keyPort) PublicCertificates(context.Context, int64) ([]byte, [32]byte, error) {
	return []byte(p.armored), sha256.Sum256([]byte(p.armored)), p.err
}

func keyRequest(method, ifNoneMatch string, class formats.Class, h *keyHost) *formats.Request {
	r := httptest.NewRequest(method, "/signing-key.asc", nil)
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	return &formats.Request{Request: r, Repo: &repository.Repository{ID: "7", Class: class, Format: "deb"}, Host: h}
}

func TestR_DEB_018_SigningKeyIsServedWithItsDigestAsETag(t *testing.T) {
	const armored = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nAAAA\n-----END PGP PUBLIC KEY BLOCK-----\n"
	sum := sha256.Sum256([]byte(armored))
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	f := New()

	h := &keyHost{store: keyPort{armored: armored}}
	w := httptest.NewRecorder()
	if err := f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Local, h), w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || w.Body.String() != armored || w.Header().Get("Content-Type") != "application/pgp-keys" || w.Header().Get("ETag") != etag {
		t.Errorf("GET = %d %q %v", w.Code, w.Body, w.Header())
	}
	if len(h.seen) != 1 || h.seen[0].Package != "" || h.seen[0].Path != "signing-key.asc" || len(h.seen[0].Actions) != 1 || h.seen[0].Actions[0] != formats.ActionRead {
		t.Errorf("the decision target is %+v: read of the file without a package (R-DEB-004)", h.seen)
	}

	for _, header := range []string{etag, "W/" + etag, `"x", ` + etag, "*"} {
		w = httptest.NewRecorder()
		if err := f.serveSigningKey(context.Background(), keyRequest("GET", header, formats.Local, h), w); err != nil || w.Code != 304 || w.Body.Len() != 0 {
			t.Errorf("If-None-Match %q = %d %v", header, w.Code, err)
		}
	}
	w = httptest.NewRecorder()
	if err := f.serveSigningKey(context.Background(), keyRequest("GET", `"other"`, formats.Virtual, h), w); err != nil || w.Code != 200 {
		t.Errorf("another validator on a virtual repository = %d %v", w.Code, err)
	}
	w = httptest.NewRecorder()
	if err := f.serveSigningKey(context.Background(), keyRequest("HEAD", "", formats.Local, h), w); err != nil || w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD = %d %q %v", w.Code, w.Body, err)
	}
}

func TestR_DEB_018_SigningKeyRefusals(t *testing.T) {
	f := New()
	code := func(err error) (int, string) {
		var e *errs.Error
		if !errors.As(err, &e) {
			return 0, ""
		}
		return e.Status(), e.Code
	}
	// Nothing to serve: every key withheld.
	h := &keyHost{store: keyPort{}}
	if st, c := code(f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Local, h), httptest.NewRecorder())); st != http.StatusNotFound || c != errs.CodeNotFound {
		t.Errorf("no key = %d %s", st, c)
	}
	// A caller the decision point refuses never reaches the port.
	h = &keyHost{store: keyPort{err: errors.New("the port was asked")}, denied: errs.E(errs.Forbidden, errs.CodeForbidden, "no")}
	if st, _ := code(f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Local, h), httptest.NewRecorder())); st != http.StatusForbidden {
		t.Errorf("denied = %d", st)
	}
	// The remote class serves the certificates of deb.upstreamKeys, and has none to serve without them (R-DEB-018).
	if st, c := code(f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Remote, &keyHost{store: keyPort{}}), httptest.NewRecorder())); st != http.StatusNotFound || c != errs.CodeDebNoUpstreamKeys {
		t.Errorf("remote = %d %s", st, c)
	}
	// A port error is the request's error.
	if err := f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Local, &keyHost{store: keyPort{err: errors.New("boom")}}), httptest.NewRecorder()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("port error: %v", err)
	}
	// A host without the port is an internal error, not a panic.
	if st, _ := code(f.serveSigningKey(context.Background(), keyRequest("GET", "", formats.Local, &keyHost{}), httptest.NewRecorder())); st != http.StatusInternalServerError {
		t.Errorf("no port = %d", st)
	}
}
