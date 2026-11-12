package deb

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// The registration of R-DEB-001: the facts of the catalog entry.
func TestDescriptor(t *testing.T) {
	d := allClasses().Descriptor()
	if d.ID != "deb" || d.DisplayName != "Debian (APT)" || d.Layout != "deb" {
		t.Errorf("identity = %q %q %q", d.ID, d.DisplayName, d.Layout)
	}
	if !reflect.DeepEqual(d.Classes, []formats.Class{formats.Local, formats.Remote, formats.Virtual}) {
		t.Errorf("classes = %v", d.Classes)
	}
	if d.ProtocolBasePath != "/api/deb/{project}/{key}/" || d.ProbePath != "" {
		t.Errorf("addresses = %q %q", d.ProtocolBasePath, d.ProbePath)
	}
	// R-REPO-062: Debian (the default), Debian's security updates and Ubuntu's archive, security updates and ports, without hosts
	// and without keys (Q-REPO-326).
	wantPresets := []catalog.UpstreamPreset{
		{ID: "debian", DisplayName: "Debian", URL: "https://deb.debian.org/debian/", Default: true},
		{ID: "debian-security", DisplayName: "Debian security updates", URL: "https://deb.debian.org/debian-security/"},
		{ID: "ubuntu", DisplayName: "Ubuntu", URL: "https://archive.ubuntu.com/ubuntu/"},
		{ID: "ubuntu-security", DisplayName: "Ubuntu security updates", URL: "https://security.ubuntu.com/ubuntu/"},
		{ID: "ubuntu-ports", DisplayName: "Ubuntu ports", URL: "https://ports.ubuntu.com/ubuntu-ports/"},
	}
	if !reflect.DeepEqual(d.UpstreamPresets, wantPresets) {
		t.Errorf("R-REPO-062 presets = %+v", d.UpstreamPresets)
	}
	if d.DefaultMetadataTTLSeconds == nil || *d.DefaultMetadataTTLSeconds != 600 || d.DefaultNotFoundTTLSeconds == nil || *d.DefaultNotFoundTTLSeconds != 300 {
		t.Errorf("cache periods = %v %v", d.DefaultMetadataTTLSeconds, d.DefaultNotFoundTTLSeconds)
	}
	if d.PatternSubject != formats.SubjectName || !d.PackageNames || !d.PriorityMembers || !d.PinsAndClaims || d.Caps {
		t.Errorf("names and members = %v %v %v %v %v", d.PatternSubject, d.PackageNames, d.PriorityMembers, d.PinsAndClaims, d.Caps)
	}
	if d.OwnershipDefault != catalog.OwnershipEnforce || d.VerifyAnnouncedDigests || d.ChecksumAlwaysVerified {
		t.Errorf("ownership %q, digests %v %v", d.OwnershipDefault, d.VerifyAnnouncedDigests, d.ChecksumAlwaysVerified)
	}
	if d.DataPort != reflect.TypeFor[Store]() || d.PropertyPrefix != "deb." || d.ContractVersion != formats.ContractVersion {
		t.Errorf("port %v, prefix %q, version %d", d.DataPort, d.PropertyPrefix, d.ContractVersion)
	}
	if _, ok := allClasses().PackageDocumentPath("hello"); ok {
		t.Error("an APT archive has a package document")
	}
}

// A class joins the format by adding a class file (format.go).
func TestNewRegistersTheClassesOfTheClassFiles(t *testing.T) {
	f := New()
	if got := f.Classes(); !reflect.DeepEqual(got, []formats.Class{formats.Local, formats.Remote, formats.Virtual}) {
		t.Fatalf("classes = %v", got)
	}
	if err := formats.Validate(f); err != nil {
		t.Fatal(err)
	}
	if d := f.Descriptor(); !d.Supports(formats.Remote) || !d.Supports(formats.Virtual) {
		t.Errorf("the descriptor lists %v", d.Classes)
	}
}

// Every route answers 501 deb_not_implemented until a work package registers the handler of its name (R-DEB-005).
func TestRoutesAnswer501UntilTheirHandlerIsRegistered(t *testing.T) {
	f := allClasses()
	if len(f.Routes()) == 0 {
		t.Fatal("no routes")
	}
	for _, r := range f.Routes() {
		if handlerRegistered(r.Name) {
			continue
		}
		err := r.Handler(context.Background(), nil, nil)
		var e *errs.Error
		if !errorsAs(err, &e) || e.Code != errs.CodeDebNotImplemented || e.Status() != http.StatusNotImplemented {
			t.Errorf("route %s = %v", r.Name, err)
		}
	}
}

func TestRegisteredHandlerIsUsed(t *testing.T) {
	const name = "deb.test.route"
	called := false
	registerHandler(name, func(*Format) formats.HandlerFunc {
		return func(context.Context, *formats.Request, http.ResponseWriter) error { called = true; return nil }
	})
	t.Cleanup(func() {
		handlerRegistry.mu.Lock()
		delete(handlerRegistry.by, name)
		handlerRegistry.mu.Unlock()
	})
	if err := New().handlerFor(name)(context.Background(), nil, nil); err != nil || !called {
		t.Fatalf("handler called %v, err %v", called, err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a second registration of a route did not panic")
		}
	}()
	registerHandler(name, nil)
}

func errorsAs(err error, target **errs.Error) bool {
	e, ok := errs.As(err)
	if ok {
		*target = e
	}
	return ok
}
