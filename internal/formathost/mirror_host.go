package formathost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/mirror"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/repository"
)

// MirrorEngine is the remote engine as the mirror's host uses it: the engine of the client path and the two background entry
// points of a pass; *proxy.Engine implements it.
type MirrorEngine interface {
	RemoteEngine
	Fill(ctx context.Context, rq proxy.Request) (proxy.FillOutcome, error)
	Get(ctx context.Context, g proxy.Get) (*proxy.GetResult, error)
}

var _ MirrorEngine = (*proxy.Engine)(nil)

// MirrorHost implements [mirror.Host] (Q-ARCH-288, docs/developer/mirror.md): what the mirror engine needs of the format host and
// the remote engine for the passes of one process. Every request it sends is a background request of the pass: it takes a
// background slot within the pass's ceiling, a client fetch can pre-empt it, its outcomes neither move nor reset the circuit,
// and its upstream log record carries flight_role mirror and the pass's job (R-REPO-053 to R-REPO-057).
//
// A format's own requests during a pass (its gate revalidating a package document, its lister reading a document) are background
// requests too: the context of the pass carries the pass, and the client path's wrapper over the engine builds a background request
// from it (Q-ARCH-311). The Host a format is handed is the format host itself.
type MirrorHost struct {
	h *Host
	e MirrorEngine
}

var _ mirror.Host = (*MirrorHost)(nil)

// NewMirrorHost builds the host of a mirror's passes over the format host and the remote engine.
func NewMirrorHost(h *Host, e MirrorEngine) (*MirrorHost, error) {
	switch {
	case h == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: the mirror host needs the format host")
	case e == nil:
		return nil, errs.E(errs.Internal, errs.CodeInternal, "formathost: the mirror host needs the remote engine")
	}
	return &MirrorHost{h: h, e: e}, nil
}

// mirrorActor and mirrorSubject are the system as the audit trail records it for what a pass stores (R-REPO-057).
var (
	mirrorActor   = authz.MirrorActor()
	mirrorSubject = authz.MirrorSubject()
)

// Lister implements [mirror.Host]: the discovery of the format's remote adapter, found through the capabilities Register read.
// Ready implements [mirror.Host]: a built host serves passes until its engine drains; a process that is shutting down starts no
// pass (an error wrapping mirror.ErrShuttingDown).
func (m *MirrorHost) Ready(context.Context) error {
	if d, ok := m.e.(interface{ Draining() bool }); ok && d.Draining() {
		return fmt.Errorf("%w: the remote engine is draining", mirror.ErrShuttingDown)
	}
	return nil
}

func (m *MirrorHost) Lister(format string) (discover.Lister, bool) {
	f, ok := m.h.o.Formats(format)
	if !ok || f.Remote() == nil {
		return nil, false
	}
	l := formats.RemoteCapabilities(formats.ID(format)).Mirror
	return l, l != nil
}

// Admits implements [mirror.Host]. A name is admitted when the repository's include and exclude patterns admit it (R-REPO-005),
// no claim refuses it at the remote (R-REPO-023, R-REPO-025 step 2: a global claim, and a name the format cannot fold) and the
// format has a document for it (the format's name rule). The claims are read through the routing's snapshot index; an error
// there is answered by the first call, so a pass fails at its start rather than skipping names.
func (m *MirrorHost) Admits(ctx context.Context, repo repository.Repository) (func(name string) bool, error) {
	f, ok := m.h.o.Formats(repo.Format)
	if !ok || f.Remote() == nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The format of the repository has no remote adapter.")
	}
	refuse := m.h.o.Refusal
	if refuse != nil {
		if _, err := refuse.RemoteRefused(ctx, repo, "probe", nil); err != nil {
			return nil, err
		}
	}
	byName := f.Descriptor().PatternSubject == formats.SubjectName
	return func(name string) bool {
		if name == "" {
			return false
		}
		if byName && !repo.Admits(name, false) {
			return false
		}
		if _, ok := f.PackageDocumentPath(name); !ok {
			return false
		}
		if _, ok := catalog.Default().NormalizeName(repo.Format, name); !ok {
			return false
		}
		if refuse != nil {
			if refused, err := refuse.RemoteRefused(ctx, repo, name, nil); err != nil || refused {
				return false // an index that cannot be read refuses: nothing is asked of the upstream for a name that cannot be checked
			}
		}
		return true
	}, nil
}

// Upstream implements [mirror.Host].
func (m *MirrorHost) Upstream(repo repository.Repository, pass mirror.Pass) (discover.Upstream, error) {
	lister, ok := m.Lister(repo.Format)
	if !ok {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The format of the repository does not discover.")
	}
	rem, err := proxy.NewRemote(repo)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The repository is not a usable remote repository.", errs.WithCause(err))
	}
	base, err := url.Parse(rem.Settings.URL)
	if err != nil {
		return nil, errs.E(errs.Internal, errs.CodeInternal, "The upstream URL of the repository does not parse.", errs.WithCause(err))
	}
	return &passUpstream{
		m: m, repo: repo, rem: rem, pass: pass, base: base, hosts: lister.MirrorHosts(base),
		settings: formatSettings(repo.Config, formats.ID(repo.Format)), since: time.Now(),
	}, nil
}

// Fill implements [mirror.Host]: the artifact goes through the format's gate and the remote engine as a background request.
func (m *MirrorHost) Fill(ctx context.Context, repo repository.Repository, pass mirror.Pass, item discover.Item) (mirror.FillResult, error) {
	ctx = withBackground(ctx, &backgroundFacts{h: m.h, jobID: pass.JobID, ceiling: pass.Ceiling})
	fetch := &remote{h: m.h, e: m.e}
	p := formats.Path{Path: item.Path}
	rq := m.request(ctx, repo, p)
	preq, err := fetch.request(ctx, rq, p, "", proxy.Artifact)
	if err != nil {
		return mirror.FillResult{}, err
	}
	// A stored artifact costs no upstream request, and no gate: the gate may revalidate the format's document.
	if _, cached, err := m.e.Cached(ctx, preq); err != nil && !proxy.IsLost(err) { // a lost item is filled again (Q-REPO-202)
		return mirror.FillResult{}, mapBackground(err)
	} else if cached {
		return mirror.FillResult{Present: true}, nil
	}
	expected, err := fetch.gate(ctx, rq, p)
	if err != nil {
		return mirror.FillResult{}, mapGate(err)
	}
	preq.Expected = proxy.Digests{SHA256: expected.SHA256, SHA512: expected.SHA512, SHA1: expected.SHA1}
	out, err := m.e.Fill(ctx, preq)
	if err != nil {
		return mirror.FillResult{}, mapFill(err)
	}
	return mirror.FillResult{Present: out.Present, Bytes: out.Bytes}, nil
}

// request is the request a format's gate is handed for a path of a pass: no client behind it, no route, the system as subject. Its
// Host is the format host itself, so that the format finds its per-replica state; the requests the gate makes through
// Host.Remote() are background requests because the context carries the pass ([withBackground]).
func (m *MirrorHost) request(ctx context.Context, repo repository.Repository, p formats.Path) *formats.Request {
	hr := (&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/"}, Header: http.Header{}}).WithContext(ctx)
	return &formats.Request{
		Request: hr, Repo: &repo, Route: &formats.Route{Name: "mirror.pass"}, Path: p, Subject: mirrorSubject,
		Host: m.h, Log: m.h.o.Log,
	}
}

// backgroundFacts are what makes a request of the client path's wrapper a background request of a pass.
type backgroundFacts struct {
	h       *Host
	jobID   string
	ceiling int
}

type backgroundKey struct{}

// withBackground marks ctx as the context of a pass's work. The key is unexported: no request of the client path carries it, and
// a format cannot set it.
func withBackground(ctx context.Context, b *backgroundFacts) context.Context {
	return context.WithValue(ctx, backgroundKey{}, b)
}

func backgroundFrom(ctx context.Context) (*backgroundFacts, bool) {
	b, ok := ctx.Value(backgroundKey{}).(*backgroundFacts)
	return b, ok
}

// apply makes preq a background request of the pass: the priority and the pass's facts, the system as caller (the upstream log
// record has no on_behalf_of) and the populate check that is the mirror's: it asks nobody, and the event of the fill names the
// system as actor and mirror as reason (R-REPO-057, Q-REPO-145).
func (b *backgroundFacts) apply(preq *proxy.Request, rq *formats.Request, info formats.PathInfo, p formats.Path) {
	preq.Priority, preq.Ceiling, preq.JobID, preq.Mode = proxy.PriorityBackground, b.ceiling, b.jobID, proxy.Normal
	preq.Caller = proxy.CallerFacts{Actor: mirrorActor}
	preq.Populate = &mirrorPopulate{h: b.h, coord: authz.Coordinate{
		Repository: rq.Repo.Name, Class: rq.Repo.Class, Format: rq.Repo.Format, Kind: authz.KindWrite,
		Path: p.Path, Package: info.Package, Version: info.Version, Actions: []formats.Action{formats.ActionRead},
		Addressed: rq.Addressed(),
	}}
}

// mirrorPopulate is the populate check of a pass's request: the system fills the cache, whatever the repository's cacheFillAction
// says of clients, and the fill transaction records the populate event with the system as actor and mirror as reason.
type mirrorPopulate struct {
	h     *Host
	coord authz.Coordinate
}

var _ proxy.Populate = (*mirrorPopulate)(nil)

// May implements [proxy.Populate].
func (*mirrorPopulate) May(context.Context) bool { return true }

// Deny implements [proxy.Populate]; a pass is never refused.
func (*mirrorPopulate) Deny(context.Context) error { return nil }

// Record implements [proxy.Populate]: authz.write.allowed, as a client's fill writes it (R-AUTHZ-008, R-AUTHZ-029).
func (p *mirrorPopulate) Record(ctx context.Context, tx meta.Tx) error {
	return p.h.o.Decider.RecordAllowed(ctx, tx, mirrorSubject, p.coord, authz.Decision{Outcome: authz.Allow, Reason: authz.ReasonMirror})
}

// Endpoint implements [proxy.Populate]: no endpoint, and one that fills.
func (*mirrorPopulate) Endpoint() (string, bool) { return "", true }

// passUpstream is the [discover.Upstream] a format is lent for one pass.
type passUpstream struct {
	m        *MirrorHost
	repo     repository.Repository
	rem      proxy.Remote
	pass     mirror.Pass
	base     *url.URL
	hosts    []string
	settings json.RawMessage
	// since is when this attempt of the pass began: a stored document that was validated before it is not current for the
	// pass's discovery, whatever its period says (see Document).
	since time.Time
}

var _ discover.Upstream = (*passUpstream)(nil)

// Base implements [discover.Upstream].
func (u *passUpstream) Base() *url.URL { b := *u.base; return &b }

// Settings implements [discover.Upstream].
func (u *passUpstream) Settings() json.RawMessage { return u.settings }

// Get implements [discover.Upstream] through [proxy.Engine.Get]: the host check, the credential rule and the background slot are
// the engine's; a rate-limit answer is returned as the engine's typed error, which discover.AsRateLimited finds.
func (u *passUpstream) Get(ctx context.Context, target *url.URL, accept string, max int64) (*discover.Response, error) {
	res, err := u.m.e.Get(ctx, proxy.Get{
		Remote: u.rem, URL: target, Accept: accept, Max: max, MirrorHosts: u.hosts, Ceiling: u.pass.Ceiling, JobID: u.pass.JobID,
	})
	if err != nil {
		return nil, mapBackground(err)
	}
	return &discover.Response{Status: res.Status, Header: res.Header, Body: res.Body}, nil
}

// Document implements [discover.Upstream]: the metadata document through the remote engine, as a background request
// (R-REPO-025 steps 4 to 10). A document the upstream does not have (a negative entry, 404 or 410), one that is gone and a
// package the repository counts as gone are found=false.
//
// A stored document counts as current only if it was validated after the attempt began: the plan of a package that a feed or a
// listing named has to see what the upstream announced, not a copy that is still fresh by its metadata period but older than the
// change (a client may have pulled the document minutes before). Such a document is revalidated once per attempt, one conditional
// request; an artifact that is stored costs none (R-REPO-051).
func (u *passUpstream) Document(ctx context.Context, path, variant string) ([]byte, bool, error) {
	ctx = withBackground(ctx, &backgroundFacts{h: u.m.h, jobID: u.pass.JobID, ceiling: u.pass.Ceiling})
	p := formats.Path{Path: path}
	rq := u.m.request(ctx, u.repo, p)
	doc, err := (&remote{h: u.m.h, e: u.m.e}).metadataSince(ctx, rq, p, variant, u.since)
	if err != nil {
		if ee, ok := errs.As(err); ok && ee.Code == errs.CodeNotFound {
			return nil, false, nil
		}
		return nil, false, mapBackground(err)
	}
	if doc == nil || doc.Status.Negative || doc.Bytes == nil || !doc.GoneAt.IsZero() {
		return nil, false, nil
	}
	b, err := doc.Bytes(ctx)
	if err != nil {
		return nil, false, mapBackground(err)
	}
	return b, true, nil
}

// Artifact implements [discover.Upstream]. The fill of an artifact through the gate is the work package of the OCI mirror
// (R-OCI-026); until it is built the call answers not_implemented, so that no format meets a silent no-op.
func (u *passUpstream) Artifact(context.Context, string, int64) ([]byte, bool, error) {
	return nil, false, errs.E(errs.NotImplemented, errs.CodeNotImplemented, "Filling an artifact on behalf of a format's plan is not built yet.")
}

// mapBackground turns the outcomes of a background request into the errors the mirror engine reads (mirror.Host): a
// pre-emption wraps mirror.ErrPreempted, a request the shutdown cancelled wraps mirror.ErrShuttingDown, an open circuit and a
// refused spool admission are a *mirror.WaitError, and a rate-limit answer is left as it is (discover.AsRateLimited finds it). Any
// other error is a failure of the item.
func mapBackground(err error) error {
	var w *proxy.BackgroundWait
	switch {
	case err == nil:
		return nil
	case errors.Is(err, proxy.ErrPreempted):
		return fmt.Errorf("%w: %w", mirror.ErrPreempted, err)
	case errors.As(err, &w):
		return &mirror.WaitError{After: w.After, Reason: w.Reason}
	}
	if ee, ok := errs.As(err); ok && ee.Code == errs.CodeShuttingDown {
		return fmt.Errorf("%w: %w", mirror.ErrShuttingDown, err)
	}
	return err
}

// mapFill is mapBackground for the error of the engine's fill, with the refusals that no retry of the pass changes
// (*mirror.RefusedError, docs/developer/mirror.md "What the host maps"): the upstream does not have the artifact (a negative
// entry, 404, 410, a package marked gone), the vulnerability scanning blocks the version, the populate check refuses.
// A repository that is offline (code not_cached_offline) is none of them: the engine reads the code and stops the pass.
func mapFill(err error) error {
	if m := mapBackground(err); m != err { //nolint:errorlint // identity: mapBackground returns err itself when it maps nothing
		return m
	}
	ee, ok := errs.As(err)
	if !ok {
		return err
	}
	switch ee.Code {
	case errs.CodeNotFound:
		return refused(mirror.RefusedNotFound, err)
	case errs.CodeVulnerabilityBlocked, errs.CodeVulnerabilityDataMissing:
		return refused(mirror.RefusedBlocked, err)
	case errs.CodePopulateNotPermitted:
		return refused(mirror.RefusedPopulate, err)
	}
	return err
}

// mapGate is mapBackground for the error of the format's artifact gate: a refusal of the format (not found, forbidden: a removed
// version, a mark) is permanent; the gate's own request failing is the failure of the item that it is.
func mapGate(err error) error {
	if m := mapBackground(err); m != err { //nolint:errorlint // identity, as in mapFill
		return m
	}
	if k := errs.KindOf(err); k == errs.NotFound || k == errs.Forbidden {
		if ee, ok := errs.As(err); !ok || ee.Code != errs.CodeNotCachedOffline {
			return refused(mirror.RefusedGate, err)
		}
	}
	return err
}

// refused is a *mirror.RefusedError that keeps the engine's error as its cause.
func refused(reason string, cause error) error {
	return &refusedError{RefusedError: mirror.RefusedError{Reason: reason}, cause: cause}
}

// refusedError carries the cause for errors.Is and errors.As; the engine reads the type *mirror.RefusedError through errors.As.
type refusedError struct {
	mirror.RefusedError
	cause error
}

func (e *refusedError) Unwrap() []error { return []error{&e.RefusedError, e.cause} }
