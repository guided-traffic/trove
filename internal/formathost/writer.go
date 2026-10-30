package formathost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
)

// OwnershipWrite says what a write is for the ownership rule (R-AUTHZ-018): a
// publish adds a version, a change of a dist-tag does not, and a first version
// creates the package and so claims it. It is the format contract's value.
type OwnershipWrite = formats.OwnershipWrite

// OwnershipCheck is what the host asks the ownership rule inside the commit
// transaction of a request (C-14): the index and the subject the request decides
// on, the coordinate of the package, and the kind of write.
type OwnershipCheck struct {
	Index      *authz.Index
	Subject    authz.Subject
	Coordinate authz.Coordinate
	Write      OwnershipWrite
}

// Ownership is the seam to the package-ownership rule of the decision point
// (R-AUTHZ-018, R-AUTHZ-015 re-check at commit). The composition root binds an
// adapter over authz.Ownership.EnforceTx; this package declares the seam because
// the rule lands in a package of its own. An error is the refusal
// (403 package_owned_by_other_owner), and it rolls the transaction back.
//
// Until a binding exists, [PackageWriter.Claim] refuses every call: a publish that
// cannot be checked is not allowed (fail closed).
type Ownership interface {
	EnforceTx(ctx context.Context, tx meta.Tx, chk OwnershipCheck) error
}

// AutoClaimer is the seam to the namespace auto claim of R-REPO-023 (C-15): it
// claims the package name for the repository's owner in the publish transaction
// when a claim rule asks for it, and returns what it did, which the audit event
// of the publish records. by is the caller of the request, the actor of the claim's
// own event. The composition root binds it over repository.Routing.AutoClaim.
// Unbound, AutoClaim refuses (fail closed).
type AutoClaimer interface {
	AutoClaimTx(ctx context.Context, tx meta.Tx, repo repository.Repository, name string, by repository.Caller) (repository.AutoClaimResult, error)
}

// RemoteRefuser decides the remote refusal of R-REPO-023: a remote repository answers
// 404 for a name a global namespace claim covers, also when it is addressed directly.
// The composition root binds it over repository.Routing.RemoteRefused. Unbound, the
// refusal does not apply (a replica without the routing has no claims).
type RemoteRefuser interface {
	RemoteRefused(ctx context.Context, remote repository.Repository, name string, via *repository.Repository) (bool, error)
}

// Changer writes the change-log row of a change area into the transaction
// (cluster.Changes implements it).
type Changer interface {
	Notify(tx meta.Tx, area string, version int, payload any) error
}

// Signaler attaches a signal to the transaction, published after the commit
// (cluster.Signals implements it).
type Signaler interface {
	Notify(tx meta.Tx, area, payload string)
}

// Jobs enqueues a job in the caller's transaction (jobs.Queue implements it).
type Jobs interface {
	Enqueue(ctx context.Context, tx meta.Tx, e jobs.EnqueueSpec) (jobs.Result, error)
}

// writerDeps are what a package writer needs beyond the rows.
type writerDeps struct {
	own      Ownership
	auto     AutoClaimer
	changes  Changer
	signals  Signaler
	enqueuer Jobs
	// remove removes items in the transaction; set for the writer of Host.Write.
	remove ItemRemover
}

// writer implements formats.PackageWriter over the package rows of a transaction.
// req is the request it is bound to, nil for a writer of a fill or a job.
type writer struct {
	w   *packages.Writer
	tx  meta.Tx
	d   writerDeps
	req *formats.Request
}

var _ formats.PackageWriter = (*writer)(nil)

func (w *writer) EnsureLocked(ctx context.Context, repo formats.RepoRef, name, displayName string) (formats.PackageRef, bool, error) {
	ref, created, err := w.w.EnsureLocked(ctx, repo.ID, name, displayName)
	if err != nil {
		return formats.PackageRef{}, false, err
	}
	return packageRef(repo, ref), created, nil
}

func packageRef(repo formats.RepoRef, r packages.Ref) formats.PackageRef {
	return formats.PackageRef{ID: formats.PackageID(r.ID), Repo: repo, Name: r.Name, RevSeq: formats.RevSeq(r.RevSeq)}
}

func pkgRef(p formats.PackageRef) packages.Ref {
	return packages.Ref{ID: int64(p.ID), RepoID: p.Repo.ID, Name: p.Name, RevSeq: int64(p.RevSeq)}
}

func (w *writer) Versions(ctx context.Context, pkg formats.PackageRef) ([]formats.VersionRow, error) {
	vs, err := w.w.Versions(ctx, pkgRef(pkg))
	if err != nil {
		return nil, err
	}
	return versionRows(vs), nil
}

func versionRows(vs []packages.Version) []formats.VersionRow {
	out := make([]formats.VersionRow, len(vs))
	for i, v := range vs {
		out[i] = formats.VersionRow{
			ID: formats.VersionID(v.ID), Version: v.Version, DisplayVersion: v.DisplayVersion, LeadItem: v.LeadItem,
			Attributes: v.Attributes, CreatedAt: v.CreatedAt, CreatedBy: v.CreatedBy,
		}
	}
	return out
}

func (w *writer) InsertVersion(ctx context.Context, pkg formats.PackageRef, v formats.NewVersion) (formats.VersionID, error) {
	nv := packages.NewVersion{Version: v.Version, DisplayVersion: v.DisplayVersion, Attributes: v.Attributes, CreatedBy: v.CreatedBy}
	if v.LeadItem != nil {
		nv.LeadItem = v.LeadItem.ID
	}
	id, err := w.w.InsertVersion(ctx, pkgRef(pkg), nv)
	return formats.VersionID(id), err
}

func (w *writer) TrashOrDeleteVersion(ctx context.Context, v formats.VersionID, purge bool) error {
	return w.w.TrashOrDeleteVersion(ctx, int64(v), purge)
}

func (w *writer) SetPackageAttributes(ctx context.Context, pkg formats.PackageRef, attrs json.RawMessage) error {
	return w.w.SetPackageAttributes(ctx, pkgRef(pkg), attrs)
}

func (w *writer) SetVersionAttributes(ctx context.Context, v formats.VersionID, attrs json.RawMessage) error {
	return w.w.SetVersionAttributes(ctx, int64(v), attrs)
}

func (w *writer) LinkItem(ctx context.Context, it formats.ItemRef, v formats.VersionID, lead bool) error {
	return w.w.LinkItem(ctx, it.ID, int64(v), lead)
}

func (w *writer) SetProperties(ctx context.Context, it formats.ItemRef, props map[string]string) error {
	return w.w.SetProperties(ctx, it.ID, props)
}

func (w *writer) BumpRevision(ctx context.Context, pkg formats.PackageRef) (formats.RevSeq, time.Time, error) {
	rev, at, err := w.w.BumpRevision(ctx, pkgRef(pkg))
	return formats.RevSeq(rev), at, err
}

// Claim decides ownership for the request this writer is bound to. A writer of no
// request - a fill, a job - decides none and refuses, as does a host with no
// ownership rule bound.
func (w *writer) Claim(ctx context.Context, t formats.Target, ow formats.OwnershipWrite) error {
	if w.req == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "Package ownership is decided for a request; this writer has none.")
	}
	if w.d.own == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "Package ownership is not wired on this replica; the write is refused.")
	}
	return w.d.own.EnforceTx(ctx, w.tx, OwnershipCheck{
		Index: w.req.Index, Subject: w.req.Subject, Coordinate: coordinate(w.req, t), Write: ow,
	})
}

// AutoClaim applies the namespace auto claim of the request's repository, as the
// caller of the request.
func (w *writer) AutoClaim(ctx context.Context, name string) (formats.AutoClaimResult, error) {
	if w.req == nil {
		return formats.AutoClaimResult{}, errs.E(errs.Internal, errs.CodeInternal, "The auto claim runs for a request; this writer has none.")
	}
	if w.d.auto == nil {
		return formats.AutoClaimResult{}, errs.E(errs.Internal, errs.CodeInternal, "The auto claim is not wired on this replica; the write is refused.")
	}
	by := repository.Caller{
		Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: w.req.Subject.Endpoint,
	}
	return w.d.auto.AutoClaimTx(ctx, w.tx, *w.req.Repo, name, by)
}

// DeleteItems removes the files at the canonical paths of a repository in this
// transaction, as the actor of the request this writer is bound to (R-STOR-018).
func (w *writer) DeleteItems(ctx context.Context, repo formats.RepoRef, paths ...string) error {
	if w.req == nil || w.d.remove == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "Items are removed for a request through Host.Write; this writer cannot.")
	}
	return w.d.remove.RemoveIn(ctx, w.tx, items.RemoveSpec{
		Repo:  items.RepoRef{ID: repo.ID, Key: repo.Key, Class: string(repo.Class), Format: string(repo.Format)},
		Paths: paths, Actor: edge.ActorFrom(httpserver.PrincipalFrom(ctx)), Source: httpserver.SourceFrom(ctx), Endpoint: w.req.Subject.Endpoint,
	})
}

// Notify attaches the notification to the transaction as its area's class says:
// a change area writes its change-log row in it, a signal area is published after
// the commit (R-ARCH-018).
func (w *writer) Notify(area string, payload any) error {
	a, ok := cluster.LookupArea(area)
	if !ok {
		return errs.E(errs.Internal, errs.CodeInternal, "Unknown notification area "+area+".")
	}
	switch a.Class {
	case cluster.ChangeArea:
		if w.d.changes == nil {
			return errs.E(errs.Internal, errs.CodeInternal, "The change log is not wired on this replica.")
		}
		return w.d.changes.Notify(w.tx, area, a.PayloadVersion, payload)
	default:
		if w.d.signals == nil { // a replica without signals loses the wake-up; consumers poll
			return nil
		}
		s, ok := payload.(string)
		if !ok {
			raw, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			s = string(raw)
		}
		w.d.signals.Notify(w.tx, area, s)
		return nil
	}
}

// Enqueue enqueues a job in the transaction.
func (w *writer) Enqueue(ctx context.Context, j formats.JobSpec) error {
	if w.d.enqueuer == nil {
		return errs.E(errs.Internal, errs.CodeInternal, "The job queue is not wired on this replica.")
	}
	spec := jobs.EnqueueSpec{Kind: j.Kind, Args: j.Args, DedupKey: j.DedupKey}
	if w.req != nil {
		spec.CreatedBy = principal.IDOrEmpty(w.req.Subject.ID)
	}
	_, err := w.d.enqueuer.Enqueue(ctx, w.tx, spec)
	return err
}
