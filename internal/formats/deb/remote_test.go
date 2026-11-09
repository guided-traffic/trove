package deb

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/openpgp"
)

func TestAnalyzeRemoteSaysWhatEachPathOfAnArchiveIs(t *testing.T) {
	hex := strings.Repeat("a", 64)
	for path, want := range map[string]remoteKind{
		"dists/stable/InRelease":                                 rkRelease,
		"dists/stable/updates/Release.gpg":                       rkRelease,
		"dists/stable/main/binary-amd64/Packages.xz":             rkIndex,
		"dists/stable/main/Contents-amd64.gz":                    rkIndex,
		"dists/stable/main/i18n/Translation-en.xz":               rkIndex,
		"dists/stable/main/binary-amd64/Packages.diff/Index":     rkIndex,
		"dists/stable/main/binary-amd64/Packages.diff/2026-x.gz": rkPatch,
		"dists/stable/main/binary-amd64/by-hash/SHA256/" + hex:   rkByHash,
		"dists/stable/main/binary-amd64/by-hash/MD5Sum/" + hex:   rkNoHash,
		"dists/stable/main/binary-amd64/by-hash/SHA1/" + hex:     rkNoHash,
		"dists/stable":                          rkNone,
		"pool/main/h/hello/hello_1.0_amd64.deb": rkContent,
		"binary/InRelease":                      rkRelease,
		"InRelease":                             rkRelease,
		"binary/Packages.gz":                    rkIndex,
		"binary/Packages.diff/Index":            rkIndex,
		"binary/Packages.diff/patch.gz":         rkPatch,
		"binary/hello_1.0_all.deb":              rkContent,
	} {
		if got := analyzeRemote(path).kind; got != want {
			t.Errorf("%s = %d, want %d", path, got, want)
		}
	}
	rp := analyzeRemote("dists/stable/updates/InRelease")
	if rp.dir != "dists/stable/updates" || rp.doc != FileInRelease || suiteOf(rp.dir) != "stable/updates" {
		t.Errorf("release path = %+v", rp)
	}
	if rp := analyzeRemote("dists/stable/main/binary-amd64/by-hash/SHA256/" + hex); rp.digest != hex || rp.algo != "SHA256" {
		t.Errorf("by-hash path = %+v", rp)
	}
}

func TestDirCandidatesAreTheDirectoriesAbovePathTheShortestFirst(t *testing.T) {
	got := dirCandidates("dists/stable/updates/main/binary-amd64/Packages.gz")
	want := []string{"dists/stable", "dists/stable/updates", "dists/stable/updates/main", "dists/stable/updates/main/binary-amd64"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dists = %v", got)
	}
	got = dirCandidates("a/b/Packages.gz")
	if strings.Join(got, "|") != "|a|a/b" {
		t.Errorf("flat = %q", got)
	}
	if got := dirCandidates("Packages.gz"); len(got) != 1 || got[0] != "" {
		t.Errorf("flat root = %q", got)
	}
}

func remoteContext(t *testing.T, base, keys string) formats.RemoteContext {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	member := `{}`
	if keys != "" {
		member = `{"upstreamKeys":` + jsonString(keys) + `}`
	}
	return formats.RemoteContext{Upstream: u, Settings: []byte(member)}
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer("\n", `\n`, `"`, `\"`).Replace(s) + `"`
}

func TestUpstreamRequestIsThePathBelowTheUpstreamURL(t *testing.T) {
	a := New().Remote()
	for _, c := range []struct{ base, path, want string }{
		{"https://deb.example/debian/", "dists/stable/InRelease", "https://deb.example/debian/dists/stable/InRelease"},
		{"https://deb.example/debian", "pool/main/libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb", "https://deb.example/debian/pool/main/libx/libxml2/libxml2-utils_2.9.14+dfsg-1.3_amd64.deb"},
		{"http://127.0.0.1:8080/flat/", "binary/Packages.gz", "http://127.0.0.1:8080/flat/binary/Packages.gz"},
	} {
		req, err := a.UpstreamRequest(context.Background(), remoteContext(t, c.base, ""), formats.Path{Path: c.path}, "")
		if err != nil || req.URL.String() != c.want || req.Method != http.MethodGet {
			t.Errorf("%s + %s = %v, %v", c.base, c.path, req, err)
		}
	}
	if _, err := a.UpstreamRequest(context.Background(), remoteContext(t, "https://x/", ""), formats.Path{Path: "dists"}, ""); err == nil {
		t.Error("a path outside the layout was mapped")
	}
}

func TestRewriteIsTheIdentityAndExpectsNoWebPages(t *testing.T) {
	a := New().Remote()
	doc := []byte("-----BEGIN PGP SIGNED MESSAGE-----\n\nbytes")
	out, err := a.Rewrite(context.Background(), formats.RemoteContext{}, formats.Path{Path: "dists/stable/InRelease"}, doc, nil)
	if err != nil || !bytes.Equal(out, doc) {
		t.Errorf("Rewrite changed the document: %v", err)
	}
	for mt, want := range map[string]bool{"text/plain": true, "application/x-xz": true, "application/octet-stream": true, "text/html; charset=utf-8": false} {
		if got := a.ExpectsType(formats.Path{Path: "dists/stable/main/binary-amd64/Packages.xz"}, mt); got != want {
			t.Errorf("ExpectsType(%s) = %v", mt, got)
		}
	}
	if a.TracksVersionRemoval() {
		t.Error("an archive has no removal marks")
	}
}

// release is a Release document that the tests sign.
func releaseText(date time.Time, suite string) []byte {
	return []byte("Origin: test\nSuite: " + suite + "\nCodename: " + suite + "\nDate: " + date.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC") +
		"\nSHA256:\n " + strings.Repeat("0", 63) + "1 10 main/binary-amd64/Packages.gz\n")
}

func signedBy(t *testing.T, doc []byte) (inrelease []byte, armored string) {
	t.Helper()
	now := time.Now()
	secret, public, _, err := openpgp.Generate("test", now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key, err := openpgp.ParseSecret(secret, nil, now, openpgp.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	inrelease, err = openpgp.ClearSign(doc, []*openpgp.Key{key}, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return inrelease, string(public)
}

func prepare(t *testing.T, keys, path string, doc []byte) (formats.Prepared, error) {
	t.Helper()
	return New().Remote().PrepareFill(context.Background(), remoteContext(t, "https://deb.example/debian/", keys), formats.Path{Path: path}, "", bytes.NewReader(doc), int64(len(doc)))
}

func codeOf(err error) string {
	if e, ok := errs.As(err); ok {
		return e.Code
	}
	return ""
}

func TestPrepareFillChecksAReleaseBeforeItIsStored(t *testing.T) {
	now := time.Now()
	good, keys := signedBy(t, releaseText(now.Add(-time.Minute), "stable"))
	// With keys: a verified release of the requested suite is prepared, with the fingerprint that verified it.
	p, err := prepare(t, keys, "dists/stable/InRelease", good)
	if err != nil {
		t.Fatal(err)
	}
	rf, ok := p.Data.(*releaseFill)
	if !ok || rf.signer == "" || !rf.keysSet || rf.dir != "dists/stable" || rf.rel.Suite != "stable" {
		t.Errorf("prepared = %+v", p.Data)
	}
	// Another suite's release is refused, a foreign key is a signature failure with its label, and nothing is stored.
	if _, err := prepare(t, keys, "dists/testing/InRelease", good); codeOf(err) != errs.CodeDebReleaseInvalid {
		t.Errorf("another suite: %v", err)
	}
	_, other := signedBy(t, releaseText(now, "stable"))
	_, err = prepare(t, other, "dists/stable/InRelease", good)
	if codeOf(err) != errs.CodeDebUpstreamSignatureInvalid || SignatureReason(err) == "" {
		t.Errorf("foreign key: %v (reason %q)", err, SignatureReason(err))
	}
	// Without keys the structure is read and nothing is verified: the foreign signature is stored.
	if p, err = prepare(t, "", "dists/stable/InRelease", good); err != nil || p.Data.(*releaseFill).signer != "" {
		t.Errorf("without keys: %v", err)
	}
	// A document that is no release is refused with or without keys; one that is longer than 16 MiB too.
	if _, err := prepare(t, "", "dists/stable/InRelease", []byte("garbage")); codeOf(err) != errs.CodeDebReleaseInvalid {
		t.Errorf("garbage: %v", err)
	}
	expired, k2 := signedBy(t, append(releaseText(now.Add(-time.Minute), "stable"), []byte("Valid-Until: Mon, 02 Jan 2006 15:04:05 UTC\n")...))
	if _, err := prepare(t, k2, "dists/stable/InRelease", expired); codeOf(err) != errs.CodeDebReleaseInvalid {
		t.Errorf("expired Valid-Until: %v", err)
	}
	// A plain Release parses; a Release.gpg owns nothing to prepare.
	if p, err := prepare(t, keys, "dists/stable/Release", releaseText(now, "stable")); err != nil || p.Data.(*releaseFill).signer != "" {
		t.Errorf("Release: %v", err)
	}
	if p, err := prepare(t, keys, "dists/stable/Release.gpg", []byte("sig")); err != nil || p.Data != nil {
		t.Errorf("Release.gpg: %v", err)
	}
	// An index is no release: PrepareFill reads nothing of it.
	if p, err := prepare(t, keys, "dists/stable/main/binary-amd64/Packages.gz", []byte("x")); err != nil || p.Data != nil {
		t.Errorf("index: %v", err)
	}
}

func TestSignatureReasonIsEmptyForOtherErrors(t *testing.T) {
	if SignatureReason(errs.E(errs.UpstreamFailed, errs.CodeDebReleaseInvalid, "x")) != "" || SignatureReason(nil) != "" {
		t.Error("a release error carries a signature reason")
	}
}

func TestRemoteClassBringsTheAdapterAndTheCapabilities(t *testing.T) {
	f := New()
	if f.Remote() == nil {
		t.Fatal("the remote class is not registered")
	}
	if _, ok := f.Remote().(formats.FactsGate); !ok {
		t.Error("the adapter has no facts gate")
	}
	if _, ok := f.Remote().(formats.NameServer); !ok {
		t.Error("the adapter has no name server")
	}
	if _, ok := f.Remote().(formats.Revalidator); !ok {
		t.Error("the adapter has no revalidator")
	}
	key := f.ServeFactsKey(formats.RepoRef{Class: formats.Remote}, formats.Path{Path: "pool/main/h/hello/hello_1.0_amd64.deb"})
	if key.Kind != formats.ServeDebRemote {
		t.Errorf("a pool file reads the kind %q", key.Kind)
	}
	if k := f.ServeFactsKey(formats.RepoRef{Class: formats.Remote}, formats.Path{Path: "dists/stable/InRelease"}); !k.Zero() {
		t.Errorf("a release document reads %q", k.Kind)
	}
}
