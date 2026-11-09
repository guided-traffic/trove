package v1

import (
	"context"
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// DebServices are the services of the operations of the Debian (APT) format that the management API serves (apt-work-packages.md).
// The composition root fills them (internal/app/wire_deb_api.go); a nil member answers its operation with 503.
type DebServices struct {
	// Coordinates changes the coordinates of a stored package file.
	Coordinates DebCoordinateService
	// Index reads the index state of a local repository (deb_index.go).
	Index DebIndexService
}

// DebCoordinateChange is one replacement of the coordinates of a stored package file (R-DEB-009). The handler has resolved the
// repository and the caller; the service validates the lists, reads the file, asks Decide for the added and the removed
// coordinates and writes inside one transaction.
type DebCoordinateChange struct {
	RepoID int64
	Repo   qname.Repo
	Path   string
	// Coordinates is the list the file is to be listed in afterwards.
	Coordinates []gen.DebCoordinate
	// Decide answers the decision point on the request's index for one action on the file's package: deploy for an added
	// coordinate, delete for a removed one (R-DEB-004). Own is the ownership rule of the package in the transaction (R-AUTHZ-018).
	Decide func(pkg string, action authz.Action) error
	Own    func(ctx context.Context, tx meta.Tx, pkg string) error
	// Actor is the id of the principal of the request.
	Actor string
}

// DebCoordinateService replaces the coordinates and returns the answer of R-DEB-006 as its JSON value.
type DebCoordinateService interface {
	Replace(ctx context.Context, in DebCoordinateChange) (any, error)
}

// PutDebCoordinates implements PUT /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/coordinates (R-DEB-009): the replacement
// of the suites and components a stored package file is listed in. An added coordinate needs deploy and a removed one delete, both on
// the file's package and with its ownership; the decisions are taken with the request's subject and scope.
func (s *Server) PutDebCoordinates(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKeyText gen.RepoKey) {
	repoKey := qname.Repo{Project: projectKey, Key: repoKeyText}
	ctx, owners, acc, err := s.ownership(r.Context(), repoKey)
	var in gen.DebCoordinatesRequest
	var repo repository.Repository
	var repoID int64
	if err == nil && (s.o.Deb.Coordinates == nil || s.o.Packages.Reader == nil) {
		err = unavailable("The Debian repository operations")
	}
	if err == nil {
		err = DecodeJSON(w, r, &in, s.bodyLimit())
	}
	if err == nil {
		// A caller that may not see the repository learns nothing about it (R-AUTHZ-016).
		err = acc.Decide(ctx, authz.Coordinate{Repository: repoKey, Kind: authz.KindMetadata, Actions: []authz.Action{authz.Read}}).Err()
	}
	if err == nil {
		repo, err = s.o.Packages.Reader.ByName(ctx, repoKey)
	}
	if err == nil {
		repoID, err = parseRepositoryID(repo.ID)
	}
	if err == nil && (repo.Format != "deb" || repo.Class != repository.Local) {
		err = errs.E(errs.Invalid, errs.CodeUnsupportedClass, "Only a local Debian repository holds package files with coordinates of their own.")
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	decide := func(pkg string, action authz.Action) error {
		c := authz.Coordinate{Repository: repoKey, Package: pkg, Path: in.Path, Kind: authz.KindWrite, Actions: []authz.Action{action}}
		return acc.Decide(ctx, s.classified(ctx, c)).Err()
	}
	own := func(ctx context.Context, tx meta.Tx, pkg string) error {
		c := authz.Coordinate{Repository: repoKey, Package: pkg, Path: in.Path, Kind: authz.KindWrite, Actions: []authz.Action{authz.Deploy}}
		_, err := owners.EnforceTx(ctx, tx, acc.Index(), acc.Subject, c, authz.OwnershipWrite{Publish: true})
		return err
	}
	out, err := s.o.Deb.Coordinates.Replace(ctx, DebCoordinateChange{
		RepoID: repoID, Repo: repoKey, Path: in.Path, Coordinates: in.Coordinates,
		Decide: decide, Own: own, Actor: httpserver.PrincipalFrom(ctx).ID,
	})
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, out)
}
