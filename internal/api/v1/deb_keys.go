package v1

import (
	"context"
	"net/http"
	"strings"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/qname"
)

// The signing-key operations of the Debian (APT) format (R-DEB-015, R-DEB-017, R-DEB-032). The permission of each is declared in
// api/openapi.yaml and enforced before the handler runs: `read` for the list, `manage` for every change. The handlers are thin: the
// data port (internal/formatdata/deb) validates, seals, writes and records. No answer, error or log line holds a private key; the
// passphrase of an import is used once and not kept.

// DebKeyStore is the part of the Debian data port the key operations use (apt-work-packages.md, 2.2).
type DebKeyStore interface {
	Keys(ctx context.Context, repoID int64) ([]rows.Key, error)
	CreateNext(ctx context.Context, in rows.NewKey) (rows.Key, error)
	Promote(ctx context.Context, repoID int64, actor string) (rows.Promotion, error)
	RetireNext(ctx context.Context, repoID int64, fingerprint, actor string) error
	ConfirmWithheld(ctx context.Context, repoID int64, fingerprint, actor string) error
	ReinstateKey(ctx context.Context, repoID int64, fingerprint, actor string) (rows.Reinstated, error)
}

// debKeyTarget resolves the repository of a key operation: the data port, then the repository of the path. A repository that is not a
// local or virtual Debian repository answers 400 not_applicable.
func (s *Server) debKeyTarget(w http.ResponseWriter, r *http.Request, project, repo string) (DebKeyStore, int64, bool) {
	store := s.o.DebKeys
	if store == nil || s.o.Repositories.Reader == nil {
		httpserver.Problem(w, r, unavailable("The Debian signing keys"))
		return nil, 0, false
	}
	v, err := s.o.Repositories.Reader.ByName(r.Context(), qname.Repo{Project: project, Key: repo})
	if err == nil && (v.Format != "deb" || string(v.Class) == "remote") {
		err = errs.E(errs.Invalid, errs.CodeNotApplicable, "Only a local or virtual Debian repository has signing keys; "+
			qname.Repo{Project: project, Key: repo}.String()+" is a "+string(v.Class)+" "+v.Format+" repository.")
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.debSigningKeys"))
		return nil, 0, false
	}
	return store, v.NumericID(), true
}

func debKeyRep(k rows.Key) gen.DebSigningKey {
	return gen.DebSigningKey{
		Fingerprint: k.Fingerprint, Algorithm: gen.DebSigningKeyAlgorithm(k.Algorithm), Bits: k.Bits, KeyCreatedAt: k.KeyCreated,
		BoundSince: k.BoundSince, State: gen.DebSigningKeyState(k.State), Origin: gen.DebSigningKeyOrigin(k.Origin), PublicKey: k.PublicKey,
		CreatedAt: k.CreatedAt, PromotedAt: k.PromotedAt, WithheldAt: k.WithheldAt, RetiredAt: k.RetiredAt,
	}
}

func (s *Server) debKeyActor(r *http.Request) string {
	return ActorFrom(httpserver.PrincipalFrom(r.Context())).ID
}

// ListDebSigningKeys implements GET /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys.
func (s *Server) ListDebSigningKeys(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey) {
	store, id, ok := s.debKeyTarget(w, r, projectKey, repoKey)
	if !ok {
		return
	}
	list, err := store.Keys(r.Context(), id)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.listDebSigningKeys"))
		return
	}
	out := gen.DebSigningKeyList{Keys: make([]gen.DebSigningKey, 0, len(list))}
	for _, k := range list {
		out.Keys = append(out.Keys, debKeyRep(k))
	}
	writeJSON(w, r, http.StatusOK, out)
}

// CreateDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys.
func (s *Server) CreateDebSigningKey(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey) {
	store, id, ok := s.debKeyTarget(w, r, projectKey, repoKey)
	if !ok {
		return
	}
	var body gen.DebSigningKeyCreate
	if err := DecodeJSON(w, r, &body, s.bodyLimit()); err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	generate := body.Generate != nil && *body.Generate
	imported := body.PrivateKey != nil && strings.TrimSpace(*body.PrivateKey) != ""
	if generate == imported {
		httpserver.Problem(w, r, errs.E(errs.Invalid, errs.CodeInvalid, `Give either {"generate": true} or a "privateKey" to import, not both and not neither (R-DEB-015).`))
		return
	}
	in := rows.NewKey{RepoID: id, Generate: generate, Actor: s.debKeyActor(r)}
	if imported {
		in.Armored = []byte(*body.PrivateKey)
		if body.Passphrase != nil {
			in.Passphrase = []byte(*body.Passphrase)
		}
	}
	k, err := store.CreateNext(r.Context(), in)
	clear(in.Armored)
	clear(in.Passphrase)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.createDebSigningKey"))
		return
	}
	writeJSON(w, r, http.StatusCreated, debKeyRep(k))
}

// PromoteDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/promote.
func (s *Server) PromoteDebSigningKey(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey) {
	store, id, ok := s.debKeyTarget(w, r, projectKey, repoKey)
	if !ok {
		return
	}
	p, err := store.Promote(r.Context(), id, s.debKeyActor(r))
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.promoteDebSigningKey"))
		return
	}
	writeJSON(w, r, http.StatusOK, debKeyRep(p.New))
}

// RetireDebSigningKey implements DELETE /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/{fingerprint}: it retires
// the next key, or confirms the retirement of a withheld one. The primary key and a retired one answer 409 deb_key_state.
func (s *Server) RetireDebSigningKey(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, fingerprint gen.DebFingerprint) {
	store, id, ok := s.debKeyTarget(w, r, projectKey, repoKey)
	if !ok {
		return
	}
	keys, err := store.Keys(r.Context(), id)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.retireDebSigningKey"))
		return
	}
	state := ""
	for _, k := range keys {
		if k.Fingerprint == fingerprint {
			state = k.State
		}
	}
	actor := s.debKeyActor(r)
	switch state {
	case rows.KeyNext:
		err = store.RetireNext(r.Context(), id, fingerprint, actor)
	case rows.KeyWithheld:
		err = store.ConfirmWithheld(r.Context(), id, fingerprint, actor)
	case "":
		err = errs.E(errs.NotFound, errs.CodeNotFound, "The repository has no signing key with this fingerprint.")
	default:
		err = errs.E(errs.Conflict, errs.CodeDebKeyState, "The key is "+state+"; only the next key or a withheld key can be retired (R-DEB-017, R-DEB-032).")
	}
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.retireDebSigningKey"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReinstateDebSigningKey implements POST /api/v1/projects/{projectKey}/repositories/{repoKey}/deb/signing-keys/{fingerprint}/reinstate.
func (s *Server) ReinstateDebSigningKey(w http.ResponseWriter, r *http.Request, projectKey gen.ProjectKey, repoKey gen.RepoKey, fingerprint gen.DebFingerprint) {
	store, id, ok := s.debKeyTarget(w, r, projectKey, repoKey)
	if !ok {
		return
	}
	res, err := store.ReinstateKey(r.Context(), id, fingerprint, s.debKeyActor(r))
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.reinstateDebSigningKey"))
		return
	}
	writeJSON(w, r, http.StatusOK, debKeyRep(res.Key))
}
