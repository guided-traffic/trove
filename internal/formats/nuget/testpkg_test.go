package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
)

// pkgSpec describes a .nupkg to build for a test.
type pkgSpec struct {
	id, version, description string
	types                    []string
	deps                     [][2]string // id, range
	entries                  map[string][]byte
	noNuspec                 bool
}

type pkgOpt func(*pkgSpec)

func withDescription(d string) pkgOpt { return func(s *pkgSpec) { s.description = d } }
func withType(t string) pkgOpt        { return func(s *pkgSpec) { s.types = append(s.types, t) } }
func withDep(id, rng string) pkgOpt {
	return func(s *pkgSpec) { s.deps = append(s.deps, [2]string{id, rng}) }
}

func withEntry(name string, body []byte) pkgOpt {
	return func(s *pkgSpec) {
		if s.entries == nil {
			s.entries = map[string][]byte{}
		}
		s.entries[name] = body
	}
}
func withoutNuspec() pkgOpt { return func(s *pkgSpec) { s.noNuspec = true } }

// nuspecOf renders the .nuspec of a spec.
func (s pkgSpec) nuspec() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>`)
	fmt.Fprintf(&b, "<id>%s</id><version>%s</version><authors>Acme</authors><description>%s</description><tags>widgets tools</tags>", s.id, s.version, s.description)
	if len(s.types) > 0 {
		b.WriteString("<packageTypes>")
		for _, t := range s.types {
			fmt.Fprintf(&b, `<packageType name="%s"/>`, t)
		}
		b.WriteString("</packageTypes>")
	}
	if len(s.deps) > 0 {
		b.WriteString(`<dependencies><group targetFramework="net8.0">`)
		for _, d := range s.deps {
			fmt.Fprintf(&b, `<dependency id="%s" version="%s"/>`, d[0], d[1])
		}
		b.WriteString("</group></dependencies>")
	}
	b.WriteString("</metadata></package>")
	return []byte(b.String())
}

// makeNupkg builds a package archive.
func makeNupkg(id, version string, opts ...pkgOpt) ([]byte, error) {
	s := pkgSpec{id: id, version: version, description: "Widgets for tests"}
	for _, o := range opts {
		o(&s)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, body []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	if !s.noNuspec {
		if err := add(id+".nuspec", s.nuspec()); err != nil {
			return nil, err
		}
	}
	if err := add("lib/net8.0/"+id+".dll", []byte("MZ-not-a-real-assembly-"+version)); err != nil {
		return nil, err
	}
	for name, body := range s.entries {
		if err := add(name, body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildNupkg is makeNupkg for a test.
func buildNupkg(t testing.TB, id, version string, opts ...pkgOpt) []byte {
	t.Helper()
	b, err := makeNupkg(id, version, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// makeMultipart wraps a package as the body NuGet clients send and returns it with its
// Content-Type; further parts are added after the package, as some clients do.
func makeMultipart(pkg []byte, extra ...string) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="package"; filename="package"`)
	h.Set("Content-Type", "application/octet-stream")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(pkg); err != nil {
		return nil, "", err
	}
	for i, e := range extra {
		w, err := mw.CreateFormField(fmt.Sprintf("extra%d", i))
		if err != nil {
			return nil, "", err
		}
		_, _ = w.Write([]byte(e))
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return &buf, mw.FormDataContentType(), nil
}

func multipartBody(t testing.TB, pkg []byte, extra ...string) (*bytes.Buffer, string) {
	t.Helper()
	b, ct, err := makeMultipart(pkg, extra...)
	if err != nil {
		t.Fatal(err)
	}
	return b, ct
}

// kitSubject is who acts on every request of the kit's Host.
var kitSubject = authz.Subject{Kind: "user", ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}

// writeHost is a Host for the tests of the write side: a local repository "main" with the
// twin of the data port, bound to the Host's revisions.
type writeHost struct {
	*formattest.Host
	f  *Format
	st *nugettest.Store
}

func newWriteHost(t testing.TB, o formattest.HostOptions) *writeHost {
	t.Helper()
	st := nugettest.New()
	o.Data = st
	if o.Resolver == nil {
		// The kit's Host has no virtual engine; a first push proceeds unless a test says the
		// shadowing check refuses.
		o.Resolver = shadowResolver{out: formats.ShadowOutcome{Proceed: true}}
	}
	if o.Subject.Name == "" {
		o.Subject = kitSubject
	}
	f := New()
	h := formattest.NewHost(f, o)
	st.Revisions = func(_ int64, lowerID string) (int64, string) {
		display := lowerID
		if row, ok, _ := h.Packages().Package(context.Background(), h.Ref(), lowerID); ok {
			display = row.DisplayName
		}
		return int64(h.RevSeq(lowerID)), display
	}
	return &writeHost{Host: h, f: f, st: st}
}

// deleteMode sets nuget.deleteMode of the repository document.
func (h *writeHost) deleteMode(mode string) {
	h.Repo().Config = []byte(`{"nuget":{"deleteMode":"` + mode + `"}}`)
}

// push sends a package the way a client does and returns the response.
func (h *writeHost) push(t testing.TB, pkg []byte) *formattest.Response {
	t.Helper()
	body, ct := multipartBody(t, pkg)
	resp, err := h.Serve(context.Background(), http.MethodPut, "v3/package", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *writeHost) do(t testing.TB, method, path string) *formattest.Response {
	t.Helper()
	resp, err := h.Serve(context.Background(), method, path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// mustPush pushes a package and fails unless it answers 201.
func (h *writeHost) mustPush(t testing.TB, id, version string, opts ...pkgOpt) []byte {
	t.Helper()
	pkg := buildNupkg(t, id, version, opts...)
	if r := h.push(t, pkg); r.Code != http.StatusCreated {
		t.Fatalf("push %s %s: status %d: %s", id, version, r.Code, r.Body.String())
	}
	return pkg
}

// code reads the code of a native error body.
func code(t testing.TB, r *formattest.Response) string {
	t.Helper()
	c, _, ok := strings.Cut(r.Body.String(), ": ")
	if !ok {
		t.Fatalf("status %d: %q is no native error body", r.Code, r.Body.String())
	}
	return c
}

func sha512Of(b []byte) []byte { s := sha512.Sum512(b); return s[:] }

func (h *writeHost) hasItem(t testing.TB, p string) bool {
	t.Helper()
	_, ok, err := h.Items().Stat(context.Background(), h.Ref(), formats.Path{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	return ok
}
