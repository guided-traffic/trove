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
// not widen (R-AUTHZ-031): requireAuthentication, hideExistence,
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
	RequireAuthentication bool    `json:"requireAuthentication"`
	HideExistence         string  `json:"hideExistence"`
	CacheFillAction       *string `json:"cacheFillAction,omitempty"`
	PackageOwnership      *string `json:"packageOwnership,omitempty"`
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
	s := accessSettings{RequireAuthentication: r.RequireAuthentication, HideExistence: r.HideExistence}
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
	details := mustJSON(map[string]any{"repository": after.Key, "repositoryId": after.ID, "before": b, "after": a})
	journal := mustJSON(map[string]any{"repositoryId": after.ID, "key": after.Key, "before": b, "after": a})
	return h.rec.RecordTx(ctx, tx, audit.Event{
		Type: EventSettingsChanged, Actor: c.Actor, Source: c.Source, Outcome: audit.Success,
		Subject:    audit.Subject{Kind: "repository", ID: after.ID, Name: after.Key},
		Repository: after.Key, Details: details, Journal: journal,
	})
}
