package oci

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// Every decision of an upload route names a path of the package (R-AUTHZ-005): the blob's path for a monolithic upload and a mount, the
// package's document path for a session; the ownership pre-check asks the same paths. It is the first version of the name while the
// repository holds no version of it, and no publish (Q-OCI-115).
func TestR_AUTHZ_005_EveryDecisionOfAnUploadRouteNamesAPathOfThePackage(t *testing.T) {
	var seen []formats.Target
	fx := withSessions(t, formattest.HostOptions{Allow: func(tg formats.Target) error {
		seen = append(seen, tg)
		return nil
	}})
	data := []byte("a layer")
	d := sha(data)
	src := fx.blob("base", []byte("the base"))
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "digest="+d, data), http.StatusCreated, "")
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+src+"&"+mountFrom, nil), http.StatusCreated, "")
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "", nil), http.StatusAccepted, "")
	for _, tg := range seen {
		if tg.Path == "" {
			t.Errorf("a decision without a path: %+v", tg)
		}
		if tg.Package == "" { // the in-memory host's own read of a link source names the path only
			continue
		}
		if info := fx.f.Classify(formats.Path{Path: tg.Path}); info.Package != tg.Package {
			t.Errorf("the path %q of the decision is in the package %q, the target names %q", tg.Path, info.Package, tg.Package)
		}
	}
	checks := fx.h.OwnershipChecks()
	want := []string{BlobPath("team/app", d), BlobPath("team/app", src), TaglistPath("team/app")}
	if len(checks) < len(want) {
		t.Fatalf("%d ownership checks, want %d: %+v", len(checks), len(want), checks)
	}
	for i, c := range checks[len(checks)-len(want):] {
		if c.Target.Path != want[i] || c.Target.Package != "team/app" {
			t.Errorf("ownership check %d on %+v, want %s", i, c.Target, want[i])
		}
		if c.Write.Publish || !c.Write.FirstVersion {
			t.Errorf("a blob of a name without a version is the first version and no publish: %+v", c.Write)
		}
		if info := fx.f.Classify(formats.Path{Path: c.Target.Path}); info.Package != "team/app" {
			t.Errorf("the path %q is in the package %q", c.Target.Path, info.Package)
		}
	}
}

// Once the name holds a version, a blob is no first version: the rule then needs an owner identity or `manage` (R-AUTHZ-018, Q-OCI-115).
func TestR_OCI_006_ABlobOfANameWithAVersionIsNoFirstVersion(t *testing.T) {
	fx := withSessions(t, formattest.HostOptions{})
	fx.image("team/app", "1", "layer")
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "", nil), http.StatusAccepted, "")
	checks := fx.h.OwnershipChecks()
	if last := checks[len(checks)-1]; last.Write.FirstVersion || last.Write.Publish || last.Target.Path != TaglistPath("team/app") {
		t.Fatalf("the check of a session start on a name with a version: %+v", last)
	}
	fx.wantStatus(fx.post("other/app/blobs/uploads/", "", nil), http.StatusAccepted, "")
	checks = fx.h.OwnershipChecks()
	if last := checks[len(checks)-1]; !last.Write.FirstVersion {
		t.Fatalf("the check on a name without a version: %+v", last)
	}
}

// countingBody counts what is read of it.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A caller who may not write a name that has an owner starts no session, uploads no blob and mounts none, and no byte of its body is read
// (R-AUTHZ-018, Q-OCI-115); a caller the decision refuses is refused before the ownership is asked.
func TestR_OCI_006_AnUploadToAnOwnedNameIsRefusedBeforeABodyIsRead(t *testing.T) {
	refuse := func(tg formats.Target, _ formats.OwnershipWrite) error {
		return errs.E(errs.Forbidden, errs.CodeForbidden, "owned by another")
	}
	fx := withSessions(t, formattest.HostOptions{Precheck: refuse})
	src := fx.blob("base", []byte("the base"))
	data := []byte("a layer")
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "", nil), http.StatusForbidden, "DENIED")
	body := &countingBody{r: bytes.NewReader(data)}
	resp, err := fx.h.Serve(context.Background(), http.MethodPost, "team/app/blobs/uploads/", body, false,
		func(r *http.Request) { r.URL.RawQuery = "digest=" + sha(data); r.ContentLength = int64(len(data)) })
	if err != nil {
		t.Fatal(err)
	}
	fx.wantStatus(resp, http.StatusForbidden, "DENIED")
	if body.n != 0 {
		t.Errorf("%d bytes of the body were read", body.n)
	}
	fx.wantStatus(fx.post("team/app/blobs/uploads/", "mount="+src+"&"+mountFrom, nil), http.StatusForbidden, "DENIED")
	if fx.up.started != 0 || stored(fx.h, BlobPath("team/app", sha(data))) || stored(fx.h, BlobPath("team/app", src)) {
		t.Fatalf("an owned name took an upload: %d sessions", fx.up.started)
	}
}

// The commit hook of a blob fails closed: a name that is no digest and a commit without a SHA-256 are refused as bytes of another digest are.
func TestR_OCI_012_TheBlobHookRefusesWhatItCannotVerify(t *testing.T) {
	f := New()
	d := sha([]byte("x"))
	good := bytes.Repeat([]byte{'a'}, 32)
	for name, ev := range map[string]formats.CommitEvent{
		"another digest":           {Path: formats.Path{Path: BlobPath("app", d)}, Digests: formats.Digests{SHA256: good}},
		"a name that is no digest": {Path: formats.Path{Path: "app/_blobs/latest"}, Digests: formats.Digests{SHA256: good}},
		"no SHA-256":               {Path: formats.Path{Path: BlobPath("app", d)}},
		"a short SHA-256":          {Path: formats.Path{Path: BlobPath("app", d)}, Digests: formats.Digests{SHA256: good[:16]}},
	} {
		err := f.OnCommit(context.Background(), nil, ev)
		if err == nil || errs.KindOf(err) != errs.Conflict {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := formats.CommitEvent{Path: formats.Path{Path: "app/_blobs/" + digestOf(good)}, Digests: formats.Digests{SHA256: good}}
	if err := f.OnCommit(context.Background(), nil, ok); err != nil {
		t.Errorf("a blob of its digest: %v", err)
	}
	if !strings.HasPrefix(ok.Path.Path, "app/_blobs/sha256:") {
		t.Fatal("test setup")
	}
}

// A caller a path grant refuses is refused by the decision, and never learns that the name has an owner (Q-OCI-116): the decision comes before the
// ownership question at a session start, as it does at a monolithic upload and a mount.
func TestR_OCI_006_ADecisionThatRefusesComesBeforeTheOwnershipQuestion(t *testing.T) {
	var precheck int
	fx := withSessions(t, formattest.HostOptions{
		Allow: func(formats.Target) error { return errs.E(errs.Forbidden, errs.CodeForbidden, "path refused") },
		Precheck: func(formats.Target, formats.OwnershipWrite) error {
			precheck++
			return errs.E(errs.Forbidden, errs.CodeForbidden, "owned by another")
		},
	})
	data := []byte("x")
	fx.wantStatus(fx.post("secret/app/blobs/uploads/", "", nil), http.StatusForbidden, "")
	fx.wantStatus(fx.post("secret/app/blobs/uploads/", "digest="+sha(data), data), http.StatusForbidden, "")
	if precheck != 0 {
		t.Errorf("the ownership was asked %d times of a caller the decision refused", precheck)
	}
}
