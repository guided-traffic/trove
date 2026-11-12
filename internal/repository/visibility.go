package repository

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/visibility"
)

// The levels of a repository (R-AUTHZ-033, R-REPO-004). They are columns of the repository row; the document carries them as the
// member `visibility`, and a write names them through it. This file decides what a create or an update stores.

// Field codes of the level rules, in errors[] of a 422 validation_failed answer (R-AUTHZ-033 rule 2).
const (
	codeVisibilityAboveProject       = "visibility_above_project"
	codeVisibilityWikiAboveArtifacts = "visibility_wiki_above_artifacts"
	// WarningPublicPopulatesRemote is the warning of a write that opens a remote repository with cacheFillAction read to the
	// public: a request without a credential then fills the cache (R-AUTHZ-008).
	WarningPublicPopulatesRemote = "public_populates_remote"
)

// resolveLevels returns the levels a write stores and checks them against the rules of R-AUTHZ-033. cur is the stored levels of
// an update and nil for a create; d holds the levels the client named, where an empty one is absent. For a create an absent
// artifacts level is the lower of the installation's default and the project's level, and an absent wiki level the lower of that
// and the artifacts level. For an update an absent level stays as stored, so a PUT without the member changes no level.
//
// Violations of the container rules are added to v as field errors. A level that rises, relative to the stored one or, on a
// create, to what an absent member would store, asks the caller's RaiseGate (recent authentication), and a level that becomes
// public while the default endpoint admits no request without a credential answers 409 public_not_allowed.
func (s *Service) resolveLevels(ctx context.Context, qs *q.Queries, v *validation, c Caller, projectLevel string, cur *Visibility, d *Document) (Visibility, error) {
	def, err := visibility.Default(ctx, qs)
	if err != nil {
		return Visibility{}, err
	}
	baseline := Visibility{Artifacts: visibility.Lower(def, projectLevel)}
	baseline.Wiki = baseline.Artifacts
	if cur != nil {
		baseline = *cur
	}
	got := d.Visibility
	out := baseline
	if got.Artifacts != "" {
		out.Artifacts = got.Artifacts
	}
	switch {
	case got.Wiki != "":
		out.Wiki = got.Wiki
	case cur == nil:
		out.Wiki = visibility.Lower(baseline.Wiki, out.Artifacts)
	}
	if !visibility.Valid(out.Artifacts) {
		v.fields = append(v.fields, visibility.InvalidLevel("/visibility/artifacts"))
		return baseline, nil
	}
	if !visibility.Valid(out.Wiki) {
		v.fields = append(v.fields, visibility.InvalidLevel("/visibility/wiki"))
		return baseline, nil
	}
	if visibility.Above(out.Artifacts, projectLevel) {
		v.add("/visibility/artifacts", codeVisibilityAboveProject, "The artifacts level is at most the project's level, "+projectLevel+".")
	}
	if visibility.Above(out.Wiki, out.Artifacts) {
		v.add("/visibility/wiki", codeVisibilityWikiAboveArtifacts, "The wiki level is at most the artifacts level, "+out.Artifacts+".")
	}
	if len(v.fields) > 0 {
		return out, nil
	}
	rises := visibility.Above(out.Artifacts, baseline.Artifacts) || visibility.Above(out.Wiki, baseline.Wiki)
	if rises && c.RaiseGate != nil {
		if err := c.RaiseGate(ctx); err != nil {
			return Visibility{}, err
		}
	}
	opensPublic := out.Artifacts == visibility.Public && baseline.Artifacts != visibility.Public ||
		out.Wiki == visibility.Public && baseline.Wiki != visibility.Public
	if opensPublic && !s.publicAvailable() {
		return Visibility{}, visibility.PublicNotAllowed()
	}
	if out.Artifacts == visibility.Public && baseline.Artifacts != visibility.Public && fillsOnRead(d) {
		v.warnings = append(v.warnings, Warning{
			Code: WarningPublicPopulatesRemote, Pointer: "/visibility/artifacts",
			Detail: "A request without a credential fills the cache of this repository: its cacheFillAction is read. Set it to deploy to let only a deploying caller fill it.",
		})
	}
	return out, nil
}

// fillsOnRead reports whether the document is a remote repository whose reads fill the cache (cacheFillAction read, the
// default of R-AUTHZ-008).
func fillsOnRead(d *Document) bool {
	if d.Class != Remote {
		return false
	}
	var r struct {
		CacheFillAction string `json:"cacheFillAction"`
	}
	if len(d.Remote) > 0 && json.Unmarshal(d.Remote, &r) != nil {
		return false
	}
	return r.CacheFillAction == "" || r.CacheFillAction == FillRead
}

func (s *Service) publicAvailable() bool { return s.publicOK != nil && s.publicOK() }
