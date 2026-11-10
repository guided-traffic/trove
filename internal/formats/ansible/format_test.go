package ansible

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// allClasses is the format with the three classes the format has once the remote and virtual classes are registered, so that the
// route table, which grows with the registered classes, is tested whole.
func allClasses() *Format {
	f := &Format{classes: []formats.Class{formats.Local, formats.Remote, formats.Virtual}}
	f.routes = f.buildRoutes()
	return f
}

// TestR_ANS_001_Descriptor holds the facts of the registration of R-ANS-001.
func TestR_ANS_001_Descriptor(t *testing.T) {
	d := allClasses().Descriptor()
	if d.ID != "ansible" || d.DisplayName != "Ansible Galaxy" || d.Layout != "ansible" {
		t.Errorf("identity = %q %q %q", d.ID, d.DisplayName, d.Layout)
	}
	if !reflect.DeepEqual(d.Classes, []formats.Class{formats.Local, formats.Remote, formats.Virtual}) {
		t.Errorf("classes = %v", d.Classes)
	}
	if d.ProtocolBasePath != "/api/ansible/{project}/{key}/" || d.DefaultUpstreamURL != "https://galaxy.ansible.com/api/" || d.ProbePath != "" ||
		!reflect.DeepEqual(d.DefaultExtraHosts, []string{"github.com", "codeload.github.com"}) {
		t.Errorf("addresses = %q %q %q %v", d.ProtocolBasePath, d.DefaultUpstreamURL, d.ProbePath, d.DefaultExtraHosts)
	}
	if d.DefaultMetadataTTLSeconds == nil || *d.DefaultMetadataTTLSeconds != 600 || d.DefaultNotFoundTTLSeconds == nil || *d.DefaultNotFoundTTLSeconds != 300 {
		t.Errorf("cache periods = %v %v", d.DefaultMetadataTTLSeconds, d.DefaultNotFoundTTLSeconds)
	}
	if d.PatternSubject != formats.SubjectName || !d.PackageNames || !d.PriorityMembers || !d.PinsAndClaims || d.Caps || !d.LowerCasePaths {
		t.Errorf("names and members = %v %v %v %v %v %v", d.PatternSubject, d.PackageNames, d.PriorityMembers, d.PinsAndClaims, d.Caps, d.LowerCasePaths)
	}
	if d.OwnershipDefault != catalog.OwnershipEnforce || !d.VerifyAnnouncedDigests || !d.ChecksumAlwaysVerified || d.ChecksumRequireUnsupported {
		t.Errorf("ownership %q, digests %v %v %v", d.OwnershipDefault, d.VerifyAnnouncedDigests, d.ChecksumAlwaysVerified, d.ChecksumRequireUnsupported)
	}
	if d.DataPort != reflect.TypeFor[Store]() || d.PropertyPrefix != "ansible." || d.ContractVersion != formats.ContractVersion || d.MaxDocumentBytes != 16<<20 {
		t.Errorf("port %v, prefix %q, version %d, document bound %d", d.DataPort, d.PropertyPrefix, d.ContractVersion, d.MaxDocumentBytes)
	}
	if p, ok := allClasses().PackageDocumentPath("community.general"); !ok || p.Path != "collections/community/general/collection.json" {
		t.Errorf("PackageDocumentPath = %q %v", p.Path, ok)
	}
}

// A class joins the format by adding a class file (format.go): with the local class file alone, the format has that class and the
// routes of the other classes are not in its table.
func TestNewRegistersTheClassesOfTheClassFiles(t *testing.T) {
	f := New()
	if got := f.Classes(); !reflect.DeepEqual(got, []formats.Class{formats.Local}) {
		t.Fatalf("classes = %v", got)
	}
	if err := formats.Validate(f); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Routes() {
		if !r.ServesClass(formats.Local) {
			t.Errorf("route %s is in the table of a format with the local class alone and does not serve it", r.Name)
		}
	}
}

// Every route of a class has a handler of that class registered (R-ANS-003 to R-ANS-009), and a route whose class registered none answers
// 501 ansible_not_implemented (R-ANS-007).
func TestRoutesOfARegisteredClassHaveHandlersAndTheRestAnswer501(t *testing.T) {
	f := New()
	handlerRegistry.mu.RLock()
	for _, r := range f.Routes() {
		for _, class := range f.Classes() {
			if r.ServesClass(class) && handlerRegistry.by[class][r.Name] == nil {
				t.Errorf("route %s serves the class %s and has no handler of it", r.Name, class)
			}
		}
	}
	handlerRegistry.mu.RUnlock()
	for _, class := range []formats.Class{formats.Local, formats.Remote, formats.Virtual} {
		rq := &formats.Request{Request: httptest.NewRequest(http.MethodGet, "/", nil), Repo: repoOfClass(class)}
		err := f.op("ansible.no.such.route")(context.Background(), rq, httptest.NewRecorder())
		e, ok := errs.As(err)
		if !ok || e.Kind != errs.NotImplemented || e.Code != errs.CodeAnsibleNotImplemented {
			t.Errorf("an unregistered route for a %s repository: error = %v, want 501 ansible_not_implemented", class, err)
		}
	}
}

// TestR_ANS_001_ADeclaredHandlerIsRunByItsClass: the handler of a class and a route is the one that runs, and no other class's.
func TestR_ANS_001_ADeclaredHandlerIsRunByItsClass(t *testing.T) {
	const route = "ansible.test.only"
	var ran formats.Class
	registerHandler(formats.Virtual, route, func(*Format) formats.HandlerFunc {
		return func(_ context.Context, rq *formats.Request, _ http.ResponseWriter) error {
			ran = rq.Repo.Class
			return nil
		}
	})
	h := (&Format{}).op(route)
	rq := &formats.Request{Request: httptest.NewRequest(http.MethodGet, "/", nil), Repo: repoOfClass(formats.Virtual)}
	if err := h(context.Background(), rq, httptest.NewRecorder()); err != nil || ran != formats.Virtual {
		t.Fatalf("virtual: err %v, ran %q", err, ran)
	}
	rq.Repo = repoOfClass(formats.Remote)
	if err := h(context.Background(), rq, httptest.NewRecorder()); err == nil {
		t.Fatal("the handler of the virtual class ran for a remote repository")
	}
	defer func() {
		if recover() == nil {
			t.Error("a handler registered twice did not panic")
		}
	}()
	registerHandler(formats.Virtual, route, func(*Format) formats.HandlerFunc { return nil })
}
