// Package formattest is the conformance kit of the format contract (R-ARCH-010):
// the in-memory Host a format runs against, the ten checks of R-ARCH-010 as a
// table-driven test a format calls from its own package, the fuzz helpers for
// Match and Inspect, and (in the subpackage crafted) the hostile inputs of check
// 6. The kit needs no PostgreSQL, no object store and no Valkey (R-ARCH-009
// acceptance): `go test ./internal/formats/...` runs it.
//
// The kit imports what the contract imports, so that check 7 runs the real
// formats.ServeRoute and the in-memory Host can hold a subject and a transaction
// (Q-ARCH-216). It never imports a concrete format.
package formattest

import (
	"context"
	"crypto/md5"  //nolint:gosec // MD5 is one of the digests a repository stores
	"crypto/sha1" //nolint:gosec // SHA-1 is one of the digests a repository stores
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/repository"
)

// DefaultBaseURL is the response base URL of the in-memory Host.
const DefaultBaseURL = "https://trove.test"

// HostOptions configure [NewHost]. The zero value is a local repository "main"
// of the format, with every authorization allowed and no data port.
type HostOptions struct {
	// Class is the class of the addressed repository; Local when empty.
	Class formats.Class
	// RepoKey is the repository key; "main" when empty.
	RepoKey string
	// BaseURL is the response base URL; [DefaultBaseURL] when empty.
	BaseURL string
	// Data is the in-memory twin of the format's data port. NewHost refuses it, as
	// the composition root does in production, when the descriptor declares a port
	// and Data does not implement it, or when Data is set and no port is declared.
	Data any
	// Allow decides Authorize; nil allows every target.
	Allow func(formats.Target) error
	// Claim decides PackageWriter.Claim; nil allows.
	Claim func(formats.Target, formats.OwnershipWrite) error
	// Precheck decides Host.CheckOwnership, the read-only early form of Claim; nil allows.
	Precheck func(formats.Target, formats.OwnershipWrite) error
	// Remote and Resolver are the engines; nil answers "not supported", as the
	// production Host does until an engine is bound.
	Remote   formats.RemoteFetcher
	Resolver formats.Resolver
	// RestoredAt is system_state restored_at.
	RestoredAt time.Time
	// Subject is who acts on every request the Host builds; the zero value is a
	// subject without a name, which is neither anonymous nor authenticated.
	Subject authz.Subject
}

// Host is the in-memory implementation of formats.Host: an item store, the
// package rows with packages.rev_seq, links, renditions, notifications, jobs, an
// authorization table and the format's data port. It mirrors the production Host
// where a format can tell the difference: a package row write raises rev_seq once
// per package per transaction (Q-ARCH-212), a rolled-back transaction leaves no
// row, no notification and no job, and the transaction is opaque. Every call that
// matters to a check is recorded.
type Host struct {
	f    formats.Format
	opts HostOptions
	repo *repository.Repository
	ref  formats.RepoRef

	mu        sync.Mutex
	nextID    int64
	items     map[string]*itemRec // by path
	byID      map[int64]*itemRec
	pkgs      map[string]*pkgRec // by normalized name
	pkgByID   map[formats.PackageID]*pkgRec
	vers      map[formats.VersionID]*verRec
	props     map[int64]map[string]string
	renders   map[formats.RenditionKey][]byte
	authz     []AuthorizeCall
	claims    []ClaimCall
	prechecks []ClaimCall
	notified  []Notification
	jobs      []formats.JobSpec
	routes    []string // names of the routes served
	reads     [][]byte // package bytes handed to a handler
	served    bool     // an item was served to a response
	violation []string
}

// AuthorizeCall is one call of Host.Authorize.
type AuthorizeCall struct {
	Route  string
	Target formats.Target
}

// ClaimCall is one call of PackageWriter.Claim.
type ClaimCall struct {
	Target formats.Target
	Write  formats.OwnershipWrite
}

// Notification is a notification a committed transaction sent.
type Notification struct {
	Area    string
	Payload any
}

var _ formats.Host = (*Host)(nil)

// NewHost creates the in-memory Host of a format. It panics when the data port
// binding is wrong, like the composition root refuses to start.
func NewHost(f formats.Format, o HostOptions) *Host {
	if o.Class == "" {
		o.Class = formats.Local
	}
	if o.RepoKey == "" {
		o.RepoKey = "main"
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	d := f.Descriptor()
	switch port := d.DataPort; {
	case port == nil && o.Data != nil:
		panic(fmt.Sprintf("formattest: %q declares no data port and HostOptions.Data is set", d.ID))
	case port != nil && o.Data == nil:
		panic(fmt.Sprintf("formattest: %q declares the data port %v and HostOptions.Data is not set", d.ID, port))
	case port != nil && !reflect.TypeOf(o.Data).Implements(port):
		panic(fmt.Sprintf("formattest: HostOptions.Data of type %T does not implement the data port %v of %q", o.Data, port, d.ID))
	}
	h := &Host{
		f: f, opts: o,
		repo:    &repository.Repository{ID: "1", Key: o.RepoKey, Class: o.Class, Format: string(d.ID), Lifecycle: repository.Active},
		items:   map[string]*itemRec{},
		byID:    map[int64]*itemRec{},
		pkgs:    map[string]*pkgRec{},
		pkgByID: map[formats.PackageID]*pkgRec{},
		vers:    map[formats.VersionID]*verRec{},
		props:   map[int64]map[string]string{},
		renders: map[formats.RenditionKey][]byte{},
	}
	h.ref = formats.RepoRef{ID: 1, Key: o.RepoKey, Class: o.Class, Format: d.ID}
	if m, ok := o.Remote.(*MemRemote); ok {
		m.note = h.noteRead
	}
	return h
}

// Format returns the format the Host serves.
func (h *Host) Format() formats.Format { return h.f }

// Repo returns the addressed repository as the format sees it.
func (h *Host) Repo() *repository.Repository { return h.repo }

// Ref returns the addressed repository as a hook meets it.
func (h *Host) Ref() formats.RepoRef { return h.ref }

// Authorizations returns the calls of Authorize so far.
func (h *Host) Authorizations() []AuthorizeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]AuthorizeCall(nil), h.authz...)
}

// Claims returns the calls of Claim so far.
func (h *Host) Claims() []ClaimCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ClaimCall(nil), h.claims...)
}

// Notifications returns what committed transactions sent, in order.
func (h *Host) Notifications() []Notification {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Notification(nil), h.notified...)
}

// Jobs returns the jobs committed transactions enqueued, in order.
func (h *Host) Jobs() []formats.JobSpec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]formats.JobSpec(nil), h.jobs...)
}

// Violations returns the contract violations the Host noticed while routes ran:
// package bytes in the response of a route that is no content route.
func (h *Host) Violations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.violation...)
}

// RoutesServed returns the names of the routes [Host.Serve] ran, in order.
func (h *Host) RoutesServed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.routes...)
}

func (h *Host) noteRead(b []byte) {
	h.mu.Lock()
	h.reads = append(h.reads, append([]byte(nil), b...))
	h.mu.Unlock()
}

func (h *Host) id() int64 {
	h.nextID++
	return h.nextID
}

// Authorize implements formats.Host.
func (h *Host) Authorize(_ context.Context, rq *formats.Request, t formats.Target) error {
	h.mu.Lock()
	h.authz = append(h.authz, AuthorizeCall{Route: rq.Route.Name, Target: t})
	h.mu.Unlock()
	if h.opts.Allow != nil {
		return h.opts.Allow(t)
	}
	return nil
}

// CheckOwnership implements formats.Host: it records the call and asks Precheck.
func (h *Host) CheckOwnership(_ context.Context, _ *formats.Request, t formats.Target, w formats.OwnershipWrite) error {
	h.mu.Lock()
	h.prechecks = append(h.prechecks, ClaimCall{Target: t, Write: w})
	h.mu.Unlock()
	if h.opts.Precheck != nil {
		return h.opts.Precheck(t, w)
	}
	return nil
}

// OwnershipChecks returns the calls of CheckOwnership so far.
func (h *Host) OwnershipChecks() []ClaimCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ClaimCall(nil), h.prechecks...)
}

// Ingest implements formats.Host.
func (h *Host) Ingest() formats.Ingestor { return ingestor{h} }

// Items implements formats.Host.
func (h *Host) Items() formats.ItemStore { return itemStore{h} }

// Packages implements formats.Host.
func (h *Host) Packages() formats.PackageStore { return packageStore{h} }

// Remote implements formats.Host.
func (h *Host) Remote() formats.RemoteFetcher {
	if h.opts.Remote != nil {
		return h.opts.Remote
	}
	return unsupportedRemote{}
}

// Resolver implements formats.Host.
func (h *Host) Resolver() formats.Resolver {
	if h.opts.Resolver != nil {
		return h.opts.Resolver
	}
	return unsupportedResolver{}
}

// Links implements formats.Host.
func (h *Host) Links(rq *formats.Request) formats.LinkBuilder {
	return links{base: h.opts.BaseURL, format: h.f.Descriptor().ID, key: rq.Repo.Key}
}

// Renditions implements formats.Host.
func (h *Host) Renditions() formats.RenditionCache { return renditions{h} }

// FormatData implements formats.Host.
func (h *Host) FormatData(id formats.ID) any {
	if id != h.f.Descriptor().ID {
		return nil
	}
	return h.opts.Data
}

// RestoredAt implements formats.Host.
func (h *Host) RestoredAt(context.Context) (time.Time, error) { return h.opts.RestoredAt, nil }

type renditions struct{ h *Host }

func (r renditions) Get(k formats.RenditionKey) ([]byte, bool) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	b, ok := r.h.renders[k]
	return b, ok
}

func (r renditions) Put(k formats.RenditionKey, doc []byte) {
	r.h.mu.Lock()
	defer r.h.mu.Unlock()
	r.h.renders[k] = append([]byte(nil), doc...)
}

// links builds the URLs of the in-memory Host (C-11).
type links struct {
	base   string
	format formats.ID
	key    string
}

func (l links) Base() string { return strings.TrimSuffix(l.base, "/") }
func (l links) Registry(servingKey string) string {
	return l.Base() + "/api/" + string(l.format) + "/" + servingKey + "/"
}
func (l links) Repository() string { return l.Registry(l.key) }
func (l links) Path(p string) string {
	return l.Repository() + escapePath(p)
}

func (l links) File(repoKey, p string) string {
	return l.Base() + "/files/" + repoKey + "/" + escapePath(p)
}

// Links returns the link builder the Host hands a request for the key; the
// kit's rewrite check uses it directly.
func Links(base string, format formats.ID, key string) formats.LinkBuilder {
	if base == "" {
		base = DefaultBaseURL
	}
	return links{base: base, format: format, key: key}
}

// escapePath escapes every segment once.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// EscapePath is the escaped form of a canonical path as a client sends it:
// every segment escaped once.
func EscapePath(p formats.Path) string {
	s := escapePath(p.Path)
	if p.Folder && p.Path != "" {
		s += "/"
	}
	return s
}

func notSupported() error {
	return errs.E(errs.Unavailable, errs.CodeUnavailable, "The engine is not supported by this host.")
}

type unsupportedRemote struct{}

func (unsupportedRemote) Metadata(context.Context, *formats.Request, formats.Path, string) (*formats.RemoteDocument, error) {
	return nil, notSupported()
}

func (unsupportedRemote) Artifact(context.Context, *formats.Request, formats.Path, formats.Digests, http.ResponseWriter) error {
	return notSupported()
}

func (unsupportedRemote) Cached(context.Context, *formats.Request, formats.Path) (formats.Item, bool, error) {
	return formats.Item{}, false, notSupported()
}

func (unsupportedRemote) EnsureCurrent(context.Context, *formats.Request, formats.Path, string) (*formats.RemoteDocument, formats.Currency, error) {
	return nil, 0, notSupported()
}

func (unsupportedRemote) EntryState(context.Context, *formats.Request, formats.Path) (formats.EntryState, error) {
	return formats.EntryState{}, notSupported()
}

func (unsupportedRemote) Exchange(context.Context, *formats.Request, formats.Exchange) (*http.Response, error) {
	return nil, notSupported()
}

type unsupportedResolver struct{}

func (unsupportedResolver) Artifact(context.Context, *formats.Request, formats.Path, formats.PathInfo, http.ResponseWriter) error {
	return notSupported()
}

func (unsupportedResolver) Metadata(context.Context, *formats.Request, formats.Path, formats.PathInfo, string) (*formats.Merged, error) {
	return nil, notSupported()
}

func (unsupportedResolver) DeployTarget(context.Context, *formats.Request) (*repository.Repository, error) {
	return nil, errs.E(errs.MethodNotAllowed, "no_deploy_target", "The repository has no deploy target.")
}

func (unsupportedResolver) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{}, notSupported()
}

// NewRequest builds the request the dispatcher would build for method and the
// escaped repository-relative path: the format's Match, the clean path, the
// params and the Host. The body may be nil.
func (h *Host) NewRequest(ctx context.Context, method, escapedPath string, body io.Reader) (*formats.Request, error) {
	route, params, err := h.f.Match(method, escapedPath)
	if err != nil {
		return nil, err
	}
	clean, err := repository.CleanPath(escapedPath)
	if err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "https", Host: strings.TrimPrefix(h.opts.BaseURL, "https://"), Path: "/api/" + string(h.f.Descriptor().ID) + "/" + h.repo.Key + "/" + clean.Path, RawPath: ""}
	hr, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	return &formats.Request{
		Request: hr, Repo: h.repo, Route: route, Params: params, Path: clean, Escaped: escapedPath,
		Subject: h.opts.Subject, Host: h, Log: slog.New(slog.DiscardHandler),
	}, nil
}

// bareRequest builds a request for a path whether or not a route matches it, for
// the adapters a check calls outside a route (Merge): the route is the matched
// one, or a placeholder.
func (h *Host) bareRequest(ctx context.Context, method, escapedPath string) *formats.Request {
	rq, err := h.NewRequest(ctx, method, escapedPath, nil)
	if err == nil {
		return rq
	}
	hr, _ := http.NewRequestWithContext(ctx, method, h.opts.BaseURL+"/", nil)
	clean, _ := repository.CleanPath(escapedPath)
	return &formats.Request{
		Request: hr, Repo: h.repo, Route: &formats.Route{Name: "formattest"}, Path: clean, Escaped: escapedPath,
		Subject: h.opts.Subject, Host: h, Log: slog.New(slog.DiscardHandler),
	}
}

// Response is what [Host.Serve] returns.
type Response struct {
	*httptest.ResponseRecorder
	Request *formats.Request
}

// Serve runs a request the way the dispatcher does: Match, the request, then
// formats.ServeRoute with the content wrapper. mods adjust the *http.Request
// (headers) before it runs. A package byte in the response of a route that is no
// content route is recorded as a violation (check 7).
func (h *Host) Serve(ctx context.Context, method, escapedPath string, body io.Reader, sameOrigin bool, mods ...func(*http.Request)) (*Response, error) {
	rq, err := h.NewRequest(ctx, method, escapedPath, body)
	if err != nil {
		return nil, err
	}
	for _, m := range mods {
		m(rq.Request)
	}
	return h.ServeRequest(rq, sameOrigin), nil
}

// ServeRequest runs a prepared request through formats.ServeRoute.
func (h *Host) ServeRequest(rq *formats.Request, sameOrigin bool) *Response {
	h.mu.Lock()
	h.routes = append(h.routes, rq.Route.Name)
	h.reads, h.served = nil, false
	h.mu.Unlock()

	rec := httptest.NewRecorder()
	formats.ServeRoute(h.f, rec, rq, sameOrigin)

	h.mu.Lock()
	defer h.mu.Unlock()
	if !rq.Route.Kind.ServesContent() {
		if h.served {
			h.violation = append(h.violation, fmt.Sprintf("route %q of kind %s served a stored item", rq.Route.Name, rq.Route.Kind))
		}
		body := rec.Body.Bytes()
		for _, r := range h.reads {
			if len(r) > 0 && len(body) > 0 && strings.Contains(string(body), string(r)) {
				h.violation = append(h.violation, fmt.Sprintf("route %q of kind %s returned package content (%d bytes it had read)", rq.Route.Name, rq.Route.Kind, len(r)))
				break
			}
		}
	}
	return &Response{ResponseRecorder: rec, Request: rq}
}

// digests computes the digests of a body in one pass.
func digests(b []byte) formats.Digests {
	s256, s512, s1, m5 := sha256.Sum256(b), sha512.Sum512(b), sha1.Sum(b), md5.Sum(b) //nolint:gosec // see the imports
	return formats.Digests{SHA256: s256[:], SHA512: s512[:], SHA1: s1[:], MD5: m5[:]}
}
