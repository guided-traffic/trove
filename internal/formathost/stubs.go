package formathost

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// The engines of the remote and the virtual class arrive with the first format
// that has those classes (R-REPO-024 and R-REPO-017; the bridges are remote.go and
// virtual.go of this package, added with them). A host without them answers every
// call with an error that says so: a format that registers the class gets the
// refusal in its own tests, not a silent miss.
func errNoRemote() error {
	return errs.E(errs.Internal, errs.CodeInternal, "The remote engine is not wired on this replica.")
}

func errNoResolver() error {
	return errs.E(errs.Internal, errs.CodeInternal, "Virtual resolution is not wired on this replica.")
}

type unsupportedRemote struct{}

func (unsupportedRemote) Metadata(context.Context, *formats.Request, formats.Path, string) (*formats.RemoteDocument, error) {
	return nil, errNoRemote()
}

func (unsupportedRemote) Artifact(context.Context, *formats.Request, formats.Path, formats.Digests, http.ResponseWriter) error {
	return errNoRemote()
}

func (unsupportedRemote) Cached(context.Context, *formats.Request, formats.Path) (formats.Item, bool, error) {
	return formats.Item{}, false, errNoRemote()
}

func (unsupportedRemote) EnsureCurrent(context.Context, *formats.Request, formats.Path, string) (*formats.RemoteDocument, formats.Currency, error) {
	return nil, 0, errNoRemote()
}

func (unsupportedRemote) EntryState(context.Context, *formats.Request, formats.Path) (formats.EntryState, error) {
	return formats.EntryState{}, errNoRemote()
}

type unsupportedResolver struct{}

func (unsupportedResolver) Artifact(context.Context, *formats.Request, formats.Path, formats.PathInfo, http.ResponseWriter) error {
	return errNoResolver()
}

func (unsupportedResolver) Metadata(context.Context, *formats.Request, formats.Path, formats.PathInfo, string) (*formats.Merged, error) {
	return nil, errNoResolver()
}

// DeployTarget answers what R-REPO-021 says of a virtual repository without a
// default deployment target, which is every one until resolution exists.
func (unsupportedResolver) DeployTarget(context.Context, *formats.Request) (*repository.Repository, error) {
	return nil, errs.E(errs.MethodNotAllowed, errs.CodeNoDeployTarget,
		"The virtual repository has no default deployment target, so it takes no deploys.", errs.WithAllow("GET", "HEAD"))
}

func (unsupportedResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{}, errNoResolver()
}

// missCache is the rendition cache of a host that has none: it never hits.
type missCache struct{}

func (missCache) Get(formats.RenditionKey) ([]byte, bool) { return nil, false }
func (missCache) Put(formats.RenditionKey, []byte)        {}
