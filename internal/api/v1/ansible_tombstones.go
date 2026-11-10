package v1

import (
	"encoding/hex"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/httpserver"
)

type ansibleTombstone struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	SHA256    string    `json:"sha256"`
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy"`
}

// ListAnsibleTombstones implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/tombstones (R-ANS-012): the deleted
// versions whose numbers are withheld, in collection and version order.
func (s *Server) ListAnsibleTombstones(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, _ gen.ListAnsibleTombstonesParams) {
	store, repo, ok := s.ansibleTarget(w, r, projectKey, repoKey, false)
	if !ok {
		return
	}
	lq, err := ParseList(r, ListSpec{Sorts: []string{"name"}}, s.o.API.Config)
	if err == nil && lq.After != nil && len(lq.After) != 1 {
		err = invalidCursor()
	}
	var list []rows.Tombstone
	if err == nil {
		after := ""
		if lq.After != nil {
			after = lq.After[0]
		}
		list, err = store.Tombstones(r.Context(), repo.NumericID(), after, lq.FetchLimit())
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listAnsibleTombstones"))
		return
	}
	WritePage(w, r, MapPage(NewPage(lq, list, func(t rows.Tombstone) []string { return []string{t.Name + "/" + t.Version} }), func(t rows.Tombstone) ansibleTombstone {
		return ansibleTombstone{Name: t.Name, Version: t.Version, SHA256: hex.EncodeToString(t.SHA256[:]), DeletedAt: t.DeletedAt, DeletedBy: t.DeletedBy}
	}))
}

// ClearAnsibleTombstone implements DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}/ansible/tombstones/{collection}/{version}
// (R-ANS-012): the clearing of one tombstone, which writes the journaled event ansible.tombstone.cleared in the same transaction.
func (s *Server) ClearAnsibleTombstone(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, collection, ver string) {
	store, repo, ok := s.ansibleTarget(w, r, projectKey, repoKey, false)
	if !ok {
		return
	}
	if !version.Valid(ver) {
		httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalid, "The version is not a semantic version."))
		return
	}
	found, err := store.ClearTombstone(r.Context(), repo.NumericID(), collection, ver, ansibleAudit(r, repo))
	if err == nil && !found {
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The collection "+collection+" has no tombstone for the version "+version.Number(ver)+".")
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.clearAnsibleTombstone"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
