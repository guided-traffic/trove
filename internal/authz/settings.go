package authz

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/repository"
)

// EventSettingsChanged is written by the repository update when one of the
// authorization settings of a repository changed (R-AUTHZ-031).
const EventSettingsChanged = "authz.settings.changed"

// SettingsHook implements [repository.Hooks]: it records authz.settings.changed,
// flagged for the revocation journal, in the transaction of the repository update
// next to the repository's own event. The settings are the ones a restore must
// not widen (R-AUTHZ-031): hideExistence,
// cacheFillAction and packageOwnership. The hook changes no state, so it does not
// notify the authz area; the update notifies repo_config, which the grant index
// follows.
type SettingsHook struct{ rec audit.Recorder }

var _ repository.Hooks = (*SettingsHook)(nil)

// NewSettingsHook returns the hook; a nil recorder discards the events.
func NewSettingsHook(rec audit.Recorder) *SettingsHook {
	if rec == nil {
		rec = audit.Nop{}
	}
	return &SettingsHook{rec: rec}
}

// accessSettings are the authorization settings of one repository as the journal
// carries them: only the members that apply to the repository appear.
type accessSettings struct {
	HideExistence    string  `json:"hideExistence"`
	CacheFillAction  *string `json:"cacheFillAction,omitempty"`
	PackageOwnership *string `json:"packageOwnership,omitempty"`
	// The two levels are members of their own, named as the document names them (R-AUTHZ-036).
	VisibilityArtifacts string `json:"visibility.artifacts"`
	VisibilityWiki      string `json:"visibility.wiki"`
}

// settingsOf reads the settings from the repository model and its document.
func settingsOf(r repository.Repository) accessSettings {
	var doc struct {
		Local struct {
			PackageOwnership *string `json:"packageOwnership"`
		} `json:"local"`
		Remote struct {
			CacheFillAction *string `json:"cacheFillAction"`
		} `json:"remote"`
	}
	_ = json.Unmarshal(r.Config, &doc) // an unreadable document has none of the optional members
	s := accessSettings{HideExistence: r.HideExistence, VisibilityArtifacts: r.Visibility.Artifacts, VisibilityWiki: r.Visibility.Wiki}
	if s.HideExistence == "" {
		s.HideExistence = "inherit"
	}
	s.PackageOwnership = doc.Local.PackageOwnership
	if r.Class == repository.Remote {
		fill := "read" // the default of R-AUTHZ-008
		if doc.Remote.CacheFillAction != nil && *doc.Remote.CacheFillAction != "" {
			fill = *doc.Remote.CacheFillAction
		}
		s.CacheFillAction = &fill
	}
	return s
}

// changed returns the settings that differ, before and after, as the members the
// journal entry names; both are empty when nothing differs.
func changedSettings(before, after accessSettings) (b, a map[string]any) {
	b, a = map[string]any{}, map[string]any{}
	bm, am := toMap(before), toMap(after)
	for k, av := range am {
		if bv, ok := bm[k]; !ok || !reflect.DeepEqual(bv, av) {
			b[k], a[k] = bv, av
		}
	}
	for k, bv := range bm {
		if _, ok := am[k]; !ok {
			b[k], a[k] = bv, nil
		}
	}
	return b, a
}

func toMap(s accessSettings) map[string]any {
	raw, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// OnDelete implements [repository.Hooks]: a deletion changes none of the settings.
func (h *SettingsHook) OnDelete(context.Context, meta.Tx, repository.Caller, repository.Repository) error {
	return nil
}

// OnUpdated implements [repository.Hooks].
func (h *SettingsHook) OnUpdated(ctx context.Context, tx meta.Tx, c repository.Caller, before, after repository.Repository) error {
	b, a := changedSettings(settingsOf(before), settingsOf(after))
	if len(a) == 0 {
		return nil
	}
	details := mustJSON(map[string]any{"repository": after.Name.String(), "repositoryId": after.ID, "before": b, "after": a, "cause": CauseRequest})
	journal := mustJSON(map[string]any{"repositoryId": after.ID, "key": after.Name.String(), "before": b, "after": a})
	return h.rec.RecordTx(ctx, tx, audit.Event{
		Type: EventSettingsChanged, Actor: c.Actor, Source: c.Source, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "repository", ID: after.ID, Name: after.Name.String()},
		Repository: after.Name.String(), Details: details, Journal: journal,
	})
}

// The causes of an authz.settings.changed event for the levels of a repository (R-AUTHZ-036): the request that names them, the
// cascade of a project's lowered level, and the move to a project of a lower level.
const (
	CauseRequest = "request"
	CauseProject = "project"
	CauseMove    = "move"
)

// RepositoryLevels are the levels of a repository's two parts, as a string each.
type RepositoryLevels struct{ Artifacts, Wiki string }

// LevelsChangedEvent builds the authz.settings.changed event of a change of a repository's levels that no repository update
// wrote: the cascade of a lowered project and the move (R-AUTHZ-033 rule 3). Only the members that changed are named, as the
// repository update names them; the journal entry carries the same before and after (R-AUTHZ-031).
func LevelsChangedEvent(actor audit.Actor, name, repositoryID string, before, after RepositoryLevels, cause string, epoch int64) audit.Event {
	b, a := map[string]any{}, map[string]any{}
	if before.Artifacts != after.Artifacts {
		b["visibility.artifacts"], a["visibility.artifacts"] = before.Artifacts, after.Artifacts
	}
	if before.Wiki != after.Wiki {
		b["visibility.wiki"], a["visibility.wiki"] = before.Wiki, after.Wiki
	}
	return audit.Event{
		Type: EventSettingsChanged, Actor: actor, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "repository", ID: repositoryID, Name: name},
		Repository: name,
		Details:    mustJSON(map[string]any{"repository": name, "repositoryId": repositoryID, "before": b, "after": a, "cause": cause, "epoch": epoch}),
		Journal:    mustJSON(map[string]any{"repositoryId": repositoryID, "key": name, "before": b, "after": a}),
	}
}
