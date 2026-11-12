package project

import (
	"context"
	"strconv"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/visibility"
)

// Levels are the levels of a repository's two parts (R-AUTHZ-033).
type Levels struct{ Artifacts, Wiki string }

// Lowered is a repository whose levels a change of its project lowered: its qualified name and the levels it had before
// (R-API-057). After holds the levels it has now.
type Lowered struct {
	Repository string
	ID         string
	Before     Levels
	After      Levels
}

// chosenLevel returns the level a project is created with: the one the client names, else the installation's default
// (R-AUTHZ-033 rule 1, read in the creating transaction). A level that is no level is 422, and public while the default
// endpoint admits no request without a credential is 409 (rule 5).
func (s *service) chosenLevel(ctx context.Context, qs *q.Queries, named string) (string, error) {
	if named == "" {
		return visibility.Default(ctx, qs)
	}
	if !visibility.Valid(named) {
		return "", errs.E(errs.Invalid, errs.CodeValidation, "The level is one of private, internal and public.",
			errs.WithFields(visibility.InvalidLevel("/visibility")))
	}
	if named == visibility.Public && !s.publicAvailable() {
		return "", visibility.PublicNotAllowed()
	}
	return named, nil
}

func (s *service) publicAvailable() bool { return s.o.PublicAvailable != nil && s.o.PublicAvailable() }

// SetVisibility implements [Service] (R-API-057, R-AUTHZ-036). One transaction takes the project's row lock, compares the
// version, sets the level and, when it is lowered, takes every repository above it down to it with a new configuration version
// (R-AUTHZ-033 rule 3). It notifies the area authz, and the area repo_config for each lowered repository, and records
// authz.project.visibility_changed with the lowered repositories and authz.settings.changed with the cause project for each of
// them. The level it already has changes nothing and records nothing.
func (s *service) SetVisibility(ctx context.Context, actor authz.Subject, key string, expected int64, level string) (Project, []Lowered, int64, error) {
	if !visibility.Valid(level) {
		return Project{}, nil, 0, errs.E(errs.Invalid, errs.CodeValidation, "The level is one of private, internal and public.",
			errs.WithFields(visibility.InvalidLevel("/level")))
	}
	var out Project
	lowered := []Lowered{}
	var epoch int64
	err := s.inTx(ctx, func(tx meta.Tx, qs *q.Queries) error {
		id, err := lockProject(ctx, qs, key)
		if err != nil {
			return err
		}
		before, err := load(ctx, qs, key)
		if err != nil {
			return err
		}
		if expected != errs.AnyRevision && expected != before.Version {
			return errs.Stale("The project", before.Version)
		}
		if before.Visibility == level {
			out = before
			epoch, err = epochOf(ctx, qs)
			return err
		}
		if level == visibility.Public && !s.publicAvailable() {
			return visibility.PublicNotAllowed()
		}
		if err := qs.ProjectSetVisibility(ctx, q.ProjectSetVisibilityParams{ID: id, Level: level, Actor: actorID(actor)}); err != nil {
			return err
		}
		if visibility.Above(before.Visibility, level) {
			rows, err := qs.ProjectLowerRepositories(ctx, q.ProjectLowerRepositoriesParams{ProjectID: id, Level: level, Actor: actorID(actor)})
			if err != nil {
				return err
			}
			for _, r := range rows {
				name := key + "/" + r.Key
				lowered = append(lowered, Lowered{
					Repository: name, ID: strconv.FormatInt(r.ID, 10),
					Before: Levels{Artifacts: r.BeforeArtifacts, Wiki: r.BeforeWiki},
					After:  Levels{Artifacts: r.VisibilityArtifacts, Wiki: r.VisibilityWiki},
				})
				if err := s.o.Changes.Notify(tx, authz.AreaRepoConfig, payloadVersion,
					repoChange{ID: strconv.FormatInt(r.ID, 10), Key: name, Op: "updated", Version: r.ConfigVersion}); err != nil {
					return err
				}
			}
		}
		if err := s.notifyAuthz(tx, "project", id); err != nil {
			return err
		}
		if out, err = load(ctx, qs, key); err != nil {
			return err
		}
		afterEpoch(tx, &epoch, func(ctx context.Context, tx meta.Tx, epoch int64) error {
			if err := s.o.Recorder.RecordTx(ctx, tx, visibilityEvent(actor, out, before.Visibility, level, lowered, epoch)); err != nil {
				return err
			}
			for _, l := range lowered {
				ev := authz.LevelsChangedEvent(actorOf(actor), l.Repository, l.ID,
					authz.RepositoryLevels(l.Before), authz.RepositoryLevels(l.After), authz.CauseProject, epoch)
				if err := s.o.Recorder.RecordTx(ctx, tx, ev); err != nil {
					return err
				}
			}
			return nil
		})
		return nil
	})
	if err != nil {
		return Project{}, nil, 0, err
	}
	return out, lowered, epoch, nil
}

// visibilityEvent is authz.project.visibility_changed (R-AUTHZ-036). Its journal entry names the project by ID and the level
// after, which is what the replay compares (journal_replay_visibility.go in internal/authz).
func visibilityEvent(actor authz.Subject, p Project, before, after string, lowered []Lowered, epoch int64) audit.Event {
	type loweredRepo struct {
		Repository   string `json:"repository"`
		RepositoryID string `json:"repositoryId"`
		Before       Levels `json:"before"`
		After        Levels `json:"after"`
	}
	list := make([]loweredRepo, 0, len(lowered))
	for _, l := range lowered {
		list = append(list, loweredRepo{Repository: l.Repository, RepositoryID: l.ID, Before: l.Before, After: l.After})
	}
	return audit.Event{
		Type: audit.EventProjectVisibilityChanged, Actor: actorOf(actor), Outcome: audit.Success, Subject: subjectOf(p), Project: p.Key,
		Details: mustJSON(map[string]any{
			"epoch": epoch, "project": p.Key, "projectId": p.ID, "before": before, "after": after, "lowered": list,
		}),
		Journal: mustJSON(map[string]any{"projectId": p.ID, "project": p.Key, "before": before, "after": after}),
	}
}

// lowerMoved takes a repository that moved into a project of a lower level down to that level (R-AUTHZ-033 rule 3) and returns
// the lowering and the repository's configuration version after it; ok is false when nothing lay above the level.
func lowerMoved(ctx context.Context, qs *q.Queries, repoID int64, toProjectID string) (l Lowered, version int64, ok bool, err error) {
	levels, err := qs.ProjectLevelByID(ctx, toProjectID)
	if err != nil || len(levels) == 0 {
		return Lowered{}, 0, false, err
	}
	rows, err := qs.ProjectLowerRepository(ctx, q.ProjectLowerRepositoryParams{ID: repoID, Level: levels[0]})
	if err != nil || len(rows) == 0 {
		return Lowered{}, 0, false, err
	}
	r := rows[0]
	return Lowered{
		Before: Levels{Artifacts: r.BeforeArtifacts, Wiki: r.BeforeWiki},
		After:  Levels{Artifacts: r.VisibilityArtifacts, Wiki: r.VisibilityWiki},
	}, r.ConfigVersion, true, nil
}
