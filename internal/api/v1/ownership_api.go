package v1

import (
	"context"
	"net/http"
	"net/url"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
)

// The ownership operations of R-AUTHZ-019. Who may change the owners of a name
// depends on the owners themselves (an owner, manage on the name,
// content:bypass), so the declarations decide only what they can: the owners
// view a repository-level read, the writes and the list an authenticated
// caller. Each handler takes the request's holder and asks the ownership
// service on its index, which decides the rest, normalizes the name with the
// format's rule and keeps the answers of R-AUTHZ-016: a refused write is 403
// whether or not the target exists, a refused read 404.

// ownership returns the service ([AuthzServices.Owners]) and the holder of the
// request's index, made to know the repository keys the request names.
func (s *Server) ownership(ctx context.Context, keys ...string) (context.Context, *authz.Ownership, *edge.Access, error) {
	svc, eng := s.o.Authz.Owners, s.o.Authz.Engine
	if svc == nil || eng == nil {
		return ctx, nil, nil, unavailable("The ownership service")
	}
	ctx, acc, err := requestHolder(ctx, eng, keys...)
	if err != nil {
		return ctx, nil, nil, err
	}
	if acc == nil {
		return ctx, nil, nil, errNoDecisionPoint()
	}
	return ctx, svc, acc, nil
}

// ownersHref is the owners view of a name, the Location of an added owner: the
// URL on which a GET shows it (R-API-004), since R-AUTHZ-019 has no read of one
// owner. The name is one path segment, its "/" encoded.
func ownersHref(base, repoKey, name string) string {
	return base + "/api/v1/repositories/" + url.PathEscape(repoKey) + "/packages/" + url.PathEscape(name) + "/owners"
}

func packageOwnerOf(o authz.Owner) gen.PackageOwner {
	out := gen.PackageOwner{Owner: o.Owner, Display: o.Display, Reason: gen.PackageOwnerReason(o.Reason), AddedAt: o.AddedAt}
	if o.AddedBy != "" {
		by := o.AddedBy
		out.AddedBy = &by
	}
	return out
}

// GetPackageOwners implements GET /api/v1/repositories/{repoKey}/packages/{packageName}/owners.
func (s *Server) GetPackageOwners(w http.ResponseWriter, r *http.Request, repoKey, packageName string) {
	ctx, svc, acc, err := s.ownership(r.Context(), repoKey)
	var po authz.PackageOwners
	if err == nil {
		po, err = svc.Owners(ctx, acc.Index(), acc.Subject, repoKey, packageName)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.PackageOwners{
		Repository: po.Repository, Domain: po.Domain, Format: po.Format, Package: po.Package,
		State: gen.PackageOwnersState(po.State), Owners: make([]gen.PackageOwner, 0, len(po.Owners)),
	}
	for _, o := range po.Owners {
		out.Owners = append(out.Owners, packageOwnerOf(o))
	}
	writeJSON(w, r, http.StatusOK, out)
}

// AddPackageOwner implements POST /api/v1/repositories/{repoKey}/packages/{packageName}/owners:
// the owner and its audit event in the transaction of an Idempotency-Key
// (R-API-014), so a replay answers the stored outcome.
func (s *Server) AddPackageOwner(w http.ResponseWriter, r *http.Request, repoKey, packageName string, _ gen.AddPackageOwnerParams) {
	ctx, svc, acc, err := s.ownership(r.Context(), repoKey)
	if err == nil && s.idem == nil {
		err = unavailable("The idempotency store")
	}
	var body []byte
	var in gen.PackageOwnerInput
	if err == nil {
		body, err = ReadBody(w, r, MediaJSON, s.bodyLimit())
	}
	if err == nil {
		err = StrictUnmarshal(body, &in)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	base := httpserver.InfoFrom(ctx).BaseURL
	err = s.idem.Do(w, r.WithContext(ctx), body, IdempotentOp{Run: func(ctx context.Context, tx meta.Tx) (Outcome, error) {
		o, err := svc.AddOwnerTx(ctx, tx, acc.Index(), acc.Subject, repoKey, packageName, in.Owner)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{
			Status: http.StatusCreated, Body: packageOwnerOf(o),
			Header: map[string]string{"Location": ownersHref(base, repoKey, svc.PackageName(acc.Index(), repoKey, packageName))},
		}, nil
	}})
	if err != nil {
		httpserver.Problem(w, r, err)
	}
}

// RemovePackageOwner implements DELETE /api/v1/repositories/{repoKey}/packages/{packageName}/owners/{owner}.
func (s *Server) RemovePackageOwner(w http.ResponseWriter, r *http.Request, repoKey, packageName, owner string, params gen.RemovePackageOwnerParams) {
	ctx, svc, acc, err := s.ownership(r.Context(), repoKey)
	if err == nil {
		release := params.Release != nil && *params.Release
		err = svc.RemoveOwner(ctx, acc.Index(), acc.Subject, repoKey, packageName, owner, release)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOwnedPackages implements GET /api/v1/package-owners: the names an identity
// owns in key order. The cursor holds the key of the last row the service read
// for the page; a page of a caller that sees only readable repositories may be
// shorter than the limit and still have a next page.
func (s *Server) ListOwnedPackages(w http.ResponseWriter, r *http.Request, params gen.ListOwnedPackagesParams) {
	lq, err := ParseList(r, ListSpec{Filters: []string{"owner", "repository"}}, s.o.API.Config)
	var after authz.OwnedCursor
	if err == nil && lq.After != nil {
		if len(lq.After) != 3 {
			err = invalidCursor()
		} else {
			after = authz.OwnedCursor{Domain: lq.After[0], Format: lq.After[1], Package: lq.After[2]}
		}
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	repoKey := ""
	var keys []string
	if params.Repository != nil && *params.Repository != "" {
		repoKey = *params.Repository
		keys = []string{repoKey}
	}
	ctx, svc, acc, err := s.ownership(r.Context(), keys...)
	var rows []authz.OwnedPackage
	var next *authz.OwnedCursor
	if err == nil {
		rows, next, err = svc.OwnedBy(ctx, acc.Index(), acc.Subject, params.Owner, repoKey, after, lq.Limit)
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	page := Page[gen.OwnedPackage]{Items: make([]gen.OwnedPackage, 0, len(rows))}
	for _, p := range rows {
		page.Items = append(page.Items, gen.OwnedPackage{
			Repository: p.Repository, Format: p.Format, Package: p.Package,
			State: gen.OwnedPackageState(p.State), Reason: gen.OwnedPackageReason(p.Reason), AddedAt: p.AddedAt,
		})
	}
	if next != nil {
		c := lq.EncodeCursor([]string{next.Domain, next.Format, next.Package})
		page.NextCursor = &c
	}
	WritePage(w, r.WithContext(ctx), page)
}
