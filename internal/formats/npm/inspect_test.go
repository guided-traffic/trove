package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/tarball"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// memSpool is a formats.Spooled over bytes.
type memSpool struct {
	b []byte
	// broken, when set, is the source that fails on its first read.
	broken io.Reader
}

func (s *memSpool) Digests() formats.Digests { return formats.Digests{} }
func (s *memSpool) Size() int64              { return int64(len(s.b)) }
func (s *memSpool) Open() (io.ReadCloser, error) {
	if s.broken != nil {
		return io.NopCloser(s.broken), nil
	}
	return io.NopCloser(bytes.NewReader(s.b)), nil
}
func (s *memSpool) ReaderAt() (io.ReaderAt, error) { return bytes.NewReader(s.b), nil }

type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

func tgz(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, n := range []string{"package/package.json", "package/README.md", "package/index.js"} {
		c, ok := files[n]
		if !ok {
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(c))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestR_NPM_007_InspectReadsTheIdentityOfATarball(t *testing.T) {
	f := New()
	sp := &memSpool{b: tgz(t, map[string]string{
		"package/package.json": `{"name":"@acme/ui","version":"1.2.0"}`, "package/README.md": "# ui", "package/index.js": "x",
	})}
	insp, err := f.Inspect(context.Background(), formats.InspectInput{Body: sp, Limits: untrusted.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if insp.Package != "@acme/ui" || insp.Version != "1.2.0" || insp.Properties["npm.name"] != "@acme/ui" ||
		insp.Properties["npm.version"] != "1.2.0" || insp.Properties["npm.scope"] != "@acme" {
		t.Fatalf("%+v", insp)
	}
	t.Run("an unscoped name has no scope property", func(t *testing.T) {
		insp, err := f.Inspect(context.Background(), formats.InspectInput{Body: &memSpool{b: tgz(t, map[string]string{"package/package.json": `{"name":"p","version":"1.0.0"}`})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := insp.Properties["npm.scope"]; ok {
			t.Fatalf("%v", insp.Properties)
		}
	})
	t.Run("R-NPM-007 a tarball without a package.json is content_refused (422)", func(t *testing.T) {
		_, err := f.Inspect(context.Background(), formats.InspectInput{Body: &memSpool{b: tgz(t, map[string]string{"package/index.js": "x"})}})
		if e, _ := errs.As(err); e == nil || e.Code != "content_refused" || e.Status() != 422 {
			t.Fatalf("%v", err)
		}
	})
	t.Run("R-NPM-007 something that is no archive is content_refused (422)", func(t *testing.T) {
		_, err := f.Inspect(context.Background(), formats.InspectInput{Body: &memSpool{b: []byte("not gzip at all")}})
		if e, _ := errs.As(err); e == nil || e.Code != "content_refused" || e.Status() != 422 {
			t.Fatalf("%v", err)
		}
	})
}

// TestR_NPM_007_ReadErrorsOfTheSpoolAreClassified: an error of the spool or the context
// is not the publisher's fault, and does not turn into a 400 or a bare 500.
func TestR_NPM_007_ReadErrorsOfTheSpoolAreClassified(t *testing.T) {
	good := tgz(t, map[string]string{"package/package.json": `{"name":"p","version":"1.0.0"}`})
	t.Run("a source error stays an internal error with its cause", func(t *testing.T) {
		boom := errors.New("disk read failed")
		_, err := validateTarball(context.Background(), &memSpool{b: good, broken: &failingReader{boom}}, tarballLimits())
		e, ok := errs.As(err)
		if ok && e.Kind != errs.Internal {
			t.Fatalf("%v", err)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("the cause is lost: %v", err)
		}
	})
	t.Run("a cancelled context is unavailable, to retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := validateTarball(ctx, &memSpool{b: good}, tarballLimits())
		if errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("%v", err)
		}
	})
	t.Run("an expired deadline is unavailable", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := validateTarball(ctx, &memSpool{b: good}, tarballLimits())
		if errs.KindOf(err) != errs.Unavailable {
			t.Fatalf("%v", err)
		}
	})
}

func tarballLimits() tarball.Limits { return tarball.Limits{} }
